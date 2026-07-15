package visualpdf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	pdfconv "github.com/LynnColeArt/Inkbite/converters/pdf"
)

var (
	pagesPattern    = regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)
	pageSizePattern = regexp.MustCompile(`(?m)^Page size:\s+([0-9.]+) x ([0-9.]+) pts`)
	svgResourceRef  = regexp.MustCompile(`(?i)(?:xlink:)?(?:href|src)\s*=\s*["']\s*([^"']*)["']`)
	svgURLRef       = regexp.MustCompile(`(?i)url\(\s*["']?\s*([^"'\)\s]+)`)
	svgImageDataURI = regexp.MustCompile(`(?is)(<image\b[^>]*?\b(?:(?:xlink:)?href|src)\s*=\s*["'])data:([^"']+)(["'])`)
)

// LoadProfileSet loads an explicitly versioned profile file. JSON is used so
// every tolerance and renderer command remains exact and reviewable.
func LoadProfileSet(path string) (ProfileSet, error) {
	profilePath, err := filepath.Abs(path)
	if err != nil {
		return ProfileSet{}, fmt.Errorf("resolve visual profile set: %w", err)
	}
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return ProfileSet{}, fmt.Errorf("read visual profile set: %w", err)
	}
	var profiles ProfileSet
	if err := json.Unmarshal(data, &profiles); err != nil {
		return ProfileSet{}, fmt.Errorf("decode visual profile set: %w", err)
	}
	if profiles.SchemaVersion != ProfileSetSchemaVersion {
		return ProfileSet{}, fmt.Errorf("unsupported visual profile schema %q", profiles.SchemaVersion)
	}
	if err := validateProfiles(profiles.Profiles); err != nil {
		return ProfileSet{}, err
	}
	if err := verifyCalibrationEvidence(profiles.Profiles, filepath.Dir(profilePath)); err != nil {
		return ProfileSet{}, err
	}
	return profiles, nil
}

// Compile emits a new self-contained visual package. It does not mutate a
// previous package and fails closed before returning a manifest on malformed
// source input, unsafe SVG content, missing tools, or a failed visual gate.
func Compile(ctx context.Context, options CompileOptions) (Manifest, error) {
	if err := validateOptions(options); err != nil {
		return Manifest{}, err
	}
	input, err := filepath.Abs(options.InputPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve input PDF: %w", err)
	}
	input = filepath.Clean(input)
	info, err := os.Stat(input)
	if err != nil {
		return Manifest{}, fmt.Errorf("inspect input PDF: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Manifest{}, errors.New("input PDF must be a regular local file")
	}
	if !strings.EqualFold(filepath.Ext(input), ".pdf") {
		return Manifest{}, errors.New("input visual document must use the .pdf extension")
	}

	tools, err := resolveTools(options.Toolchain)
	if err != nil {
		return Manifest{}, err
	}
	if err := verifyToolchain(ctx, tools, options.Toolchain.Version); err != nil {
		return Manifest{}, err
	}

	output, err := filepath.Abs(options.OutputDirectory)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve output directory: %w", err)
	}
	if err := createEmptyOutputDirectory(output); err != nil {
		return Manifest{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(output)
		}
	}()

	sourcePath := filepath.Join(output, "source.pdf")
	if err := copyFile(input, sourcePath); err != nil {
		return Manifest{}, fmt.Errorf("retain source PDF: %w", err)
	}
	sourceArtifact, err := artifactFor(output, sourcePath, "application/pdf")
	if err != nil {
		return Manifest{}, err
	}
	sourceDocument, err := os.ReadFile(sourcePath)
	if err != nil {
		return Manifest{}, fmt.Errorf("read source PDF: %w", err)
	}

	pageCount, err := pageCount(ctx, tools.pdfinfo, sourcePath)
	if err != nil {
		return Manifest{}, err
	}
	pageDimensions, err := dimensionsForPages(ctx, tools.pdfinfo, sourcePath, pageCount)
	if err != nil {
		return Manifest{}, err
	}

	manifest := Manifest{
		SchemaVersion:    ManifestSchemaVersion,
		CompilerVersion:  nonEmpty(options.CompilerVersion, "dev"),
		GeneratedAt:      compilationTime(options).UTC().Format(time.RFC3339),
		Source:           sourceArtifact,
		Toolchain:        options.Toolchain,
		Pages:            make([]PageManifest, 0, pageCount),
		RemediationQueue: []RemediationItem{},
	}

	for page := 1; page <= pageCount; page++ {
		pageManifest, remediation, err := compilePage(ctx, output, sourcePath, sourceDocument, tools, options.Profiles, page, pageDimensions[page-1])
		if err != nil {
			return Manifest{}, fmt.Errorf("compile page %d: %w", page, err)
		}
		manifest.Pages = append(manifest.Pages, pageManifest)
		if remediation != nil {
			remediation.SourceSHA256 = sourceArtifact.SHA256
			manifest.RemediationQueue = append(manifest.RemediationQueue, *remediation)
		}
	}

	sort.Slice(manifest.RemediationQueue, func(i, j int) bool {
		if manifest.RemediationQueue[i].Page != manifest.RemediationQueue[j].Page {
			return manifest.RemediationQueue[i].Page < manifest.RemediationQueue[j].Page
		}
		return manifest.RemediationQueue[i].FailedProfile < manifest.RemediationQueue[j].FailedProfile
	})
	if err := writeJSON(filepath.Join(output, "manifest.json"), manifest); err != nil {
		return Manifest{}, fmt.Errorf("write visual manifest: %w", err)
	}
	cleanup = false
	return manifest, nil
}

