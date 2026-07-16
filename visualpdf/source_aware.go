package visualpdf

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dslipak/pdf"
)

var (
	popplerGlyphDefinition = regexp.MustCompile(`(?i)\bid\s*=\s*["']glyph-[^"']+["']`)
	popplerGlyphReference  = regexp.MustCompile(`(?i)\b(?:xlink:)?href\s*=\s*["']#glyph-[^"']+["']`)
)

// sourceAwarePage is the source-text information that can be faithfully
// represented in an SVG only when every used font passes the checks below.
// It intentionally keeps individual PDF text operations: merging them would
// discard spacing and positioning information that the visual gate needs.
type sourceAwarePage struct {
	fonts []sourceAwareFont
	runs  []sourceAwareRun
}

type sourceAwareFont struct {
	family     string
	program    []byte
	characters map[rune]struct{}
}

type sourceAwareRun struct {
	family   string
	text     string
	fontSize float64
	x        float64
	y        float64
	width    float64
}

func readSourceAwarePage(document []byte, pageNumber int) (result sourceAwarePage, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("source-aware font inspection failed: %v", recovered)
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(document), int64(len(document)))
	if err != nil {
		return sourceAwarePage{}, fmt.Errorf("source-aware font inspection cannot read PDF: %w", err)
	}
	if pageNumber <= 0 || pageNumber > reader.NumPage() {
		return sourceAwarePage{}, errors.New("source-aware font inspection page is unavailable")
	}
	page := reader.Page(pageNumber)
	if page.V.IsNull() {
		return sourceAwarePage{}, errors.New("source-aware font inspection page is null")
	}
	text := page.Content().Text
	if len(text) == 0 {
		return sourceAwarePage{}, errors.New("source-aware text candidate requires drawable PDF text")
	}
	fonts := make(map[string]pdf.Font)
	for _, name := range page.Fonts() {
		font := page.Font(name)
		base := strings.TrimPrefix(font.BaseFont(), fontSubsetPrefix(font.BaseFont()))
		if base != "" {
			fonts[base] = font
		}
	}
	byName := make(map[string]int, len(fonts))
	for _, run := range text {
		if run.Font == "" || run.S == "" {
			return sourceAwarePage{}, errors.New("source-aware text candidate has incomplete PDF text runs")
		}
		if !finite(run.FontSize) || !finite(run.X) || !finite(run.Y) || !finite(run.W) || run.FontSize == 0 || run.W < 0 {
			return sourceAwarePage{}, errors.New("source-aware text candidate has invalid PDF text geometry")
		}
		fontIndex, known := byName[run.Font]
		if !known {
			font, ok := fonts[run.Font]
			if !ok {
				return sourceAwarePage{}, fmt.Errorf("source-aware text candidate cannot map PDF font %q", run.Font)
			}
			data, failure := sourceAwareFontProgram(font, run.Font)
			if failure != nil {
				return sourceAwarePage{}, failure
			}
			fontIndex = len(result.fonts)
			byName[run.Font] = fontIndex
			result.fonts = append(result.fonts, sourceAwareFont{
				family:     fmt.Sprintf("inkbite-p%04d-f%02d", pageNumber, fontIndex+1),
				program:    data,
				characters: make(map[rune]struct{}),
			})
		}
		font := &result.fonts[fontIndex]
		for _, character := range run.S {
			font.characters[character] = struct{}{}
		}
		result.runs = append(result.runs, sourceAwareRun{
			family: font.family, text: run.S, fontSize: math.Abs(run.FontSize), x: run.X, y: run.Y, width: run.W,
		})
	}
	return result, nil
}

