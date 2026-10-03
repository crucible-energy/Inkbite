package htmlconv

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LynnColeArt/Inkbite"
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
	for _, attr := range []string{`colspan="2147483647"`, `rowspan="2147483647"`, `colspan="33"`, `rowspan="33"`} {
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