type toolPaths struct {
	pdfinfo    string
	pdftocairo string
	pdftotext  string
}

func validateOptions(options CompileOptions) error {
	if strings.TrimSpace(options.InputPath) == "" {
		return errors.New("visual PDF input path is required")
	}
	if strings.TrimSpace(options.OutputDirectory) == "" {
		return errors.New("visual PDF output directory is required")
	}
	if strings.TrimSpace(options.Toolchain.Directory) == "" || strings.TrimSpace(options.Toolchain.Version) == "" {
		return errors.New("pinned Poppler directory and version are required")
	}
	if err := validateProfiles(options.Profiles); err != nil {
		return err
	}
	return verifyCalibrationEvidence(options.Profiles, "")
}

func validateProfiles(profiles []VisualProfile) error {
	if len(profiles) == 0 {
		return errors.New("at least one visual profile is required")
	}
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		id := strings.TrimSpace(profile.ID)
		if id == "" || strings.TrimSpace(profile.Version) == "" {
			return errors.New("every visual profile needs an id and version")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate visual profile id %q", id)
		}
		seen[id] = struct{}{}
		if profile.ReferenceDPI <= 0 {
			return fmt.Errorf("visual profile %q reference_dpi must be positive", id)
		}
		if strings.TrimSpace(profile.Renderer.Path) == "" || strings.TrimSpace(profile.Renderer.Version) == "" {
			return fmt.Errorf("visual profile %q needs a pinned SVG renderer path and version", id)
		}
		if countToken(profile.Renderer.Arguments, "{input}") != 1 || countToken(profile.Renderer.Arguments, "{output}") != 1 {
			return fmt.Errorf("visual profile %q renderer arguments need exactly one {input} and one {output}", id)
		}
		if strings.TrimSpace(profile.Calibration.CorpusID) == "" || strings.TrimSpace(profile.Calibration.Report) == "" {
			return fmt.Errorf("visual profile %q needs committed calibration corpus and report identifiers", id)
		}
		if !isSHA256Digest(profile.Calibration.ReportSHA256) {
			return fmt.Errorf("visual profile %q needs a lowercase SHA-256 calibration report hash", id)
		}
		if profile.Calibration.MaxChangedPixels < 0 {
			return fmt.Errorf("visual profile %q max_changed_pixels cannot be negative", id)
		}
	}
	return nil
}

func verifyCalibrationEvidence(profiles []VisualProfile, baseDirectory string) error {
	for index := range profiles {
		calibration := &profiles[index].Calibration
		reportPath, err := resolveCalibrationReport(calibration, baseDirectory)
		if err != nil {
			return fmt.Errorf("visual profile %q calibration evidence: %w", profiles[index].ID, err)
		}
		hash, err := sha256File(reportPath)
		if err != nil {
			return fmt.Errorf("visual profile %q read calibration report: %w", profiles[index].ID, err)
		}
		if hash != calibration.ReportSHA256 {
			return fmt.Errorf("visual profile %q calibration report hash does not match %s", profiles[index].ID, calibration.Report)
		}
	}
	return nil
}