func sourceAwareFontProgram(font pdf.Font, name string) ([]byte, error) {
	if font.V.Key("Subtype").Name() != "TrueType" {
		return nil, fmt.Errorf("source-aware text candidate font %q is not embedded TrueType", name)
	}
	if font.V.Key("ToUnicode").Kind() != pdf.Stream {
		return nil, fmt.Errorf("source-aware text candidate font %q lacks a ToUnicode glyph map", name)
	}
	program := font.V.Key("FontDescriptor").Key("FontFile2")
	if program.Kind() != pdf.Stream {
		return nil, fmt.Errorf("source-aware text candidate font %q lacks an embedded TrueType program", name)
	}
	data, err := readFontProgram(program)
	if err != nil {
		return nil, fmt.Errorf("source-aware text candidate font %q program is unreadable: %w", name, err)
	}
	if failure := trueTypeEmbeddingPolicyFailure(data); failure != "" {
		return nil, fmt.Errorf("source-aware text candidate font %q %s", name, failure)
	}
	return data, nil
}

// emitSourceAwareCandidate derives a second candidate from the already-safe
// Poppler SVG. Poppler's identifiable glyph definitions and uses are removed
// before source text is added, leaving non-text vector artwork intact without
// making the source-aware candidate structurally larger than its baseline.
func emitSourceAwareCandidate(
	ctx context.Context,
	output, pageDirectory string,
	outlined Candidate,
	document []byte,
	subsetter *WOFF2Subsetter,
	page int,
	dimensions PageDimensions,
	profiles []VisualProfile,
	references []Verification,
) Candidate {
	cleanup := func() {
		_ = os.Remove(filepath.Join(pageDirectory, "source-aware-text.svg"))
		_ = os.RemoveAll(filepath.Join(pageDirectory, "source-aware-assets"))
	}
	fail := func(reason string) Candidate {
		cleanup()
		return unavailableSourceAwareCandidate(reason)
	}
	if subsetter == nil {
		return fail("source-aware text candidate requires a pinned WOFF2 subsetter; outlined glyph candidate remains required")
	}
	if outlined.SVG == nil {
		return fail("source-aware text candidate cannot derive from the Poppler SVG")
	}
	source, err := readSourceAwarePage(document, page)
	if err != nil {
		return fail(err.Error())
	}
	fontAssets, err := emitWOFF2Subsets(ctx, output, pageDirectory, source, subsetter)
	if err != nil {
		return fail(fmt.Sprintf("source-aware text candidate cannot emit WOFF2 subsets: %v", err))
	}
	svgPath := filepath.Join(pageDirectory, "source-aware-text.svg")
	outlinedPath := filepath.Join(output, filepath.FromSlash(outlined.SVG.Locator))
	if err := emitSourceAwareSVG(outlinedPath, svgPath, source, dimensions, fontAssets); err != nil {
		return fail(fmt.Sprintf("source-aware text candidate cannot emit positioned text SVG: %v", err))
	}
	assets := append([]Artifact{}, outlined.ReferencedAssets...)
	assets = append(assets, fontAssets...)
	if err := validateSVGWithAssets(svgPath, output, assets); err != nil {
		return fail(fmt.Sprintf("source-aware text candidate emitted unsafe SVG: %v", err))
	}
	artifact, err := artifactFor(output, svgPath, "image/svg+xml")
	if err != nil {
		return fail(fmt.Sprintf("source-aware text candidate cannot hash SVG: %v", err))
	}
	installedBytes := artifact.ByteCount
	seen := map[string]struct{}{}
	for _, asset := range assets {
		if _, duplicate := seen[asset.Locator]; duplicate {
			continue
		}
		seen[asset.Locator] = struct{}{}
		installedBytes += asset.ByteCount
	}
	return verifySVGCandidate(ctx, output, pageDirectory, profiles, references, Candidate{
		Kind: "source_aware_text", State: CandidateVerified, SVG: &artifact, ReferencedAssets: assets, InstalledByteCount: installedBytes,
		Verification: make([]Verification, 0, len(profiles)),
	})
}

