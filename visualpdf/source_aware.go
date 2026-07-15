package visualpdf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/dslipak/pdf"
)

// sourceAwareEligibility identifies the first source-font requirement that a
// future positioned-text candidate must satisfy. It deliberately does not
// guess at a substitute font: only embedded TrueType programs with a PDF
// ToUnicode map and an embedding policy that permits subsetting are eligible.
func sourceAwareEligibility(document []byte, pageNumber int, hasWOFF2Subsetter bool) (reason string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			reason = fmt.Sprintf("source-aware font inspection failed: %v", recovered)
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(document), int64(len(document)))
	if err != nil {
		return fmt.Sprintf("source-aware font inspection cannot read PDF: %v", err)
	}
	if pageNumber <= 0 || pageNumber > reader.NumPage() {
		return "source-aware font inspection page is unavailable"
	}
	page := reader.Page(pageNumber)
	if page.V.IsNull() {
		return "source-aware font inspection page is null"
	}
	text := page.Content().Text
	if len(text) == 0 {
		return "source-aware text candidate requires drawable PDF text"
	}
	used := make(map[string]struct{}, len(text))
	for _, run := range text {
		if run.Font == "" || run.S == "" {
			return "source-aware text candidate has incomplete PDF text runs"
		}
		used[run.Font] = struct{}{}
	}
	fonts := make(map[string]pdf.Font)
	for _, name := range page.Fonts() {
		font := page.Font(name)
		base := strings.TrimPrefix(font.BaseFont(), fontSubsetPrefix(font.BaseFont()))
		if base != "" {
			fonts[base] = font
		}
	}
	for name := range used {
		font, ok := fonts[name]
		if !ok {
			return fmt.Sprintf("source-aware text candidate cannot map PDF font %q", name)
		}
		if font.V.Key("Subtype").Name() != "TrueType" {
			return fmt.Sprintf("source-aware text candidate font %q is not embedded TrueType", name)
		}
		if font.V.Key("ToUnicode").Kind() != pdf.Stream {
			return fmt.Sprintf("source-aware text candidate font %q lacks a ToUnicode glyph map", name)
		}
		program := font.V.Key("FontDescriptor").Key("FontFile2")
		if program.Kind() != pdf.Stream {
			return fmt.Sprintf("source-aware text candidate font %q lacks an embedded TrueType program", name)
		}
		data, err := readFontProgram(program)
		if err != nil {
			return fmt.Sprintf("source-aware text candidate font %q program is unreadable: %v", name, err)
		}
		if failure := trueTypeEmbeddingPolicyFailure(data); failure != "" {
			return fmt.Sprintf("source-aware text candidate font %q %s", name, failure)
		}
	}
	if !hasWOFF2Subsetter {
		return "source-aware text candidate requires a pinned WOFF2 subsetter; outlined glyph candidate remains required"
	}
	return "source-aware text candidate requires positioned text SVG emission; outlined glyph candidate remains required"
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