func resolveCalibrationReport(calibration *Calibration, baseDirectory string) (string, error) {
	if calibration.reportPath != "" {
		return calibration.reportPath, nil
	}
	if baseDirectory == "" {
		if !filepath.IsAbs(calibration.Report) {
			return "", errors.New("report must be absolute when profiles are not loaded from a profile set")
		}
		calibration.reportPath = filepath.Clean(calibration.Report)
		return calibration.reportPath, nil
	}
	if filepath.IsAbs(calibration.Report) {
		return "", errors.New("report must be relative to the profile set")
	}
	baseDirectory, err := filepath.EvalSymlinks(filepath.Clean(baseDirectory))
	if err != nil {
		return "", fmt.Errorf("resolve profile set directory: %w", err)
	}
	path := filepath.Clean(filepath.Join(baseDirectory, filepath.FromSlash(calibration.Report)))
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve report: %w", err)
	}
	relative, err := filepath.Rel(baseDirectory, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("report escapes the profile set directory")
	}
	calibration.reportPath = path
	return path, nil
}

func isSHA256Digest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func countToken(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func resolveTools(toolchain Toolchain) (toolPaths, error) {
	directory, err := filepath.Abs(toolchain.Directory)
	if err != nil {
		return toolPaths{}, fmt.Errorf("resolve Poppler directory: %w", err)
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return toolPaths{}, fmt.Errorf("pinned Poppler directory is unavailable: %s", directory)
	}
	resolve := func(name string) (string, error) {
		path := filepath.Join(directory, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			return "", fmt.Errorf("pinned Poppler tool %q is unavailable or not executable", path)
		}
		return path, nil
	}
	pdfinfo, err := resolve("pdfinfo")
	if err != nil {
		return toolPaths{}, err
	}
	pdftocairo, err := resolve("pdftocairo")
	if err != nil {
		return toolPaths{}, err
	}
	pdftotext, err := resolve("pdftotext")
	if err != nil {
		return toolPaths{}, err
	}
	return toolPaths{pdfinfo: pdfinfo, pdftocairo: pdftocairo, pdftotext: pdftotext}, nil
}

func verifyToolchain(ctx context.Context, tools toolPaths, expectedVersion string) error {
	for _, tool := range []string{tools.pdfinfo, tools.pdftocairo, tools.pdftotext} {
		output, err := run(ctx, tool, "-v")
		if err != nil {
			return fmt.Errorf("run pinned Poppler tool %s: %w", filepath.Base(tool), err)
		}
		if !strings.Contains(string(output), expectedVersion) {
			return fmt.Errorf("pinned Poppler tool %s did not report required version %q", filepath.Base(tool), expectedVersion)
		}
	}
	return nil
}

func createEmptyOutputDirectory(output string) error {
	if info, err := os.Stat(output); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("visual PDF output exists and is not a directory: %s", output)
		}
		entries, readErr := os.ReadDir(output)
		if readErr != nil {
			return fmt.Errorf("inspect visual PDF output: %w", readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("visual PDF output directory must be empty: %s", output)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect visual PDF output: %w", err)
	}
	return os.MkdirAll(output, 0o755)
}

func pageCount(ctx context.Context, pdfinfo, input string) (int, error) {
	output, err := run(ctx, pdfinfo, input)
	if err != nil {
		return 0, fmt.Errorf("inspect PDF pages: %w", err)
	}
	match := pagesPattern.FindStringSubmatch(string(output))
	if len(match) != 2 {
		return 0, errors.New("pdfinfo did not report a page count")
	}
	pages, err := strconv.Atoi(match[1])
	if err != nil || pages <= 0 {
		return 0, errors.New("PDF page count is invalid")
	}
	return pages, nil
}

func dimensionsForPages(ctx context.Context, pdfinfo, input string, pages int) ([]PageDimensions, error) {
	dimensions := make([]PageDimensions, 0, pages)
	for page := 1; page <= pages; page++ {
		output, err := run(ctx, pdfinfo, "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), input)
		if err != nil {
			return nil, fmt.Errorf("inspect PDF page %d dimensions: %w", page, err)
		}
		match := pageSizePattern.FindStringSubmatch(string(output))
		if len(match) != 3 {
			return nil, fmt.Errorf("pdfinfo did not report page %d dimensions", page)
		}
		width, widthErr := strconv.ParseFloat(match[1], 64)
		height, heightErr := strconv.ParseFloat(match[2], 64)
		if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
			return nil, fmt.Errorf("PDF page %d dimensions are invalid", page)
		}
		dimensions = append(dimensions, PageDimensions{WidthPoints: width, HeightPoints: height})
	}
	return dimensions, nil
}

func compilePage(
	ctx context.Context,
	output, input string,
	sourceDocument []byte,
	tools toolPaths,
	profiles []VisualProfile,
	page int,
	dimensions PageDimensions,
) (PageManifest, *RemediationItem, error) {
	pageDirectory := filepath.Join(output, "pages", fmt.Sprintf("%04d", page))
	if err := os.MkdirAll(pageDirectory, 0o755); err != nil {
		return PageManifest{}, nil, err
	}
	semantic, textRuns, err := emitSemantics(ctx, tools.pdftotext, input, output, page, pageDirectory)
	if err != nil {
		return PageManifest{}, nil, err
	}
	sourceRasterAssets, err := emitSourceRasterAssets(sourceDocument, output, page, pageDirectory)
	if err != nil {
		return PageManifest{}, nil, err
	}
	references, err := emitReferences(ctx, tools.pdftocairo, input, output, page, pageDirectory, profiles)
	if err != nil {
		return PageManifest{}, nil, err
	}

	outlined, err := emitOutlinedCandidate(ctx, tools.pdftocairo, input, output, page, pageDirectory, profiles, references)
	if err != nil {
		return PageManifest{}, nil, err
	}
	sourceAware := unavailableSourceAwareCandidate()
	candidates := []Candidate{outlined, sourceAware}
	verified := make([]Candidate, 0, 1)
	for _, candidate := range candidates {
		if candidate.State == CandidateVerified {
			verified = append(verified, candidate)
		}
	}
	pageManifest := PageManifest{
		Page: page, Dimensions: dimensions, SemanticMarkdown: semantic, TextRuns: textRuns, SourceRasterAssets: sourceRasterAssets,
		Candidates: candidates, RemediationState: "none",
	}
	if len(verified) > 0 {
		sort.SliceStable(verified, func(i, j int) bool {
			if verified[i].InstalledByteCount != verified[j].InstalledByteCount {
				return verified[i].InstalledByteCount < verified[j].InstalledByteCount
			}
			return verified[i].Kind < verified[j].Kind
		})
		pageManifest.State = PageVerifiedSVG
		pageManifest.PrimaryDisplay = verified[0].SVG
		return pageManifest, nil, nil
	}

	fallback := references[0].Reference
	if fallback.ByteCount <= 0 || fallback.SHA256 == "" {
		pageManifest.State = PageRejected
		pageManifest.RemediationState = "rejected"
		return pageManifest, &RemediationItem{Page: page, CompilerReason: "no verified SVG and no verified deterministic reference raster", FailedProfile: firstFailedProfile(candidates)}, nil
	}
	pageManifest.State = PageRasterFallback
	pageManifest.RasterFallback = &fallback
	pageManifest.RemediationState = "queued"
	return pageManifest, &RemediationItem{Page: page, CompilerReason: candidateFailureReason(candidates), FailedProfile: firstFailedProfile(candidates)}, nil
}

func emitSourceRasterAssets(document []byte, output string, page int, pageDirectory string) ([]SourceRasterAsset, error) {
	assets, err := pdfconv.ExtractPageRasterAssets(document, page)
	if err != nil {
		return nil, fmt.Errorf("extract source raster assets: %w", err)
	}
	if len(assets) == 0 {
		return []SourceRasterAsset{}, nil
	}
	assetDirectory := filepath.Join(pageDirectory, "source-raster-assets")
	if err := os.MkdirAll(assetDirectory, 0o755); err != nil {
		return nil, err
	}
	result := make([]SourceRasterAsset, 0, len(assets))
	usedNames := make(map[string]int, len(assets))
	for _, asset := range assets {
		extension, err := assetExtension(asset.MediaType)
		if err != nil {
			return nil, err
		}
		base := safeName(asset.Name)
		usedNames[base]++
		if usedNames[base] > 1 {
			base = fmt.Sprintf("%s-%d", base, usedNames[base])
		}
		path := filepath.Join(assetDirectory, base+extension)
		if err := os.WriteFile(path, asset.Bytes, 0o644); err != nil {
			return nil, err
		}
		artifact, err := artifactFor(output, path, asset.MediaType)
		if err != nil {
			return nil, err
		}
		placements := make([]SourceRasterPlacement, len(asset.Placements))
		for index, placement := range asset.Placements {
			placements[index] = SourceRasterPlacement{Matrix: placement.Matrix}
		}
		result = append(result, SourceRasterAsset{
			Name: asset.Name, Role: asset.Role, MaskFor: asset.MaskFor, Placements: placements, Encoding: asset.Encoding, Artifact: artifact,
		})
	}
	return result, nil
}

func assetExtension(mediaType string) (string, error) {
	switch mediaType {
	case "image/jpeg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	default:
		return "", fmt.Errorf("unsupported source raster asset media type %q", mediaType)
	}
}

func emitSemantics(ctx context.Context, pdftotext, input, output string, page int, pageDirectory string) (Artifact, Artifact, error) {
	plain, err := run(ctx, pdftotext, "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-layout", "-enc", "UTF-8", input, "-")
	if err != nil {
		return Artifact{}, Artifact{}, fmt.Errorf("extract source text: %w", err)
	}
	semanticPath := filepath.Join(pageDirectory, "semantic.md")
	semantic := strings.TrimRight(string(plain), "\r\n")
	if semantic != "" {
		semantic += "\n"
	}
	if err := os.WriteFile(semanticPath, []byte(semantic), 0o644); err != nil {
		return Artifact{}, Artifact{}, err
	}

	bbox, err := run(ctx, pdftotext, "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-bbox", "-enc", "UTF-8", input, "-")
	if err != nil {
		return Artifact{}, Artifact{}, fmt.Errorf("extract positioned source text: %w", err)
	}
	runs, err := parseTextRuns(bbox)
	if err != nil {
		return Artifact{}, Artifact{}, fmt.Errorf("parse positioned source text: %w", err)
	}
	runsPath := filepath.Join(pageDirectory, "text-runs.json")
	if err := writeJSON(runsPath, runs); err != nil {
		return Artifact{}, Artifact{}, err
	}
	semanticArtifact, err := artifactFor(output, semanticPath, "text/markdown")
	if err != nil {
		return Artifact{}, Artifact{}, err
	}
	runsArtifact, err := artifactFor(output, runsPath, "application/json")
	if err != nil {
		return Artifact{}, Artifact{}, err
	}
	return semanticArtifact, runsArtifact, nil
}

func emitReferences(ctx context.Context, pdftocairo, input, output string, page int, pageDirectory string, profiles []VisualProfile) ([]Verification, error) {
	references := make([]Verification, 0, len(profiles))
	for _, profile := range profiles {
		prefix := filepath.Join(pageDirectory, "reference-"+safeName(profile.ID))
		if _, err := run(ctx, pdftocairo, "-png", "-singlefile", "-r", strconv.Itoa(profile.ReferenceDPI), "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), input, prefix); err != nil {
			return nil, fmt.Errorf("render %s reference: %w", profile.ID, err)
		}
		referencePath := prefix + ".png"
		if _, err := inspectPNG(referencePath); err != nil {
			return nil, fmt.Errorf("verify %s reference: %w", profile.ID, err)
		}
		artifact, err := artifactFor(output, referencePath, "image/png")
		if err != nil {
			return nil, err
		}
		references = append(references, Verification{ProfileID: profile.ID, ProfileVersion: profile.Version, Reference: artifact, Calibration: profile.Calibration})
	}
	return references, nil
}

func emitOutlinedCandidate(ctx context.Context, pdftocairo, input, output string, page int, pageDirectory string, profiles []VisualProfile, references []Verification) (Candidate, error) {
	svgPath := filepath.Join(pageDirectory, "outlined-glyph.svg")
	if _, err := run(ctx, pdftocairo, "-svg", "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), input, svgPath); err != nil {
		return Candidate{}, fmt.Errorf("emit Poppler/Cairo outlined SVG: %w", err)
	}
	if err := validateSVG(svgPath); err != nil {
		return Candidate{}, fmt.Errorf("unsafe or unsupported Poppler SVG: %w", err)
	}
	referencedAssets, err := externalizeSVGImageAssets(output, svgPath, filepath.Join(pageDirectory, "outlined-assets"))
	if err != nil {
		return Candidate{}, fmt.Errorf("externalize Poppler SVG image assets: %w", err)
	}
	if err := validateSVGWithAssets(svgPath, output, referencedAssets); err != nil {
		return Candidate{}, fmt.Errorf("unsafe or unsupported rewritten Poppler SVG: %w", err)
	}
	artifact, err := artifactFor(output, svgPath, "image/svg+xml")
	if err != nil {
		return Candidate{}, err
	}
	installedBytes := artifact.ByteCount
	for _, asset := range referencedAssets {
		installedBytes += asset.ByteCount
	}
	candidate := Candidate{Kind: "outlined_glyph", State: CandidateVerified, SVG: &artifact, ReferencedAssets: referencedAssets, InstalledByteCount: installedBytes, Verification: make([]Verification, 0, len(profiles))}
	for index, profile := range profiles {
		verification := references[index]
		renderedPath := filepath.Join(pageDirectory, "rendered-"+safeName(profile.ID)+".png")
		_, err := renderSVG(ctx, profile, svgPath, renderedPath)
		if err != nil {
			verification.Passed = false
			verification.Reason = fmt.Sprintf("offline SVG renderer failed: %v", err)
			candidate.State = CandidateFailed
			candidate.Verification = append(candidate.Verification, verification)
			continue
		}
		renderedArtifact, artifactErr := artifactFor(output, renderedPath, "image/png")
		if artifactErr != nil {
			verification.Passed = false
			verification.Reason = artifactErr.Error()
			candidate.State = CandidateFailed
			candidate.Verification = append(candidate.Verification, verification)
			continue
		}
		verification.Rendered = &renderedArtifact
		comparison, compareErr := comparePNG(references[index].Reference, verification.Rendered, output, profile.Calibration)
		if compareErr != nil {
			verification.Passed = false
			verification.Reason = compareErr.Error()
			candidate.State = CandidateFailed
		} else {
			verification.MaxChannelDelta = comparison.maxDelta
			verification.ChangedPixels = comparison.changedPixels
			verification.Passed = comparison.passed
			if !comparison.passed {
				verification.Reason = fmt.Sprintf("visual difference exceeds calibration %s", profile.Calibration.Report)
				candidate.State = CandidateFailed
			}
		}
		candidate.Verification = append(candidate.Verification, verification)
	}
	if candidate.State == CandidateVerified {
		for _, verification := range candidate.Verification {
			if !verification.Passed {
				candidate.State = CandidateFailed
				break
			}
		}
	}
	return candidate, nil
}

func unavailableSourceAwareCandidate() Candidate {
	return Candidate{
		Kind:             "source_aware_text",
		State:            CandidateUnavailable,
		ReferencedAssets: []Artifact{},
		Verification:     []Verification{},
		Reason:           "No source font program, glyph mapping, and approved embedding policy were supplied; outlined glyph candidate remains required.",
	}
}

func renderSVG(ctx context.Context, profile VisualProfile, input, output string) (string, error) {
	rendererPath, err := filepath.Abs(profile.Renderer.Path)
	if err != nil {
		return "", fmt.Errorf("resolve SVG renderer: %w", err)
	}
	info, err := os.Stat(rendererPath)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return "", errors.New("qualified SVG renderer is unavailable or not executable")
	}
	versionOutput, err := run(ctx, rendererPath, "--version")
	if err != nil {
		return "", fmt.Errorf("inspect SVG renderer version: %w", err)
	}
	if !strings.Contains(string(versionOutput), profile.Renderer.Version) {
		return "", fmt.Errorf("SVG renderer did not report required version %q", profile.Renderer.Version)
	}
	args := make([]string, len(profile.Renderer.Arguments))
	for index, argument := range profile.Renderer.Arguments {
		args[index] = strings.NewReplacer("{input}", input, "{output}", output).Replace(argument)
	}
	if _, err := run(ctx, rendererPath, args...); err != nil {
		return "", err
	}
	if _, err := inspectPNG(output); err != nil {
		return "", err
	}
	return output, nil
}

