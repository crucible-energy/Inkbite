package visualpdf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ManifestSHA256 returns the digest that a package registry must retain with a
// visual-PDF package. The manifest cannot securely attest to itself, so callers
// must obtain this value from the trusted package record rather than the package
// directory they are about to load.
func ManifestSHA256(directory string) (string, error) {
	root, err := packageDirectory(directory)
	if err != nil {
		return "", err
	}
	data, err := readPackageFile(root, "manifest.json")
	if err != nil {
		return "", fmt.Errorf("read package manifest: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// LoadVerifiedPackage reads a package only when its externally trusted manifest
// digest and every declared local artifact are intact. It rejects symlinks,
// escaping locators, unsafe SVG references, and package states that cannot ship.
func LoadVerifiedPackage(directory, expectedManifestSHA256 string) (Manifest, error) {
	if !isSHA256Digest(expectedManifestSHA256) {
		return Manifest{}, errors.New("expected manifest SHA-256 must be a lowercase SHA-256 digest")
	}
	root, err := packageDirectory(directory)
	if err != nil {
		return Manifest{}, err
	}
	data, err := readPackageFile(root, "manifest.json")
	if err != nil {
		return Manifest{}, fmt.Errorf("read package manifest: %w", err)
	}
	digest := sha256.Sum256(data)
	if actual := hex.EncodeToString(digest[:]); actual != expectedManifestSHA256 {
		return Manifest{}, errors.New("package manifest SHA-256 does not match the trusted package record")
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode package manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("decode package manifest: trailing JSON")
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, fmt.Errorf("unsupported package manifest schema %q", manifest.SchemaVersion)
	}
	if err := verifyPackageManifest(root, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func packageDirectory(directory string) (string, error) {
	root, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve package directory: %w", err)
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect package directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("visual package directory must be a non-symlink directory")
	}
	return root, nil
}

func readPackageFile(root, locator string) ([]byte, error) {
	path, err := packagePath(root, locator)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("package artifact must be a regular non-symlink file")
	}
	return os.ReadFile(path)
}

func packagePath(root, locator string) (string, error) {
	if locator == "" || filepath.IsAbs(locator) || strings.Contains(locator, "\\") {
		return "", errors.New("package artifact locator must be a non-empty slash-separated relative path")
	}
	local := filepath.FromSlash(locator)
	if filepath.Clean(local) != local || local == "." || strings.HasPrefix(local, ".."+string(filepath.Separator)) || local == ".." {
		return "", errors.New("package artifact locator escapes the package directory")
	}
	path := filepath.Join(root, local)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("package artifact locator escapes the package directory")
	}
	current := root
	for _, component := range strings.Split(filepath.ToSlash(filepath.Dir(local)), "/") {
		if component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("package artifact parent must be a non-symlink directory")
		}
	}
	return path, nil
}

type packageVerifier struct {
	root      string
	artifacts map[string]Artifact
}

func verifyPackageManifest(root string, manifest Manifest) error {
	if strings.TrimSpace(manifest.CompilerVersion) == "" {
		return errors.New("package manifest compiler_version is required")
	}
	if _, err := time.Parse(time.RFC3339, manifest.GeneratedAt); err != nil {
		return fmt.Errorf("package manifest generated_at is invalid: %w", err)
	}
	if strings.TrimSpace(manifest.Toolchain.Directory) == "" || strings.TrimSpace(manifest.Toolchain.Version) == "" {
		return errors.New("package manifest toolchain is required")
	}
	if len(manifest.Pages) == 0 {
		return errors.New("package manifest has no pages")
	}
	verifier := packageVerifier{root: root, artifacts: make(map[string]Artifact)}
	if _, err := verifier.artifact("source", manifest.Source, "application/pdf"); err != nil {
		return err
	}
	if manifest.Source.Locator != "source.pdf" {
		return errors.New("package source must be retained as source.pdf")
	}
	for index, page := range manifest.Pages {
		if err := verifier.page(page, index+1); err != nil {
			return err
		}
	}
	return verifyRemediationQueue(manifest)
}

func (verifier *packageVerifier) page(page PageManifest, expectedPage int) error {
	if page.Page != expectedPage {
		return fmt.Errorf("package page order is invalid at page %d", expectedPage)
	}
	if !positiveFinite(page.Dimensions.WidthPoints) || !positiveFinite(page.Dimensions.HeightPoints) {
		return fmt.Errorf("package page %d dimensions are invalid", page.Page)
	}
	if _, err := verifier.artifact(fmt.Sprintf("page %d semantic markdown", page.Page), page.SemanticMarkdown, "text/markdown"); err != nil {
		return err
	}
	if _, err := verifier.artifact(fmt.Sprintf("page %d text runs", page.Page), page.TextRuns, "application/json"); err != nil {
		return err
	}
	verifiedSVGs := make(map[Artifact]struct{})
	for index, candidate := range page.Candidates {
		if err := verifier.candidate(page.Page, candidate, index, verifiedSVGs); err != nil {
			return err
		}
	}
	for _, asset := range page.SourceRasterAssets {
		if asset.Role != "image" && asset.Role != "mask" {
			return fmt.Errorf("page %d source raster asset %q has an invalid role", page.Page, asset.Name)
		}
		if strings.TrimSpace(asset.Encoding) == "" {
			return fmt.Errorf("page %d source raster asset %q is missing its encoding", page.Page, asset.Name)
		}
		if _, err := verifier.artifact(fmt.Sprintf("page %d source raster asset %q", page.Page, asset.Name), asset.Artifact, ""); err != nil {
			return err
		}
		if asset.Artifact.MediaType != "image/jpeg" && asset.Artifact.MediaType != "image/png" {
			return fmt.Errorf("page %d source raster asset %q has unsupported media type %q", page.Page, asset.Name, asset.Artifact.MediaType)
		}
	}
	switch page.State {
	case PageVerifiedSVG:
		if page.PrimaryDisplay == nil || page.RasterFallback != nil || page.RemediationState != "none" {
			return fmt.Errorf("page %d verified SVG state is inconsistent", page.Page)
		}
		if _, err := verifier.artifact(fmt.Sprintf("page %d primary display", page.Page), *page.PrimaryDisplay, "image/svg+xml"); err != nil {
			return err
		}
		if _, ok := verifiedSVGs[*page.PrimaryDisplay]; !ok {
			return fmt.Errorf("page %d primary display is not a verified candidate", page.Page)
		}
	case PageRasterFallback:
		if page.PrimaryDisplay != nil || page.RasterFallback == nil || page.RemediationState != "queued" {
			return fmt.Errorf("page %d raster fallback state is inconsistent", page.Page)
		}
		if _, err := verifier.artifact(fmt.Sprintf("page %d raster fallback", page.Page), *page.RasterFallback, "image/png"); err != nil {
			return err
		}
	case PageRejected:
		return fmt.Errorf("page %d is rejected and cannot be loaded for display", page.Page)
	default:
		return fmt.Errorf("page %d has an unknown display state %q", page.Page, page.State)
	}
	return nil
}

func (verifier *packageVerifier) candidate(page int, candidate Candidate, index int, verifiedSVGs map[Artifact]struct{}) error {
	if strings.TrimSpace(candidate.Kind) == "" {
		return fmt.Errorf("page %d candidate %d has no kind", page, index)
	}
	switch candidate.State {
	case CandidateVerified, CandidateFailed:
		if candidate.SVG == nil {
			return fmt.Errorf("page %d candidate %q has no SVG", page, candidate.Kind)
		}
		svgPath, err := verifier.artifact(fmt.Sprintf("page %d candidate %q SVG", page, candidate.Kind), *candidate.SVG, "image/svg+xml")
		if err != nil {
			return err
		}
		installedBytes := candidate.SVG.ByteCount
		for _, asset := range candidate.ReferencedAssets {
			if _, err := verifier.artifact(fmt.Sprintf("page %d candidate %q asset", page, candidate.Kind), asset, ""); err != nil {
				return err
			}
			installedBytes += asset.ByteCount
		}
		if candidate.InstalledByteCount != installedBytes {
			return fmt.Errorf("page %d candidate %q installed byte count is inconsistent", page, candidate.Kind)
		}
		if err := validateSVGWithAssets(svgPath, verifier.root, candidate.ReferencedAssets); err != nil {
			return fmt.Errorf("page %d candidate %q SVG is unsafe: %w", page, candidate.Kind, err)
		}
		if len(candidate.Verification) == 0 {
			return fmt.Errorf("page %d candidate %q has no visual verification", page, candidate.Kind)
		}
		allPassed := true
		for _, verification := range candidate.Verification {
			if strings.TrimSpace(verification.ProfileID) == "" || strings.TrimSpace(verification.ProfileVersion) == "" {
				return fmt.Errorf("page %d candidate %q has an incomplete visual profile", page, candidate.Kind)
			}
			if _, err := verifier.artifact(fmt.Sprintf("page %d candidate %q reference", page, candidate.Kind), verification.Reference, "image/png"); err != nil {
				return err
			}
			if verification.Rendered == nil {
				if verification.Passed {
					return fmt.Errorf("page %d candidate %q passed without a renderer capture", page, candidate.Kind)
				}
			} else if _, err := verifier.artifact(fmt.Sprintf("page %d candidate %q renderer capture", page, candidate.Kind), *verification.Rendered, "image/png"); err != nil {
				return err
			}
			allPassed = allPassed && verification.Passed
		}
		if candidate.State == CandidateVerified && !allPassed {
			return fmt.Errorf("page %d candidate %q is marked verified without passing every profile", page, candidate.Kind)
		}
		if candidate.State == CandidateFailed && allPassed {
			return fmt.Errorf("page %d candidate %q is marked failed without a failed profile", page, candidate.Kind)
		}
		if candidate.State == CandidateVerified {
			verifiedSVGs[*candidate.SVG] = struct{}{}
		}
	case CandidateUnavailable:
		if candidate.SVG != nil || candidate.InstalledByteCount != 0 || len(candidate.ReferencedAssets) != 0 || len(candidate.Verification) != 0 || strings.TrimSpace(candidate.Reason) == "" {
			return fmt.Errorf("page %d unavailable candidate %q is inconsistent", page, candidate.Kind)
		}
	default:
		return fmt.Errorf("page %d candidate %q has an unknown state %q", page, candidate.Kind, candidate.State)
	}
	return nil
}

func (verifier *packageVerifier) artifact(label string, artifact Artifact, expectedMediaType string) (string, error) {
	if expectedMediaType != "" && artifact.MediaType != expectedMediaType {
		return "", fmt.Errorf("%s has media type %q, expected %q", label, artifact.MediaType, expectedMediaType)
	}
	if !isSHA256Digest(artifact.SHA256) || artifact.ByteCount < 0 || strings.TrimSpace(artifact.MediaType) == "" {
		return "", fmt.Errorf("%s has invalid artifact metadata", label)
	}
	if recorded, exists := verifier.artifacts[artifact.Locator]; exists {
		if recorded != artifact {
			return "", fmt.Errorf("package artifact %q has conflicting metadata", artifact.Locator)
		}
		return packagePath(verifier.root, artifact.Locator)
	}
	data, err := readPackageFile(verifier.root, artifact.Locator)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(data)) != artifact.ByteCount {
		return "", fmt.Errorf("%s byte count does not match the package manifest", label)
	}
	digest := sha256.Sum256(data)
	if actual := hex.EncodeToString(digest[:]); actual != artifact.SHA256 {
		return "", fmt.Errorf("%s SHA-256 does not match the package manifest", label)
	}
	verifier.artifacts[artifact.Locator] = artifact
	return packagePath(verifier.root, artifact.Locator)
}

