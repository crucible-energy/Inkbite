package visualpdf

import (
	"bytes"
	"testing"
)

func TestStripPopplerGlyphOutlinesSupportsPopplerAndCairoShapes(t *testing.T) {
	tests := []struct {
		name     string
		document string
		removed  string
	}{
		{
			name:     "poppler group with xlink reference",
			document: `<svg xmlns:xlink="http://www.w3.org/1999/xlink"><defs><g id="glyph-0-0"><path d="M0 0"/></g></defs><use xlink:href="#glyph-0-0"/><symbol id="logo"><path d="M1 1"/></symbol><use href="#logo"/></svg>`,
			removed:  "glyph-0-0",
		},
		{
			name:     "cairo symbol with href reference",
			document: `<svg><defs><symbol id="glyph0-0"><path d="M0 0"/></symbol></defs><use href="#glyph0-0"/><symbol id="logo"><path d="M1 1"/></symbol><use href="#logo"/></svg>`,
			removed:  "glyph0-0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := stripPopplerGlyphOutlines([]byte(test.document))
			if err != nil {
				t.Fatalf("stripPopplerGlyphOutlines() error = %v", err)
			}
			if bytes.Contains(got, []byte(test.removed)) {
				t.Fatalf("stripped SVG still contains glyph %q: %s", test.removed, got)
			}
			if !bytes.Contains(got, []byte(`id="logo"`)) || !bytes.Contains(got, []byte(`href="#logo"`)) {
				t.Fatalf("stripped SVG removed unrelated symbols or references: %s", got)
			}
		})
	}
}