type visualComparison struct {
	maxDelta      uint8
	changedPixels int
	passed        bool
}

func comparePNG(reference Artifact, rendered *Artifact, output string, calibration Calibration) (visualComparison, error) {
	if rendered == nil {
		return visualComparison{}, errors.New("SVG renderer did not emit an artifact")
	}
	referenceImage, err := decodePNG(filepath.Join(output, filepath.FromSlash(reference.Locator)))
	if err != nil {
		return visualComparison{}, fmt.Errorf("decode reference: %w", err)
	}
	renderedImage, err := decodePNG(filepath.Join(output, filepath.FromSlash(rendered.Locator)))
	if err != nil {
		return visualComparison{}, fmt.Errorf("decode SVG render: %w", err)
	}
	if referenceImage.Bounds() != renderedImage.Bounds() {
		return visualComparison{}, errors.New("reference and SVG render dimensions differ")
	}
	comparison := visualComparison{passed: true}
	bounds := referenceImage.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			rr, rg, rb, ra := referenceImage.At(x, y).RGBA()
			sr, sg, sb, sa := renderedImage.At(x, y).RGBA()
			delta := maxUint8(absChannel(rr, sr), absChannel(rg, sg), absChannel(rb, sb), absChannel(ra, sa))
			if delta > comparison.maxDelta {
				comparison.maxDelta = delta
			}
			if delta > calibration.MaxChannelDelta {
				comparison.changedPixels++
			}
		}
	}
	comparison.passed = comparison.changedPixels <= calibration.MaxChangedPixels
	return comparison, nil
}

