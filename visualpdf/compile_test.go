package visualpdf

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
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
	fixtureData, err := os.ReadFile(fixturePNG)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PNG_BASE64", base64.StdEncoding.EncodeToString(fixtureData))
	if _, _, err := decodeSVGImageDataURI("data:image/png;base64," + base64.StdEncoding.EncodeToString(fixtureData)); err != nil {
		t.Fatalf("fixture data URI must be valid: %v", err)
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
case " $* " in *" -svg "*) printf '<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/><image href="data:image/png;base64,%s"/></svg>' "$FAKE_PNG_BASE64" > "$last";; *) cp "$FAKE_PNG" "${last}.png";; esac
`)
	renderer := filepath.Join(root, "renderer")
	if err := os.WriteFile(renderer, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'renderer 9'; exit 0; fi\ncp \"$FAKE_PNG\" \"$4\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(root, "source.pdf")
	writeValidPDF(t, input)
	output := filepath.Join(root, "package")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	subsetter := filepath.Join(root, "woff2-subsetter")
	writeTool(t, root, "woff2-subsetter", `if [ "$1" = "--version" ]; then echo "woff2-subsetter 1.0"; exit 0; fi; exit 1`)
	manifest, err := Compile(context.Background(), CompileOptions{
		InputPath:       input,
		OutputDirectory: output,
		Toolchain:       Toolchain{Directory: tools, Version: "1.2.3"},
		WOFF2Subsetter:  &WOFF2Subsetter{Path: subsetter, Version: "1.0"},
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
	if manifest.SchemaVersion != "inkbite.visualpdf.manifest.v3" {
		t.Fatalf("unexpected visual manifest schema: %q", manifest.SchemaVersion)
	}
	if manifest.WOFF2Subsetter == nil || manifest.WOFF2Subsetter.Version != "1.0" || manifest.WOFF2Subsetter.Path != subsetter {
		t.Fatalf("expected pinned WOFF2 subsetter in manifest, got %#v", manifest.WOFF2Subsetter)
	}
	if len(manifest.Pages) != 1 || manifest.Pages[0].State != PageVerifiedSVG {
		t.Fatalf("expected one verified SVG page, got %#v", manifest.Pages)
	}
	if manifest.Pages[0].PrimaryDisplay == nil || manifest.Pages[0].PrimaryDisplay.MediaType != "image/svg+xml" {
		t.Fatalf("expected SVG primary display, got %#v", manifest.Pages[0].PrimaryDisplay)
	}
	outlined := manifest.Pages[0].Candidates[0]
	if len(outlined.ReferencedAssets) != 1 || outlined.InstalledByteCount != outlined.SVG.ByteCount+outlined.ReferencedAssets[0].ByteCount {
		t.Fatalf("expected one installed SVG image asset, got %#v", outlined)
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
	outputInfo, err := os.Stat(output)
	if err != nil {
		t.Fatalf("compiled package output should exist: %v", err)
	}
	if got := outputInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("expected output permissions to remain %04o, got %04o", 0o700, got)
	}
}

func TestCompileEmitsVerifiedSourceAwareTextCandidate(t *testing.T) {
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
case " $* " in *" -f "*) echo "Page size: 300 x 144 pts";; *) echo "Pages: 1";; esac
`)
	writeTool(t, tools, "pdftotext", `
if [ "$1" = "-v" ]; then echo "pdftotext version 1.2.3"; exit 0; fi
case " $* " in *" -bbox "*) echo '<?xml version="1.0"?><doc><word xMin="100" yMin="32" xMax="108" yMax="44">A</word></doc>';; *) echo 'A';; esac
`)
	writeTool(t, tools, "pdftocairo", `
if [ "$1" = "-v" ]; then echo "pdftocairo version 1.2.3"; exit 0; fi
last=""
for value in "$@"; do last="$value"; done
case " $* " in *" -svg "*) printf '<svg xmlns="http://www.w3.org/2000/svg" width="300" height="144"><defs></defs><g fill="black"><path d="M0 0"/></g></svg>' > "$last";; *) cp "$FAKE_PNG" "${last}.png";; esac
`)
	renderer := filepath.Join(root, "renderer")
	writeTool(t, root, "renderer", `if [ "$1" = "--version" ]; then echo "renderer 9"; exit 0; fi; cp "$FAKE_PNG" "$4"`)
	woff2 := filepath.Join(root, "fixture.woff2")
	if err := os.WriteFile(woff2, testWOFF2Program(), 0o644); err != nil {
		t.Fatal(err)
	}
	arguments := filepath.Join(root, "subsetter-arguments")
	t.Setenv("FAKE_WOFF2", woff2)
	t.Setenv("FAKE_SUBSETTER_ARGUMENTS", arguments)
	subsetter := filepath.Join(root, "woff2-subsetter")
	writeTool(t, root, "woff2-subsetter", `
if [ "$1" = "--version" ]; then echo "woff2-subsetter 1.0"; exit 0; fi
for value in "$@"; do case "$value" in --output-file=*) output="${value#--output-file=}";; esac; done
[ -n "$output" ] || exit 1
printf '%s\n' "$*" > "$FAKE_SUBSETTER_ARGUMENTS"
cp "$FAKE_WOFF2" "$output"
`)
	input := filepath.Join(root, "source.pdf")
	if err := os.WriteFile(input, embeddedTrueTypePDF(), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "package")
	manifest, err := Compile(context.Background(), CompileOptions{
		InputPath: input, OutputDirectory: output, Toolchain: Toolchain{Directory: tools, Version: "1.2.3"},
		WOFF2Subsetter: &WOFF2Subsetter{Path: subsetter, Version: "1.0"},
		Profiles: []VisualProfile{{
			ID: "fixture", Version: "1", ReferenceDPI: 72,
			Renderer:    SVGRenderer{Path: renderer, Version: "renderer 9", Arguments: []string{"--input", "{input}", "--output", "{output}"}},
			Calibration: fixtureCalibration(t, root),
		}},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	candidate := manifest.Pages[0].Candidates[1]
	if candidate.Kind != "source_aware_text" || candidate.State != CandidateVerified || candidate.SVG == nil {
		t.Fatalf("expected verified source-aware candidate, got %#v", candidate)
	}
	if len(candidate.ReferencedAssets) != 1 || candidate.ReferencedAssets[0].MediaType != "font/woff2" {
		t.Fatalf("expected one WOFF2 candidate asset, got %#v", candidate.ReferencedAssets)
	}
	svg, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(candidate.SVG.Locator)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(svg, []byte("@font-face")) || !bytes.Contains(svg, []byte("<text")) || !bytes.Contains(svg, []byte(">A</text>")) {
		t.Fatalf("source-aware SVG does not contain positioned source text: %s", svg)
	}
	gotArguments, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gotArguments), "--no-ignore-missing-unicodes") || !strings.Contains(string(gotArguments), "--flavor=woff2") {
		t.Fatalf("subsetter invocation did not require a complete WOFF2 subset: %q", gotArguments)
	}
}

