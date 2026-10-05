package htmlconv

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LynnColeArt/Inkbite"
	"golang.org/x/net/html"
)

func TestHTMLConversion(t *testing.T) {
	input := `<html><head><title>Example</title><style>body{}</style><script>alert(1)</script></head><body><h1>Hello</h1><p>World</p></body></html>`

	converter := New()
	result, err := converter.Convert(context.Background(), bytes.NewReader([]byte(input)), inkbite.StreamInfo{
		MIMEType: "text/html",
	}, inkbite.ConvertOptions{})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if result.Title != "Example" {
		t.Fatalf("expected title Example, got %q", result.Title)
	}
	if !strings.Contains(result.Markdown, "# Hello") {
		t.Fatalf("expected heading in markdown, got %q", result.Markdown)
	}
	if strings.Contains(result.Markdown, "alert") {
		t.Fatalf("expected script content to be removed, got %q", result.Markdown)
	}
}

func TestHTMLTablePreservesRowsColumnsAndInlineContent(t *testing.T) {
	input := `<html><body><h1>Evidence</h1><table>
	<tr><th>Source</th><th>Status</th></tr>
	<tr><td><a href="https://example.invalid/raw">Raw source</a></td><td><strong>Retained</strong></td></tr>
	<tr><td>Parser | profile</td><td>Reviewed</td></tr>
	</table><p>After table.</p></body></html>`
	result, err := New().ConvertString(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"# Evidence", "Source", "Status", "[Raw source](https://example.invalid/raw)", "**Retained**", `Parser \| profile`, "Reviewed", "After table."} {
		if !strings.Contains(result.Markdown, wanted) {
			t.Fatalf("missing %q in Markdown:\n%s", wanted, result.Markdown)
		}
	}
	rows := 0
	for _, line := range strings.Split(result.Markdown, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			rows++
		}
	}
	if rows != 4 {
		t.Fatalf("expected header, separator, and two data rows; got %d:\n%s", rows, result.Markdown)
	}
}

func TestHTMLTableWithoutHeaderDoesNotPromoteDataToHeading(t *testing.T) {
	result, err := New().ConvertString(`<table><tr><td>First</td><td>One</td></tr><tr><td>Second</td><td>Two</td></tr></table>`)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"First", "One", "Second", "Two"} {
		if !strings.Contains(result.Markdown, wanted) {
			t.Fatalf("missing source cell %q: %s", wanted, result.Markdown)
		}
	}
	lines := strings.Split(result.Markdown, "\n")
	if len(lines) < 4 || strings.Contains(lines[0], "First") {
		t.Fatalf("source data was promoted to a header instead of preserved as a row:\n%s", result.Markdown)
	}
}

func TestHTMLRejectsSpanExpansionBeforeRendering(t *testing.T) {
	for _, attr := range []string{`colspan="2147483647"`, `rowspan="2147483647"`, `colspan="65537"`, `rowspan="65537"`} {
		_, err := New().ConvertString(`<table><tr><td ` + attr + `>source</td></tr></table>`)
		if !errors.Is(err, ErrHTMLLimit) {
			t.Fatalf("expected checked limit for %s, got %v", attr, err)
		}
	}
	_, err := New().ConvertString(`<table>` + strings.Repeat(`<tr><td colspan="32" rowspan="32">x</td></tr>`, 100) + `</table>`)
	if !errors.Is(err, ErrHTMLLimit) {
		t.Fatalf("combined small spans escaped the grid budget: %v", err)
	}
	_, err = New().ConvertString(`<table>` + strings.Repeat(`<tr><td colspan="32" colspan="1" rowspan="32">x</td></tr>`, 100) + `</table>`)
	if !errors.Is(err, ErrHTMLLimit) {
		t.Fatalf("duplicate attributes changed the effective span budget: %v", err)
	}
}