func absChannel(left, right uint32) uint8 {
	if left < right {
		left, right = right, left
	}
	return uint8((left - right) / 257)
}

func maxUint8(values ...uint8) uint8 {
	maximum := uint8(0)
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func decodePNG(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	imageValue, format, err := image.Decode(file)
	if err != nil {
		return nil, err
	}
	if format != "png" {
		return nil, fmt.Errorf("expected PNG, got %s", format)
	}
	return imageValue, nil
}

func inspectPNG(path string) (image.Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return image.Config{}, err
	}
	defer file.Close()
	config, format, err := image.DecodeConfig(file)
	if err != nil {
		return image.Config{}, err
	}
	if format != "png" || config.Width <= 0 || config.Height <= 0 {
		return image.Config{}, errors.New("render output is not a non-empty PNG")
	}
	return config, nil
}

func validateSVG(path string) error {
	return validateSVGReferences(path, nil)
}

func validateSVGWithAssets(path, output string, assets []Artifact) error {
	allowed := make(map[string]struct{}, len(assets))
	for _, asset := range assets {
		assetPath := filepath.Join(output, filepath.FromSlash(asset.Locator))
		reference, err := filepath.Rel(filepath.Dir(path), assetPath)
		if err != nil || reference == ".." || strings.HasPrefix(reference, ".."+string(filepath.Separator)) {
			return errors.New("SVG asset is outside the SVG directory")
		}
		allowed[filepath.ToSlash(reference)] = struct{}{}
	}
	return validateSVGReferences(path, allowed)
}