func TestCompileDefaultsModeWhenOutputDoesNotExist(t *testing.T) {
	root := t.TempDir()
	fixturePNG := filepath.Join(root, "fixture.png")
	writeFixturePNG(t, fixturePNG)
	t.Setenv("FAKE_PNG", fixturePNG)
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	fixtureData, err := os.ReadFile(fixturePNG)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PNG_BASE64", base64.StdEncoding.EncodeToString(fixtureData))
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
	if _, err := Compile(context.Background(), CompileOptions{
		InputPath:       input,
		OutputDirectory: output,
		Toolchain:       Toolchain{Directory: tools, Version: "1.2.3"},
		Profiles: []VisualProfile{{
			ID: "fixture", Version: "1", ReferenceDPI: 72,
			Renderer:    SVGRenderer{Path: renderer, Version: "renderer 9", Arguments: []string{"--input", "{input}", "--output", "{output}"}},
			Calibration: fixtureCalibration(t, root),
		}},
		CompilerVersion: "test",
	}); err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatalf("compiled package output should exist: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("expected output permissions %04o for newly created output, got %04o", 0o755, got)
	}
}

func TestPublishOutputDirectoryRestoresExistingOutputOnRenameFailure(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "package")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	err := publishOutputDirectoryWithRename(staging, output, func(string, string) error {
		return fmt.Errorf("injected rename failure")
	})
	if err == nil || !strings.Contains(err.Error(), "publish visual PDF output") {
		t.Fatalf("expected publish failure, got %v", err)
	}
	info, err := os.Stat(output)
	if err != nil || !info.IsDir() {
		t.Fatalf("existing output directory was not restored: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("expected restored output permissions %04o, got %04o", 0o700, got)
	}
	if _, err := os.Stat(staging); err != nil {
		t.Fatalf("staging directory was removed after failed publish: %v", err)
	}
}