func emitWOFF2Subsets(ctx context.Context, output, pageDirectory string, source sourceAwarePage, subsetter *WOFF2Subsetter) ([]Artifact, error) {
	inputDirectory, err := os.MkdirTemp(pageDirectory, ".source-aware-input-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(inputDirectory)
	assetDirectory := filepath.Join(pageDirectory, "source-aware-assets")
	if err := os.MkdirAll(assetDirectory, 0o755); err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(assetDirectory)
		}
	}()
	assets := make([]Artifact, 0, len(source.fonts))
	for index, font := range source.fonts {
		inputPath := filepath.Join(inputDirectory, fmt.Sprintf("f%02d.ttf", index+1))
		if err := os.WriteFile(inputPath, font.program, 0o600); err != nil {
			return nil, err
		}
		textPath := filepath.Join(inputDirectory, fmt.Sprintf("f%02d.txt", index+1))
		if err := os.WriteFile(textPath, sourceAwareCharacters(font.characters), 0o600); err != nil {
			return nil, err
		}
		outputPath := filepath.Join(assetDirectory, fmt.Sprintf("f%02d.woff2", index+1))
		if _, err := run(ctx, subsetter.Path, inputPath, "--text-file="+textPath, "--no-ignore-missing-unicodes", "--flavor=woff2", "--output-file="+outputPath); err != nil {
			return nil, err
		}
		if err := validateWOFF2(outputPath); err != nil {
			return nil, err
		}
		artifact, err := artifactFor(output, outputPath, "font/woff2")
		if err != nil {
			return nil, err
		}
		assets = append(assets, artifact)
	}
	cleanup = false
	return assets, nil
}

func sourceAwareCharacters(characters map[rune]struct{}) []byte {
	runes := make([]rune, 0, len(characters))
	for character := range characters {
		runes = append(runes, character)
	}
	sort.Slice(runes, func(left, right int) bool { return runes[left] < runes[right] })
	return []byte(string(runes))
}

