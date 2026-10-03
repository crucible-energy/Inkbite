package rssconv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LynnColeArt/Inkbite"
	htmlconv "github.com/LynnColeArt/Inkbite/converters/html"
)

func TestRSSConversion(t *testing.T) {
	input := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Example Feed</title>
    <description><![CDATA[<p>Feed intro</p>]]></description>
    <item>
      <title>First Post</title>
      <pubDate>Fri, 21 Mar 2026 10:00:00 GMT</pubDate>
      <description><![CDATA[<p>Hello <strong>world</strong></p>]]></description>
    </item>
  </channel>
</rss>`

	converter := New()
	result, err := converter.Convert(context.Background(), bytes.NewReader([]byte(input)), inkbite.StreamInfo{
		MIMEType: "application/rss+xml",
	}, inkbite.ConvertOptions{})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	for _, fragment := range []string{
		"# Example Feed",
		"## First Post",
		"Published on: Fri, 21 Mar 2026 10:00:00 GMT",
		"Hello **world**",
	} {
		if !strings.Contains(result.Markdown, fragment) {
			t.Fatalf("expected %q in markdown, got %q", fragment, result.Markdown)
		}
	}
}

func TestFeedHTMLRejectionReturnsNoPartialResult(t *testing.T) {
	badHTML := `<table><tr><td colspan="33">source</td></tr></table><script>not-converted</script>`
	for _, tc := range []struct {
		name, format string
	}{
		{"rss-description", `<rss><channel><title>Feed</title><description><![CDATA[%s]]></description></channel></rss>`},
		{"rss-item-description", `<rss><channel><description>Accepted intro</description><item><description><![CDATA[%s]]></description></item></channel></rss>`},
		{"rss-encoded", `<rss xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel><item><description>Fallback text</description><content:encoded><![CDATA[%s]]></content:encoded></item></channel></rss>`},
		{"atom-subtitle", `<feed><title>Feed</title><subtitle><![CDATA[%s]]></subtitle></feed>`},
		{"atom-content", `<feed><entry><summary>Fallback text</summary><content><![CDATA[%s]]></content></entry></feed>`},
		{"atom-summary", `<feed><entry><summary><![CDATA[%s]]></summary></entry></feed>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := New().Convert(context.Background(), strings.NewReader(fmt.Sprintf(tc.format, badHTML)), inkbite.StreamInfo{}, inkbite.ConvertOptions{})
			if !errors.Is(err, htmlconv.ErrHTMLLimit) {
				t.Fatalf("expected propagated HTML limit, got %v", err)
			}
			if result.Markdown != "" || result.Title != "" {
				t.Fatalf("rejected source returned a partial or raw result: %#v", result)
			}
		})
	}
}

func TestFeedHTMLKeepsTablesAndDoesNotRestoreRemovedContent(t *testing.T) {
	for _, format := range []string{
		`<rss><channel><item><description><![CDATA[%s]]></description></item></channel></rss>`,
		`<feed><entry><content><![CDATA[%s]]></content></entry></feed>`,
	} {
		for _, html := range []string{
			`<script>not-converted</script><style>not-converted</style>`,
			`<table><tr><th>Source</th></tr><tr><td>Retained</td></tr></table><script>not-converted</script>`,
		} {
			result, err := New().Convert(context.Background(), strings.NewReader(fmt.Sprintf(format, html)), inkbite.StreamInfo{}, inkbite.ConvertOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.Markdown, "not-converted") {
				t.Fatalf("removed content was restored: %q", result.Markdown)
			}
			if strings.Contains(html, "<table>") && (!strings.Contains(result.Markdown, "| Source |") || !strings.Contains(result.Markdown, "| Retained |")) {
				t.Fatalf("source table was flattened: %q", result.Markdown)
			}
		}
	}
}

func TestFeedCancellationPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := New().Convert(ctx, strings.NewReader(`<feed><title>Source</title></feed>`), inkbite.StreamInfo{}, inkbite.ConvertOptions{})
	if !errors.Is(err, context.Canceled) || result.Markdown != "" {
		t.Fatalf("canceled feed returned %#v, %v", result, err)
	}
}