func TestCompileFailureLeavesExistingOutputUntouched(t *testing.T) {
	root := t.TempDir()
	fixturePNG := filepath.Join(root, "fixture.png")
	writeFixturePNG(t, fixturePNG)
	t.Setenv("FAKE_PNG", fixturePNG)
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTool(t, tools, "pdfinfo", `if [ "$1" = "-v" ]; then echo "pdfinfo version 1.2.3"; elif [ "$1" = "-f" ]; then echo "Page size: 72 x 72 pts"; else echo "Pages: 1"; fi`)
	writeTool(t, tools, "pdftotext", `if [ "$1" = "-v" ]; then echo "pdftotext version 1.2.3"; elif echo " $* " | grep -q " -bbox "; then echo '<doc/>'; else echo 'Source'; fi`)
	writeTool(t, tools, "pdftocairo", `if [ "$1" = "-v" ]; then echo "pdftocairo version 1.2.3"; exit 0; fi; case " $* " in *" -svg "*) exit 1;; esac; for value in "$@"; do last="$value"; done; cp "$FAKE_PNG" "${last}.png"`)
	input := filepath.Join(root, "source.pdf")
	writeValidPDF(t, input)
	output := filepath.Join(root, "package")
	if err := os.Mkdir(output, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Compile(context.Background(), CompileOptions{
		InputPath: input, OutputDirectory: output, Toolchain: Toolchain{Directory: tools, Version: "1.2.3"},
		Profiles: []VisualProfile{{ID: "fixture", Version: "1", ReferenceDPI: 72, Renderer: SVGRenderer{Path: filepath.Join(root, "renderer"), Version: "renderer 9", Arguments: []string{"--input", "{input}", "--output", "{output}"}}, Calibration: fixtureCalibration(t, root)}},
	})
	if err == nil || !strings.Contains(err.Error(), "emit Poppler/Cairo") {
		t.Fatalf("expected outlined SVG failure, got %v", err)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatalf("existing output directory was removed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("existing output directory contains partial package files: %#v", entries)
	}
	staging, err := filepath.Glob(filepath.Join(root, ".package.tmp-*"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("staging directories remain after failure: %v, %v", staging, err)
	}
}

func TestCompileRejectsOutputSymlink(t *testing.T) {
	root := t.TempDir()
	fixturePNG := filepath.Join(root, "fixture.png")
	writeFixturePNG(t, fixturePNG)
	t.Setenv("FAKE_PNG", fixturePNG)
	tools := filepath.Join(root, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTool(t, tools, "pdfinfo", `if [ "$1" = "-v" ]; then echo "pdfinfo version 1.2.3"; else echo "Pages: 1"; fi`)
	writeTool(t, tools, "pdftotext", `if [ "$1" = "-v" ]; then echo "pdftotext version 1.2.3"; fi`)
	writeTool(t, tools, "pdftocairo", `if [ "$1" = "-v" ]; then echo "pdftocairo version 1.2.3"; fi`)
	input := filepath.Join(root, "source.pdf")
	writeValidPDF(t, input)
	realOutput := filepath.Join(root, "package")
	if err := os.Mkdir(realOutput, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "package-link")
	if err := os.Symlink(realOutput, symlink); err != nil {
		t.Fatal(err)
	}
	_, err := Compile(context.Background(), CompileOptions{
		InputPath:       input,
		OutputDirectory: symlink,
		Toolchain:       Toolchain{Directory: tools, Version: "1.2.3"},
		Profiles: []VisualProfile{{
			ID: "fixture", Version: "1", ReferenceDPI: 72,
			Renderer:    SVGRenderer{Path: filepath.Join(root, "renderer"), Version: "renderer 9", Arguments: []string{"--input", "{input}", "--output", "{output}"}},
			Calibration: fixtureCalibration(t, root),
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlink output rejection, got %v", err)
	}
	entries, err := os.ReadDir(realOutput)
	if err != nil {
		t.Fatalf("existing output target read failed: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink target must remain untouched, got entries: %#v", entries)
	}
	if _, err := os.Stat(symlink); err != nil {
		t.Fatalf("symlink output path should still exist: %v", err)
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

func TestValidateSVGRejectsExecutableAndStylesheetBypasses(t *testing.T) {
	directory := t.TempDir()
	for name, document := range map[string]string{
		"event":     `<svg onbegin="run()"/>`,
		"import":    `<svg><style>@import "https://example.invalid/chart.css";</style></svg>`,
		"directive": `<!DOCTYPE svg><svg/>`,
		"malformed": `<svg><path></svg>`,
		"data-link": `<svg><a href="data:text/html;base64,PHNjcmlwdD4=">unsafe</a></svg>`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, name+".svg")
			if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := validateSVG(path); err == nil {
				t.Fatalf("validateSVG(%s) unexpectedly accepted %q", name, document)
			}
		})
	}
}

func TestExternalizeSVGImageAssetsPreservesBytesAndPlacement(t *testing.T) {
	output := t.TempDir()
	svgPath := filepath.Join(output, "pages", "0001", "outlined-glyph.svg")
	if err := os.MkdirAll(filepath.Dir(svgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(output, "fixture.png")
	writeFixturePNG(t, fixture)
	pngBytes, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	inline := base64.StdEncoding.EncodeToString(pngBytes)
	if err := os.WriteFile(svgPath, []byte(`<svg><image x="12" y="34" width="56" height="78" href="data:image/png;base64,`+inline+`"/></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	assets, err := externalizeSVGImageAssets(output, svgPath, filepath.Join(filepath.Dir(svgPath), "outlined-assets"))
	if err != nil {
		t.Fatalf("externalizeSVGImageAssets() error = %v", err)
	}
	if len(assets) != 1 || assets[0].MediaType != "image/png" {
		t.Fatalf("unexpected assets: %#v", assets)
	}
	rewritten, err := os.ReadFile(svgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rewritten), "data:image") || !strings.Contains(string(rewritten), `x="12" y="34" width="56" height="78" href="outlined-assets/a1.png"`) {
		t.Fatalf("unexpected rewritten SVG: %s", rewritten)
	}
	sidecar, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(assets[0].Locator)))
	if err != nil || !bytes.Equal(sidecar, pngBytes) {
		t.Fatalf("externalized asset does not preserve image bytes: %v", err)
	}
	if err := validateSVGWithAssets(svgPath, output, assets); err != nil {
		t.Fatalf("validateSVGWithAssets() error = %v", err)
	}
	if err := validateSVG(svgPath); err == nil || !strings.Contains(err.Error(), "external resource") {
		t.Fatalf("expected undeclared local SVG asset rejection, got %v", err)
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
	profileSet.Profiles[0].Calibration.Report = "calibration.md"
	data, err = json.Marshal(profileSet)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["unexpected"] = json.RawMessage(`true`)
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfileSet(profilePath); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown profile field rejection, got %v", err)
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
	if len(assets[0].Placements) != 1 || assets[0].Placements[0].Matrix != [6]float64{2, 0, 0, 1, 0, 0} {
		t.Fatalf("unexpected source raster placement: %#v", assets[0].Placements)
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

func TestTrueTypeEmbeddingPolicyFailureRejectsUnembeddableFonts(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags uint16
		want  string
	}{
		{name: "allowed", flags: 0, want: ""},
		{name: "restricted", flags: 0x0002, want: "forbids embedding"},
		{name: "no subset", flags: 0x0100, want: "forbids subsetting"},
		{name: "bitmap only", flags: 0x0200, want: "permits bitmap embedding only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := trueTypeEmbeddingPolicyFailure(testTrueTypeProgram(test.flags)); got != test.want {
				t.Fatalf("trueTypeEmbeddingPolicyFailure() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPinnedWOFF2SubsetterFailsClosed(t *testing.T) {
	root := t.TempDir()
	tool := filepath.Join(root, "woff2-subsetter")
	writeTool(t, root, "woff2-subsetter", `if [ "$1" = "--version" ]; then echo "woff2-subsetter 1.0"; exit 0; fi`)
	if _, err := resolveWOFF2Subsetter(context.Background(), &WOFF2Subsetter{Path: tool, Version: "2.0"}); err == nil {
		t.Fatal("expected WOFF2 subsetter version mismatch")
	}
	if _, err := resolveWOFF2Subsetter(context.Background(), &WOFF2Subsetter{Path: tool, Version: "1.0"}); err != nil {
		t.Fatalf("expected pinned WOFF2 subsetter: %v", err)
	}
}

func TestValidateWOFF2FailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "font.woff2")
	if err := os.WriteFile(path, testWOFF2Program(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateWOFF2(path); err != nil {
		t.Fatalf("validateWOFF2() rejected valid WOFF2 header: %v", err)
	}
	for _, data := range [][]byte{
		[]byte("not a font"),
		append([]byte("wOF2\x00\x00\x00\x00\x00\x00\x00\x30\x00\x00"), make([]byte, 36)...),
	} {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := validateWOFF2(path); err == nil {
			t.Fatalf("validateWOFF2() accepted malformed output %x", data)
		}
	}
}

func TestEmitWOFF2SubsetsRemovesRejectedOutput(t *testing.T) {
	root := t.TempDir()
	tool := filepath.Join(root, "woff2-subsetter")
	writeTool(t, root, "woff2-subsetter", `
for value in "$@"; do case "$value" in --output-file=*) output="${value#--output-file=}";; esac; done
printf 'not a font' > "$output"
`)
	pageDirectory := filepath.Join(root, "pages", "0001")
	if err := os.MkdirAll(pageDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := emitWOFF2Subsets(context.Background(), root, pageDirectory, sourceAwarePage{fonts: []sourceAwareFont{{
		program: testTrueTypeProgram(0), characters: map[rune]struct{}{'A': {}},
	}}}, &WOFF2Subsetter{Path: tool, Version: "1"})
	if err == nil || !strings.Contains(err.Error(), "did not emit a WOFF2") {
		t.Fatalf("expected rejected WOFF2 output, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(pageDirectory, "source-aware-assets")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected WOFF2 asset directory remains: %v", err)
	}
}

func testTrueTypeProgram(flags uint16) []byte {
	data := make([]byte, 64)
	binary.BigEndian.PutUint16(data[4:6], 1)
	copy(data[12:16], "OS/2")
	binary.BigEndian.PutUint32(data[20:24], 32)
	binary.BigEndian.PutUint32(data[24:28], 10)
	binary.BigEndian.PutUint16(data[40:42], flags)
	return data
}

func testWOFF2Program() []byte {
	data := make([]byte, 48)
	copy(data[:4], "wOF2")
	binary.BigEndian.PutUint32(data[8:12], uint32(len(data)))
	binary.BigEndian.PutUint16(data[12:14], 1)
	return data
}

func embeddedTrueTypePDF() []byte {
	fontProgram := testTrueTypeProgram(0)
	toUnicode := []byte(`/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def
/CMapName /Adobe-Identity-UCS def
/CMapType 2 def
1 begincodespacerange
<00> <FF>
endcodespacerange
1 beginbfchar
<41> <0041>
endbfchar
endcmap
CMapName currentdict /CMap defineresource pop
end
end`)
	content := []byte("BT /F1 12 Tf 100 100 Td (A) Tj ET")
	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Resources << /Font << /F1 6 0 R >> >> /Contents 4 0 R >>"),
		pdfStream(content, ""),
		[]byte("<< >>"),
		[]byte("<< /Type /Font /Subtype /TrueType /BaseFont /AAAAAA+TestFont /FirstChar 65 /LastChar 65 /Widths [600] /FontDescriptor 7 0 R /ToUnicode 9 0 R >>"),
		[]byte("<< /Type /FontDescriptor /FontName /AAAAAA+TestFont /Flags 32 /FontBBox [0 0 1000 1000] /ItalicAngle 0 /Ascent 1000 /Descent 0 /CapHeight 700 /StemV 80 /FontFile2 8 0 R >>"),
		pdfStream(fontProgram, "/Length1 "+strconv.Itoa(len(fontProgram))),
		pdfStream(toUnicode, ""),
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

func pdfStream(data []byte, extra string) []byte {
	var stream bytes.Buffer
	stream.WriteString("<< /Length ")
	stream.WriteString(strconv.Itoa(len(data)))
	if extra != "" {
		stream.WriteByte(' ')
		stream.WriteString(extra)
	}
	stream.WriteString(" >>\nstream\n")
	stream.Write(data)
	stream.WriteString("\nendstream")
	return stream.Bytes()
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