func TestBoundedWideAndRepeatedSourceSpans(t *testing.T) {
	for _, input := range []string{
		`<table><tr><td colspan="39">Actual wide source heading</td></tr><tr><td>Retained data</td></tr></table>`,
		`<table>` + strings.Repeat(`<tr><td colspan="3">Source</td><td colspan="3">Retained</td></tr>`, 300) + `</table>`,
	} {
		result, err := New().ConvertString(input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Markdown, "Retained") || len(result.Markdown) > 100000 {
			t.Fatalf("bounded source content lost or amplified: %d bytes", len(result.Markdown))
		}
	}
}

func TestRowspanExpansionAndDocumentGridStillBounded(t *testing.T) {
	for _, input := range []string{
		`<table><tr><td rowspan="65536">Source</td></tr></table>`,
		`<table><tr><td colspan="65536">Source</td></tr></table>`,
		strings.Repeat(`<table><tr><td colspan="39">Source</td></tr></table>`, 1000),
		`<table>` + strings.Repeat(`<tr><td colspan="256" rowspan="256">Source</td></tr>`, 2) + `</table>`,
	} {
		_, err := New().ConvertString(input)
		if !errors.Is(err, ErrHTMLLimit) {
			t.Fatalf("expected terminal expansion limit, got %v", err)
		}
	}
}

func TestDenseInsertShiftWorkIsRejectedBeforeRendering(t *testing.T) {
	// The rectangular footprint alone is below the cell budget, but the
	// overlapping row insertions would move over two million cell references.
	input := `<table><tr>` + strings.Repeat(`<td rowspan="64">source</td>`, 64) + `</tr>` +
		strings.Repeat(`<tr>`+strings.Repeat(`<td></td>`, 600)+`</tr>`, 63) + `</table>`
	_, err := New().ConvertString(input)
	if !errors.Is(err, ErrHTMLLimit) {
		t.Fatalf("unbounded insert work reached the renderer: %v", err)
	}
}

func TestSpanFootprintBoundsActualRenderedGrid(t *testing.T) {
	for _, input := range []string{
		`<table><thead><tr><th colspan="4">Heading</th></tr></thead><tr><td rowspan="3" colspan="2">A</td><td>B</td></tr><tr><td colspan="3">C</td></tr></table>`,
		`<table><tr><td colspan="5" rowspan="3">A</td><td rowspan="2">B</td></tr><tr><td colspan="3">C</td></tr><tr><td>D</td></tr></table>`,
	} {
		doc, err := html.Parse(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		footprint, err := tableFootprint(context.Background(), findFirstNode(doc, "table"), maxTableGridCells)
		if err != nil {
			t.Fatal(err)
		}
		result, err := New().ConvertString(input)
		if err != nil {
			t.Fatal(err)
		}
		cells := 0
		for _, line := range strings.Split(result.Markdown, "\n") {
			if strings.HasPrefix(line, "|") && strings.Trim(line, "| :-") != "" {
				cells += strings.Count(line, "|") - 1
			}
		}
		if cells == 0 || cells > footprint || footprint > maxTableGridCells {
			t.Fatalf("rendered grid escaped preflight: rendered=%d footprint=%d", cells, footprint)
		}
	}
}

func TestHTMLMinimalPaddingCannotAmplifyWideCellsAcrossRows(t *testing.T) {
	wide := strings.Repeat("w", 16384)
	input := `<table><tr><th>Source</th></tr><tr><td>` + wide + `</td></tr>` + strings.Repeat(`<tr><td>x</td></tr>`, 200) + `</table>`
	result, err := New().ConvertString(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(result.Markdown, wide) != 1 || len(result.Markdown) > len(input)+4096 {
		t.Fatalf("wide-cell padding amplified the source: input=%d output=%d", len(input), len(result.Markdown))
	}
}

func TestHTMLLimitsAndCancellationPropagate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().Convert(ctx, bytes.NewReader([]byte(`<p>source</p>`)), inkbite.StreamInfo{MIMEType: "text/html"}, inkbite.ConvertOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	_, err = New().ConvertString(strings.Repeat(`<div>`, maxHTMLDepth+1) + `source` + strings.Repeat(`</div>`, maxHTMLDepth+1))
	if !errors.Is(err, ErrHTMLLimit) {
		t.Fatalf("depth limit lost: %v", err)
	}
}