func verifyRemediationQueue(manifest Manifest) error {
	previous := RemediationItem{}
	queued := make(map[int]struct{})
	for index, item := range manifest.RemediationQueue {
		if item.Page <= 0 || item.Page > len(manifest.Pages) || item.SourceSHA256 != manifest.Source.SHA256 || strings.TrimSpace(item.CompilerReason) == "" || strings.TrimSpace(item.FailedProfile) == "" {
			return errors.New("package remediation queue contains an invalid item")
		}
		if manifest.Pages[item.Page-1].State != PageRasterFallback {
			return fmt.Errorf("package remediation item for page %d does not match a raster fallback", item.Page)
		}
		if _, exists := queued[item.Page]; exists {
			return fmt.Errorf("package remediation queue duplicates page %d", item.Page)
		}
		queued[item.Page] = struct{}{}
		if index > 0 && remediationLess(item, previous) {
			return errors.New("package remediation queue is not deterministic")
		}
		previous = item
	}
	for _, page := range manifest.Pages {
		_, queuedPage := queued[page.Page]
		if (page.State == PageRasterFallback) != queuedPage {
			return fmt.Errorf("package page %d remediation state does not match the remediation queue", page.Page)
		}
	}
	return nil
}

func remediationLess(left, right RemediationItem) bool {
	if left.Page != right.Page {
		return left.Page < right.Page
	}
	return left.FailedProfile < right.FailedProfile
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}