func validateSVGReferences(path string, localAssets map[string]struct{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lower := strings.ToLower(string(data))
	if !strings.Contains(lower, "<svg") || strings.Contains(lower, "<script") || strings.Contains(lower, "<foreignobject") || strings.Contains(lower, "<iframe") || strings.Contains(lower, "<!doctype") || strings.Contains(lower, "<!entity") {
		return errors.New("SVG contains an unsupported structural construct")
	}
	if strings.Contains(lower, "srcset=") || strings.Contains(lower, "sizes=") {
		return errors.New("SVG contains unsupported responsive-image metadata")
	}
	for _, match := range svgResourceRef.FindAllStringSubmatch(lower, -1) {
		if !safeSVGResourceReference(match[1], localAssets) {
			return errors.New("SVG contains an unsafe external resource reference")
		}
	}
	for _, match := range svgURLRef.FindAllStringSubmatch(lower, -1) {
		if !safeSVGResourceReference(match[1], localAssets) {
			return errors.New("SVG contains an unsafe CSS resource reference")
		}
	}
	for _, prohibited := range []string{"onload=", "onclick=", "onerror="} {
		if strings.Contains(lower, prohibited) {
			return fmt.Errorf("SVG contains unsafe external or executable reference %q", prohibited)
		}
	}
	return nil
}

func safeSVGResourceReference(value string, localAssets map[string]struct{}) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if strings.HasPrefix(value, "#") || strings.HasPrefix(value, "data:") {
		return true
	}
	_, allowed := localAssets[value]
	return allowed
}