func validateWOFF2(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("read WOFF2 output: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("WOFF2 output must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < 48 || string(data[:4]) != "wOF2" {
		return errors.New("WOFF2 subsetter did not emit a WOFF2 font")
	}
	if int(binary.BigEndian.Uint32(data[8:12])) != len(data) || binary.BigEndian.Uint16(data[12:14]) == 0 {
		return errors.New("WOFF2 subsetter emitted an invalid WOFF2 header")
	}
	return nil
}

func emitSourceAwareSVG(outlinedPath, outputPath string, source sourceAwarePage, dimensions PageDimensions, fontAssets []Artifact) error {
	data, err := os.ReadFile(outlinedPath)
	if err != nil {
		return err
	}
	data, err = stripPopplerGlyphOutlines(data)
	if err != nil {
		return err
	}
	rootStart := bytes.Index(data, []byte("<svg"))
	if rootStart < 0 {
		return errors.New("outlined SVG has no root element")
	}
	rootEndOffset := bytes.IndexByte(data[rootStart:], '>')
	if rootEndOffset < 0 {
		return errors.New("outlined SVG root element is incomplete")
	}
	rootEnd := rootStart + rootEndOffset
	contentEnd := bytes.LastIndex(data, []byte("</svg>"))
	if contentEnd < rootEnd {
		return errors.New("outlined SVG has no closing root element")
	}
	fontReferences, err := sourceAwareFontReferences(source, fontAssets)
	if err != nil {
		return err
	}
	text, err := sourceAwareTextElements(source, dimensions)
	if err != nil {
		return err
	}
	var document bytes.Buffer
	if defsEnd := bytes.LastIndex(data[:contentEnd], []byte("</defs>")); defsEnd >= 0 {
		document.Write(data[:defsEnd])
		document.WriteString("<style>")
		document.WriteString(fontReferences)
		document.WriteString("</style>")
		document.Write(data[defsEnd:contentEnd])
	} else {
		document.Write(data[:rootEnd+1])
		document.WriteString("<defs><style>")
		document.WriteString(fontReferences)
		document.WriteString("</style></defs>")
		document.Write(data[rootEnd+1 : contentEnd])
	}
	document.WriteString("<g id=\"inkbite-source-text\">")
	document.WriteString(text)
	document.WriteString("</g>")
	document.Write(data[contentEnd:])
	return os.WriteFile(outputPath, document.Bytes(), 0o644)
}

func stripPopplerGlyphOutlines(document []byte) ([]byte, error) {
	var result bytes.Buffer
	copyStart, cursor := 0, 0
	definitions, uses := 0, 0
	for cursor < len(document) {
		tagStart := bytes.IndexByte(document[cursor:], '<')
		if tagStart < 0 {
			break
		}
		tagStart += cursor
		tagEnd, err := svgTagEnd(document, tagStart)
		if err != nil {
			return nil, err
		}
		tag := document[tagStart:tagEnd]
		name, closing, selfClosing := svgTagKind(tag)
		switch {
		case !closing && name == "g" && popplerGlyphDefinition.Match(tag):
			elementEnd, err := svgElementEnd(document, tagStart, "g", selfClosing)
			if err != nil {
				return nil, err
			}
			result.Write(document[copyStart:tagStart])
			copyStart, cursor = elementEnd, elementEnd
			definitions++
		case !closing && name == "use" && popplerGlyphReference.Match(tag):
			elementEnd, err := svgElementEnd(document, tagStart, "use", selfClosing)
			if err != nil {
				return nil, err
			}
			result.Write(document[copyStart:tagStart])
			copyStart, cursor = elementEnd, elementEnd
			uses++
		default:
			cursor = tagEnd
		}
	}
	if definitions == 0 || uses == 0 {
		return nil, errors.New("outlined SVG has no removable Poppler glyph outlines")
	}
	result.Write(document[copyStart:])
	return result.Bytes(), nil
}

func svgElementEnd(document []byte, start int, expectedName string, selfClosing bool) (int, error) {
	startEnd, err := svgTagEnd(document, start)
	if err != nil {
		return 0, err
	}
	if selfClosing {
		return startEnd, nil
	}
	depth, cursor := 1, startEnd
	for cursor < len(document) {
		tagStart := bytes.IndexByte(document[cursor:], '<')
		if tagStart < 0 {
			break
		}
		tagStart += cursor
		tagEnd, err := svgTagEnd(document, tagStart)
		if err != nil {
			return 0, err
		}
		name, closing, nestedSelfClosing := svgTagKind(document[tagStart:tagEnd])
		if name == expectedName {
			if closing {
				depth--
				if depth == 0 {
					return tagEnd, nil
				}
			} else if !nestedSelfClosing {
				depth++
			}
		}
		cursor = tagEnd
	}
	return 0, fmt.Errorf("outlined SVG %s element is incomplete", expectedName)
}

func svgTagEnd(document []byte, start int) (int, error) {
	quote := byte(0)
	for cursor := start + 1; cursor < len(document); cursor++ {
		character := document[cursor]
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == '>' {
			return cursor + 1, nil
		}
	}
	return 0, errors.New("outlined SVG has an incomplete element")
}

func svgTagKind(tag []byte) (name string, closing, selfClosing bool) {
	trimmed := bytes.TrimSpace(tag)
	if len(trimmed) < 3 || trimmed[0] != '<' || trimmed[1] == '!' || trimmed[1] == '?' {
		return "", false, false
	}
	cursor := 1
	if trimmed[cursor] == '/' {
		closing = true
		cursor++
	}
	start := cursor
	for cursor < len(trimmed) && ((trimmed[cursor] >= 'a' && trimmed[cursor] <= 'z') || (trimmed[cursor] >= 'A' && trimmed[cursor] <= 'Z') || trimmed[cursor] == ':' || trimmed[cursor] == '-') {
		cursor++
	}
	name = strings.ToLower(string(trimmed[start:cursor]))
	for cursor = len(trimmed) - 2; cursor > 0 && (trimmed[cursor] == ' ' || trimmed[cursor] == '\t' || trimmed[cursor] == '\n' || trimmed[cursor] == '\r'); cursor-- {
	}
	selfClosing = !closing && cursor > 0 && trimmed[cursor] == '/'
	return name, closing, selfClosing
}

func sourceAwareFontReferences(source sourceAwarePage, assets []Artifact) (string, error) {
	if len(source.fonts) != len(assets) {
		return "", errors.New("source-aware SVG font asset count does not match source fonts")
	}
	var style strings.Builder
	for index, asset := range assets {
		if asset.MediaType != "font/woff2" || filepath.Base(asset.Locator) != fmt.Sprintf("f%02d.woff2", index+1) {
			return "", errors.New("source-aware SVG font asset is invalid")
		}
		style.WriteString("@font-face{font-family:'")
		style.WriteString(source.fonts[index].family)
		style.WriteString("';src:url('")
		style.WriteString("source-aware-assets/")
		style.WriteString(filepath.Base(asset.Locator))
		style.WriteString("') format('woff2');}")
	}
	if style.Len() == 0 {
		return "", errors.New("source-aware SVG has no WOFF2 assets")
	}
	return style.String(), nil
}

func sourceAwareTextElements(source sourceAwarePage, dimensions PageDimensions) (string, error) {
	var text strings.Builder
	for _, run := range source.runs {
		if run.y > dimensions.HeightPoints || run.y < 0 {
			return "", errors.New("source-aware text is outside page bounds")
		}
		text.WriteString("<text font-family=\"")
		text.WriteString(run.family)
		text.WriteString("\" font-size=\"")
		text.WriteString(svgNumber(run.fontSize))
		text.WriteString("\" x=\"")
		text.WriteString(svgNumber(run.x))
		text.WriteString("\" y=\"")
		text.WriteString(svgNumber(dimensions.HeightPoints - run.y))
		text.WriteString("\"")
		if run.width > 0 {
			text.WriteString(" textLength=\"")
			text.WriteString(svgNumber(run.width))
			text.WriteString("\" lengthAdjust=\"spacingAndGlyphs\"")
		}
		text.WriteString(">")
		if err := xml.EscapeText(&text, []byte(run.text)); err != nil {
			return "", err
		}
		text.WriteString("</text>")
	}
	return text.String(), nil
}

func svgNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func fontSubsetPrefix(name string) string {
	if index := strings.IndexByte(name, '+'); index >= 0 {
		return name[:index+1]
	}
	return ""
}

func readFontProgram(program pdf.Value) ([]byte, error) {
	reader := program.Reader()
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("font program is empty")
	}
	return data, nil
}

