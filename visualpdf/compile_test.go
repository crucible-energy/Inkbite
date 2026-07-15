package visualpdf

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompileEmitsVerifiedPackageWithSourceText(t *testing.T) {
	root := t.TempDir()
	fixturePNG := filepath.Join(root, "fixture.png")
	writeFixturePNG(t, fixturePNG)
	t.Setenv("FAKE_PNG", fixturePNG)
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTool(t, tools, "pdfinfo", `
if [ "$1" = "-v" ]; then echo "pdfinfo version 1.2.3"; exit 0; fi
case " $* " in *" -f "*) echo "Page size: 72 x 72 pts";; *) echo "Pages: 1";; esac
`)
	writeTool(t, tools, "pdftotext", `
if [ "$1" = "-v" ]; then echo "pdftotext version 1.2.3"; exit 0; fi
case " $* " in *" -bbox "*) echo '<?xml version="1.0"?><doc><word xMin="1" yMin="2" xMax="3" yMax="4">Source text</word></doc>';; *) echo 'Source text';; esac
`)
	writeTool(t, tools, "pdftocairo", `
if [ "$1" = "-v" ]; then echo "pdftocairo version 1.2.3"; exit 0; fi
last=""
for value in "$@"; do last="$value"; done
case " $* " in *" -svg "*) printf '<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>' > "$last";; *) cp "$FAKE_PNG" "${last}.png";; esac
`)
	renderer := filepath.Join(root, "renderer")
	if err := os.WriteFile(renderer, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'renderer 9'; exit 0; fi\ncp \"$FAKE_PNG\" \"$4\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "source.pdf")
	writeValidPDF(t, input)
	output := filepath.Join(root, "package")
	manifest, err := Compile(context.Background(), CompileOptions{
		InputPath:       input,
		OutputDirectory: output,
		Toolchain:       Toolchain{Directory: tools, Version: "1.2.3"},
		Profiles: []VisualProfile{{
			ID: "fixture-webview", Version: "1", ReferenceDPI: 72,
			Renderer:    SVGRenderer{Path: renderer, Version: "renderer 9", Arguments: []string{"--input", "{input}", "--output", "{output}"}},
			Calibration: fixtureCalibration(t, root),
		}},
		CompilerVersion: "test",
		Now:             func() time.Time { return time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if manifest.Source.Locator != "source.pdf" || manifest.Source.SHA256 == "" {
		t.Fatalf("unexpected source manifest: %#v", manifest.Source)
	}
	if len(manifest.Pages) != 1 || manifest.Pages[0].State != PageVerifiedSVG {
		t.Fatalf("expected one verified SVG page, got %#v", manifest.Pages)
	}
	if manifest.Pages[0].PrimaryDisplay == nil || manifest.Pages[0].PrimaryDisplay.MediaType != "image/svg+xml" {
		t.Fatalf("expected SVG primary display, got %#v", manifest.Pages[0].PrimaryDisplay)
	}
	if manifest.Pages[0].Candidates[1].State != CandidateUnavailable {
		t.Fatalf("expected font-policy source-aware candidate to remain unavailable, got %#v", manifest.Pages[0].Candidates[1])
	}
	if got := manifest.Pages[0].Candidates[0].Verification[0].Calibration.ReportSHA256; got == "" {
		t.Fatalf("expected verification to retain its calibration evidence, got %#v", manifest.Pages[0].Candidates[0].Verification[0])
	}
	semantic, err := os.ReadFile(filepath.Join(output, "pages", "0001", "semantic.md"))
	if err != nil || string(semantic) != "Source text\n" {
		t.Fatalf("expected source semantic text, got %q, %v", semantic, err)
	}
	if len(manifest.RemediationQueue) != 0 {
		t.Fatalf("expected no remediation items, got %#v", manifest.RemediationQueue)
	}
}

func TestCompileUsesVerifiedReferenceAsRasterFallback(t *testing.T) {
	root := t.TempDir()
	fixturePNG := filepath.Join(root, "fixture.png")
	writeFixturePNG(t, fixturePNG)
	t.Setenv("FAKE_PNG", fixturePNG)
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTool(t, tools, "pdfinfo", `if [ "$1" = "-v" ]; then echo "pdfinfo version 1.2.3"; elif [ "$1" = "-f" ]; then echo "Page size: 72 x 72 pts"; else echo "Pages: 1"; fi`)
	writeTool(t, tools, "pdftotext", `if [ "$1" = "-v" ]; then echo "pdftotext version 1.2.3"; elif [ "$4" = "-bbox" ]; then echo '<doc/>'; else echo 'Source'; fi`)
	writeTool(t, tools, "pdftocairo", `if [ "$1" = "-v" ]; then echo "pdftocairo version 1.2.3"; exit 0; fi; for value in "$@"; do last="$value"; done; case " $* " in *" -svg "*) printf '<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>' > "$last";; *) cp "$FAKE_PNG" "${last}.png";; esac`)
	renderer := filepath.Join(root, "renderer")
	if err := os.WriteFile(renderer, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'renderer 9'; exit 0; fi\nprintf 'not-a-png' > \"$4\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "source.pdf")
	writeValidPDF(t, input)
	manifest, err := Compile(context.Background(), CompileOptions{
		InputPath: input, OutputDirectory: filepath.Join(root, "package"), Toolchain: Toolchain{Directory: tools, Version: "1.2.3"},
		Profiles: []VisualProfile{{ID: "fixture", Version: "1", ReferenceDPI: 72, Renderer: SVGRenderer{Path: renderer, Version: "renderer 9", Arguments: []string{"--input", "{input}", "--output", "{output}"}}, Calibration: fixtureCalibration(t, root)}},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	page := manifest.Pages[0]
	if page.State != PageRasterFallback || page.RasterFallback == nil || len(manifest.RemediationQueue) != 1 {
		t.Fatalf("expected verified fallback and remediation, got %#v %#v", page, manifest.RemediationQueue)
	}
}

func TestProfileAndSVGValidationFailClosed(t *testing.T) {
	if err := validateProfiles([]VisualProfile{{ID: "missing", Version: "1", ReferenceDPI: 72, Renderer: SVGRenderer{Path: "/renderer", Version: "1", Arguments: []string{"{input}", "{output}"}}}}); err == nil {
		t.Fatal("expected missing calibration to fail")
	}
	svg := filepath.Join(t.TempDir(), "unsafe.svg")
	if err := os.WriteFile(svg, []byte(`<svg><script>bad()</script></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSVG(svg); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsafe SVG rejection, got %v", err)
	}
}

func TestValidateSVGRejectsUnlistedCSSResource(t *testing.T) {
	svg := filepath.Join(t.TempDir(), "unsafe-style.svg")
	if err := os.WriteFile(svg, []byte(`<svg><style>rect { fill: url(https://example.invalid/fill.svg); }</style></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSVG(svg); err == nil || !strings.Contains(err.Error(), "CSS") {
		t.Fatalf("expected CSS resource rejection, got %v", err)
	}
}

func TestValidateSVGRejectsResponsiveImageMetadata(t *testing.T) {
	svg := filepath.Join(t.TempDir(), "responsive.svg")
	if err := os.WriteFile(svg, []byte(`<svg><image srcset="one.png 1x" sizes="100vw"/></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateSVG(svg); err == nil || !strings.Contains(err.Error(), "responsive-image") {
		t.Fatalf("expected responsive image metadata rejection, got %v", err)
	}
}

func TestLoadProfileSetPinsCalibrationEvidence(t *testing.T) {
	root := t.TempDir()
	calibrationPath := filepath.Join(root, "calibration.md")
	if err := os.WriteFile(calibrationPath, []byte("reviewed calibration evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := sha256File(calibrationPath)
	if err != nil {
		t.Fatal(err)
	}
	profileSet := ProfileSet{
		SchemaVersion: ProfileSetSchemaVersion,
		Profiles: []VisualProfile{{
			ID: "fixture", Version: "1", ReferenceDPI: 72,
			Renderer:    SVGRenderer{Path: "/qualified/renderer", Version: "fixture", Arguments: []string{"{input}", "{output}"}},
			Calibration: Calibration{CorpusID: "fixture", Report: "calibration.md", ReportSHA256: hash},
		}},
	}
	data, err := json.Marshal(profileSet)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(root, "profiles.json")
	if err := os.WriteFile(profilePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadProfileSet(profilePath)
	if err != nil {
		t.Fatalf("LoadProfileSet() error = %v", err)
	}
	resolvedCalibrationPath, err := filepath.EvalSymlinks(calibrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Profiles[0].Calibration.reportPath != resolvedCalibrationPath {
		t.Fatalf("calibration report path = %q, want %q", loaded.Profiles[0].Calibration.reportPath, resolvedCalibrationPath)
	}
	if err := os.WriteFile(calibrationPath, []byte("tampered evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfileSet(profilePath); err == nil || !strings.Contains(err.Error(), "calibration report hash") {
		t.Fatalf("expected tampered calibration rejection, got %v", err)
	}
	outsidePath := filepath.Join(filepath.Dir(root), "outside.md")
	if err := os.WriteFile(outsidePath, []byte("outside profile set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outsidePath) })
	profileSet.Profiles[0].Calibration.Report = "../outside.md"
	data, err = json.Marshal(profileSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfileSet(profilePath); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected escaped calibration report rejection, got %v", err)
	}
}

func TestCheckedInProfilePinsItsCalibrationReport(t *testing.T) {
	if _, err := LoadProfileSet(filepath.Join("profiles", "iris-offline-webview-v2.json")); err != nil {
		t.Fatalf("checked-in profile must load: %v", err)
	}
}

func TestEmitSourceRasterAssetsWritesLosslessSidecars(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "source.pdf")
	if err := os.WriteFile(input, grayRasterPDF(2, 1, []byte{0x00, 0xFF}), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "package")
	pageDirectory := filepath.Join(output, "pages", "0001")
	if err := os.MkdirAll(pageDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	document, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := emitSourceRasterAssets(document, output, 1, pageDirectory)
	if err != nil {
		t.Fatalf("emitSourceRasterAssets() error = %v", err)
	}
	if len(assets) != 1 || assets[0].Encoding != "lossless_png" || assets[0].Artifact.MediaType != "image/png" {
		t.Fatalf("unexpected source raster assets: %#v", assets)
	}
	file, err := os.Open(filepath.Join(output, filepath.FromSlash(assets[0].Artifact.Locator)))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoded, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if left, _, _, _ := decoded.At(0, 0).RGBA(); left != 0 {
		t.Fatalf("first pixel = %d, want 0", left)
	}
	if right, _, _, _ := decoded.At(1, 0).RGBA(); right != 0xFFFF {
		t.Fatalf("second pixel = %d, want 65535", right)
	}
}

func fixtureCalibration(t *testing.T, root string) Calibration {
	t.Helper()
	path := filepath.Join(root, "calibration.md")
	if err := os.WriteFile(path, []byte("fixture calibration evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}
	return Calibration{CorpusID: "fixture-corpus", Report: path, ReportSHA256: hash}
}

func writeValidPDF(t *testing.T, destination string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "converters", "pdf", "testdata", "simple.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func grayRasterPDF(width, height int, pixels []byte) []byte {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(pixels); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	content := []byte("q\n2 0 0 1 0 0 cm\n/Im1 Do\nQ")
	image := append([]byte(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", width, height, compressed.Len())), compressed.Bytes()...)
	image = append(image, []byte("\nendstream")...)
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R >> >> >>"),
		[]byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)),
		image,
	}
	var document bytes.Buffer
	document.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = document.Len()
		fmt.Fprintf(&document, "%d 0 obj\n", index+1)
		document.Write(object)
		document.WriteString("\nendobj\n")
	}
	xrefOffset := document.Len()
	fmt.Fprintf(&document, "xref\n0 %d\n", len(objects)+1)
	document.WriteString("0000000000 65535 f \n")
	for index := 1; index <= len(objects); index++ {
		fmt.Fprintf(&document, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&document, "trailer\n<< /Root 1 0 R /Size %d >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return document.Bytes()
}

func writeFixturePNG(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	pixels := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixels.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	if err := png.Encode(file, pixels); err != nil {
		t.Fatal(err)
	}
}

func writeTool(t *testing.T, directory, name, body string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
