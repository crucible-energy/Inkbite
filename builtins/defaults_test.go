package builtins

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/LynnColeArt/Inkbite"
	htmlconv "github.com/LynnColeArt/Inkbite/converters/html"
)

func TestRegisterDefaultConverters(t *testing.T) {
	engine := inkbite.New()
	RegisterDefaultConverters(engine)

	var names []string
	for _, converter := range engine.RegisteredConverters() {
		names = append(names, converter.Name())
	}

	for _, want := range []string{"ipynb", "xlsx", "xls", "docx", "pptx", "pdf", "csv", "epub", "rss", "zip", "html", "text"} {
		if !slices.Contains(names, want) {
			t.Fatalf("expected converter %q in defaults, got %v", want, names)
		}
	}
}

func TestDefaultDispatchCannotBypassHTMLLimit(t *testing.T) {
	engine := inkbite.New()
	RegisterDefaultConverters(engine)
	html := `<table><tr><td colspan="33">source</td></tr></table><script>not-converted</script>`
	rss := `<rss><channel><item><description><![CDATA[` + html + `]]></description></item></channel></rss>`
	atom := `<feed><entry><content><![CDATA[` + html + `]]></content></entry></feed>`
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	entry, err := w.Create("rejected.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(html)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, extension string
		data            []byte
	}{
		{"html", ".html", []byte(html)},
		{"rss", ".rss", []byte(rss)},
		{"rss-xml", ".xml", []byte(rss)},
		{"atom", ".atom", []byte(atom)},
		{"atom-xml", ".xml", []byte(atom)},
		{"zip-member", ".zip", archive.Bytes()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.Convert(context.Background(), tc.data, &inkbite.StreamInfo{Extension: tc.extension}, inkbite.ConvertOptions{})
			if !errors.Is(err, htmlconv.ErrHTMLLimit) || !errors.Is(err, inkbite.ErrResourceLimit) {
				t.Fatalf("expected terminal HTML rejection, got %#v, %v", result, err)
			}
			if result.Markdown != "" {
				t.Fatalf("dispatch returned rejected source through fallback: %q", result.Markdown)
			}
		})
	}
}