func externalizeSVGImageAssets(output, svgPath, assetDirectory string) ([]Artifact, error) {
	svg, err := os.ReadFile(svgPath)
	if err != nil {
		return nil, err
	}
	matches := svgImageDataURI.FindAllSubmatchIndex(svg, -1)
	if len(matches) == 0 {
		return []Artifact{}, nil
	}
	if err := os.MkdirAll(assetDirectory, 0o755); err != nil {
		return nil, err
	}
	byContent := make(map[string]string, len(matches))
	assets := make([]Artifact, 0, len(matches))
	var rewritten bytes.Buffer
	position := 0
	for _, match := range matches {
		dataURI := string(svg[match[4]:match[5]])
		mediaType, data, err := decodeSVGImageDataURI(dataURI)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		key := mediaType + ":" + hex.EncodeToString(digest[:])
		reference, exists := byContent[key]
		if !exists {
			extension, err := assetExtension(mediaType)
			if err != nil {
				return nil, err
			}
			name := fmt.Sprintf("a%d%s", len(assets)+1, extension)
			path := filepath.Join(assetDirectory, name)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return nil, err
			}
			artifact, err := artifactFor(output, path, mediaType)
			if err != nil {
				return nil, err
			}
			reference, err = filepath.Rel(filepath.Dir(svgPath), path)
			if err != nil || reference == ".." || strings.HasPrefix(reference, ".."+string(filepath.Separator)) {
				return nil, errors.New("externalized SVG image asset is outside the SVG directory")
			}
			reference = filepath.ToSlash(reference)
			byContent[key] = reference
			assets = append(assets, artifact)
		}
		rewritten.Write(svg[position:match[3]])
		rewritten.WriteString(reference)
		rewritten.Write(svg[match[6]:match[7]])
		position = match[1]
	}
	rewritten.Write(svg[position:])
	if err := os.WriteFile(svgPath, rewritten.Bytes(), 0o644); err != nil {
		return nil, err
	}
	return assets, nil
}