// trueTypeEmbeddingPolicyFailure reads the OpenType OS/2 fsType flags. A
// restricted, bitmap-only, or no-subsetting font cannot produce an embedded
// WOFF2 subset and therefore is not eligible.
func trueTypeEmbeddingPolicyFailure(program []byte) string {
	if len(program) < 12 {
		return "embedded TrueType program is malformed"
	}
	tableCount := int(binary.BigEndian.Uint16(program[4:6]))
	if len(program) < 12+tableCount*16 {
		return "embedded TrueType table directory is malformed"
	}
	for index := 0; index < tableCount; index++ {
		record := 12 + index*16
		if string(program[record:record+4]) != "OS/2" {
			continue
		}
		offset := int(binary.BigEndian.Uint32(program[record+8 : record+12]))
		length := int(binary.BigEndian.Uint32(program[record+12 : record+16]))
		if offset < 0 || length < 10 || offset > len(program)-length {
			return "embedded TrueType OS/2 table is malformed"
		}
		flags := binary.BigEndian.Uint16(program[offset+8 : offset+10])
		switch {
		case flags&0x0002 != 0:
			return "forbids embedding"
		case flags&0x0100 != 0:
			return "forbids subsetting"
		case flags&0x0200 != 0:
			return "permits bitmap embedding only"
		default:
			return ""
		}
	}
	return "embedded TrueType program lacks an OS/2 embedding policy table"
}