func decodeSVGImageDataURI(value string) (string, []byte, error) {
	metadata, encoded, found := strings.Cut(value, ",")
	if !found {
		return "", nil, errors.New("SVG image data URI is missing a payload")
	}
	parts := strings.Split(metadata, ";")
	if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[1]), "base64") {
		return "", nil, errors.New("SVG image data URI must use a single base64 encoding marker")
	}
	mediaType := strings.ToLower(strings.TrimSpace(parts[0]))
	if mediaType != "image/jpeg" && mediaType != "image/png" {
		return "", nil, fmt.Errorf("unsupported SVG image data URI media type %q", mediaType)
	}
	encoded = strings.ReplaceAll(strings.ReplaceAll(encoded, "\n", ""), "\r", "")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, fmt.Errorf("decode SVG image data URI: %w", err)
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", nil, fmt.Errorf("decode SVG image data URI image: %w", err)
	}
	if (mediaType == "image/jpeg" && format != "jpeg") || (mediaType == "image/png" && format != "png") {
		return "", nil, fmt.Errorf("SVG image data URI media type %q does not match decoded %s", mediaType, format)
	}
	return mediaType, data, nil
}

func parseTextRuns(data []byte) ([]TextRun, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	runs := []TextRun{}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "word" {
			continue
		}
		attributes := make(map[string]string, len(start.Attr))
		for _, attribute := range start.Attr {
			attributes[attribute.Name.Local] = attribute.Value
		}
		var text string
		if err := decoder.DecodeElement(&text, &start); err != nil {
			return nil, err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		xMin, errOne := strconv.ParseFloat(attributes["xMin"], 64)
		yMin, errTwo := strconv.ParseFloat(attributes["yMin"], 64)
		xMax, errThree := strconv.ParseFloat(attributes["xMax"], 64)
		yMax, errFour := strconv.ParseFloat(attributes["yMax"], 64)
		if errOne != nil || errTwo != nil || errThree != nil || errFour != nil || math.IsNaN(xMin) || math.IsNaN(yMin) || math.IsNaN(xMax) || math.IsNaN(yMax) {
			return nil, errors.New("PDF text run has invalid bounds")
		}
		runs = append(runs, TextRun{Text: text, XMin: xMin, YMin: yMin, XMax: xMax, YMax: yMax})
	}
	return runs, nil
}

func firstFailedProfile(candidates []Candidate) string {
	for _, candidate := range candidates {
		for _, verification := range candidate.Verification {
			if !verification.Passed {
				return verification.ProfileID
			}
		}
	}
	return "source_aware_font_policy"
}

func candidateFailureReason(candidates []Candidate) string {
	for _, candidate := range candidates {
		if candidate.State == CandidateFailed {
			for _, verification := range candidate.Verification {
				if verification.Reason != "" {
					return verification.Reason
				}
			}
		}
	}
	return "no candidate passed the visual gate"
}

func artifactFor(output, path, mediaType string) (Artifact, error) {
	relative, err := filepath.Rel(output, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Artifact{}, errors.New("artifact escapes visual package output")
	}
	info, err := os.Stat(path)
	if err != nil {
		return Artifact{}, err
	}
	hash, err := sha256File(path)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Locator: filepath.ToSlash(relative), MediaType: mediaType, ByteCount: info.Size(), SHA256: hash}, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = output.Close() }()
	_, err = io.Copy(output, input)
	return err
}

func run(ctx context.Context, path string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, path, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s: %s", filepath.Base(path), strings.Join(arguments, " "), strings.TrimSpace(string(output)))
	}
	return output, nil
}

func safeName(value string) string {
	var builder strings.Builder
	for _, runeValue := range value {
		if (runeValue >= 'a' && runeValue <= 'z') || (runeValue >= 'A' && runeValue <= 'Z') || (runeValue >= '0' && runeValue <= '9') || runeValue == '-' || runeValue == '_' {
			builder.WriteRune(runeValue)
		} else {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-")
	return nonEmpty(result, "profile")
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func compilationTime(options CompileOptions) time.Time {
	if options.Now != nil {
		return options.Now()
	}
	return time.Now()
}
