package pdfconv

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LynnColeArt/Inkbite"
	"github.com/LynnColeArt/Inkbite/internal/testutil"
)

func TestLayoutToMarkdownConvertsTableBlocks(t *testing.T) {
	input := strings.Join([]string{
		"Inventory Summary",
		"",
		"Product Code    Location    Qty    Status",
		"SKU-1           A-01        10     OK",
		"SKU-2           B-07        5      HOLD",
		"",
		"Recommendations follow.",
	}, "\n")

	got := layoutToMarkdown(input)
	for _, fragment := range []string{
		"Inventory Summary",
		"| Product Code | Location | Qty | Status |",
		"| SKU-1 | A-01 | 10 | OK |",
		"| SKU-2 | B-07 | 5 | HOLD |",
		"Recommendations follow.",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("expected %q in markdown, got %q", fragment, got)
		}
	}
}

func TestLayoutToMarkdownLeavesParagraphsAlone(t *testing.T) {
	input := "This is a paragraph\nwith wrapped lines\nand no tabular structure."

	got := layoutToMarkdown(input)
	if strings.Contains(got, "| --- |") {
		t.Fatalf("expected no markdown table, got %q", got)
	}
	if !strings.Contains(got, "This is a paragraph") {
		t.Fatalf("expected paragraph text, got %q", got)
	}
}

func TestLayoutToMarkdownKeepsWideSpacedParagraphs(t *testing.T) {
	input := "The office of primary responsibility is:   FAA Headquarters, Mission Support Services"

	got := layoutToMarkdown(input)
	if !strings.Contains(got, "The office of primary responsibility is:") {
		t.Fatalf("expected dense paragraph text, got %q", got)
	}
	if !strings.Contains(got, "FAA Headquarters, Mission Support Services") {
		t.Fatalf("expected trailing paragraph text, got %q", got)
	}
}

func TestConvertUsesPureGoBackendWithoutPATH(t *testing.T) {
	t.Setenv("PATH", "")

	converter := New()
	result, err := converter.Convert(
		context.Background(),
		bytes.NewReader(testutil.LoadFixture(t, filepath.Join("testdata", "simple.pdf"))),
		inkbite.StreamInfo{Extension: ".pdf"},
		inkbite.ConvertOptions{PDFBackend: "auto"},
	)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if !strings.Contains(result.Markdown, "Hello PDF") {
		if !strings.Contains(result.Markdown, "Fixture PDF") {
			t.Fatalf("expected extracted PDF text, got %q", result.Markdown)
		}
	}
}

func TestPDFConversionFixture(t *testing.T) {
	converter := New()
	result, err := converter.Convert(
		context.Background(),
		bytes.NewReader(testutil.LoadFixture(t, filepath.Join("testdata", "simple.pdf"))),
		inkbite.StreamInfo{Extension: ".pdf"},
		inkbite.ConvertOptions{PDFBackend: "purego"},
	)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if !strings.Contains(result.Markdown, "Fixture PDF") {
		t.Fatalf("expected extracted PDF text, got %q", result.Markdown)
	}
}

func TestPDFConversionHandlesArrayPageContents(t *testing.T) {
	converter := New()
	result, err := converter.Convert(
		context.Background(),
		bytes.NewReader(makeArrayContentsPDF("First fragment", "Second fragment")),
		inkbite.StreamInfo{Extension: ".pdf"},
		inkbite.ConvertOptions{PDFBackend: "purego"},
	)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	for _, fragment := range []string{"First fragment", "Second fragment"} {
		if !strings.Contains(result.Markdown, fragment) {
			t.Fatalf("expected %q in extracted markdown, got %q", fragment, result.Markdown)
		}
	}
}

func TestPDFConversionPrefixesShortPagesWithHeading(t *testing.T) {
	converter := New()
	result, err := converter.Convert(
		context.Background(),
		bytes.NewReader(makeTwoPagePDF("First page", "Second page")),
		inkbite.StreamInfo{Extension: ".pdf"},
		inkbite.ConvertOptions{PDFBackend: "purego"},
	)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	for _, fragment := range []string{"# Page 1", "# Page 2", "First page", "Second page"} {
		if !strings.Contains(result.Markdown, fragment) {
			t.Fatalf("expected %q in extracted markdown, got %q", fragment, result.Markdown)
		}
	}
}

func TestPDFConversionLeavesLongPageUnprefixed(t *testing.T) {
	converter := New()
	result, err := converter.Convert(
		context.Background(),
		bytes.NewReader(makeSimplePDF(strings.Repeat("Long page content ", 200))),
		inkbite.StreamInfo{Extension: ".pdf"},
		inkbite.ConvertOptions{PDFBackend: "purego"},
	)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if strings.Contains(result.Markdown, "# Page 1") {
		t.Fatalf("expected long page to remain unprefixed, got %q", result.Markdown)
	}
}

func TestPDFConversionExtractsFlateImageDataURI(t *testing.T) {
	converter := New()
	result, err := converter.Convert(
		context.Background(),
		bytes.NewReader(makeGrayImagePDF(2, 1, []byte{0x00, 0xFF})),
		inkbite.StreamInfo{Extension: ".pdf"},
		inkbite.ConvertOptions{PDFBackend: "purego"},
	)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if !strings.Contains(result.Markdown, "![PDF image page 1 Im1](data:image/png;base64,") {
		t.Fatalf("expected PNG image data URI, got %q", result.Markdown)
	}
}

func TestPDFConversionExtractsJPEGImageDataURI(t *testing.T) {
	converter := New()
	result, err := converter.Convert(
		context.Background(),
		bytes.NewReader(makeJPEGImagePDF()),
		inkbite.StreamInfo{Extension: ".pdf"},
		inkbite.ConvertOptions{PDFBackend: "purego"},
	)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if !strings.Contains(result.Markdown, "![PDF image page 1 Im1](data:image/jpeg;base64,") {
		t.Fatalf("expected JPEG image data URI, got %q", result.Markdown)
	}
}

func TestPDFConversionSkipsUnusedImageResources(t *testing.T) {
	converter := New()
	result, err := converter.Convert(
		context.Background(),
		bytes.NewReader(makeGrayImagePDFWithUnusedResource()),
		inkbite.StreamInfo{Extension: ".pdf"},
		inkbite.ConvertOptions{PDFBackend: "purego"},
	)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if !strings.Contains(result.Markdown, "![PDF image page 1 Im1](data:image/png;base64,") {
		t.Fatalf("expected referenced PNG image data URI, got %q", result.Markdown)
	}
	if strings.Contains(result.Markdown, "![PDF image page 1 Im2](data:image/png;base64,") {
		t.Fatalf("expected unused image resource to be skipped, got %q", result.Markdown)
	}
}

func TestPDFConversionAppliesImageMaskAsAlpha(t *testing.T) {
	converter := New()
	result, err := converter.Convert(
		context.Background(),
		bytes.NewReader(makeMaskedJPEGImagePDF()),
		inkbite.StreamInfo{Extension: ".pdf"},
		inkbite.ConvertOptions{PDFBackend: "purego"},
	)
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	prefix := "![PDF image page 1 Im1](data:image/png;base64,"
	start := strings.Index(result.Markdown, prefix)
	if start < 0 {
		t.Fatalf("expected masked image PNG data URI, got %q", result.Markdown)
	}
	start += len(prefix)
	end := strings.Index(result.Markdown[start:], ")")
	if end < 0 {
		t.Fatalf("expected closing data URI marker, got %q", result.Markdown)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Markdown[start : start+end])
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	img, err := png.Decode(bytes.NewReader(decoded))
	if err != nil {
		t.Fatalf("png.Decode() error = %v", err)
	}
	left := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA)
	right := color.NRGBAModel.Convert(img.At(1, 0)).(color.NRGBA)
	if left.A != 0xFF || right.A != 0x00 {
		t.Fatalf("expected alpha mask to keep first pixel opaque and second pixel transparent, got left=%#v right=%#v", left, right)
	}
}

func TestChooseExtractorRejectsExternalBackendName(t *testing.T) {
	converter := New()

	_, err := converter.chooseExtractor("pdftotext")
	if err == nil {
		t.Fatal("expected error for unsupported external backend")
	}
	if !strings.Contains(err.Error(), "unknown PDF extractor") {
		t.Fatalf("expected unknown backend error, got %v", err)
	}
}

func TestConvertRejectsMalformedPDF(t *testing.T) {
	converter := New()

	_, err := converter.Convert(
		context.Background(),
		bytes.NewReader([]byte("%PDF-1.4\nnot actually a PDF\n%%EOF")),
		inkbite.StreamInfo{Extension: ".pdf"},
		inkbite.ConvertOptions{PDFBackend: "auto"},
	)
	if err == nil {
		t.Fatal("expected malformed PDF error")
	}
}

func makeSimplePDF(text string) []byte {
	stream := "BT\n/F1 24 Tf\n100 100 Td\n(" + escapePDFString(text) + ") Tj\nET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var doc bytes.Buffer
	doc.WriteString("%PDF-1.4\n")

	offsets := make([]int, len(objects)+1)
	for idx, object := range objects {
		offsets[idx+1] = doc.Len()
		fmt.Fprintf(&doc, "%d 0 obj\n%s\nendobj\n", idx+1, object)
	}

	xrefOffset := doc.Len()
	fmt.Fprintf(&doc, "xref\n0 %d\n", len(objects)+1)
	doc.WriteString("0000000000 65535 f \n")
	for idx := 1; idx <= len(objects); idx++ {
		fmt.Fprintf(&doc, "%010d 00000 n \n", offsets[idx])
	}
	fmt.Fprintf(&doc, "trailer\n<< /Root 1 0 R /Size %d >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)

	return doc.Bytes()
}

func makeArrayContentsPDF(first, second string) []byte {
	stream1 := "BT\n/F1 18 Tf\n72 120 Td\n(" + escapePDFString(first) + ") Tj\nET"
	stream2 := "BT\n/F1 18 Tf\n72 90 Td\n(" + escapePDFString(second) + ") Tj\nET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents [4 0 R 5 0 R] /Resources << /Font << /F1 6 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream1), stream1),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream2), stream2),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var doc bytes.Buffer
	doc.WriteString("%PDF-1.4\n")

	offsets := make([]int, len(objects)+1)
	for idx, object := range objects {
		offsets[idx+1] = doc.Len()
		fmt.Fprintf(&doc, "%d 0 obj\n%s\nendobj\n", idx+1, object)
	}

	xrefOffset := doc.Len()
	fmt.Fprintf(&doc, "xref\n0 %d\n", len(objects)+1)
	doc.WriteString("0000000000 65535 f \n")
	for idx := 1; idx <= len(objects); idx++ {
		fmt.Fprintf(&doc, "%010d 00000 n \n", offsets[idx])
	}
	fmt.Fprintf(&doc, "trailer\n<< /Root 1 0 R /Size %d >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)

	return doc.Bytes()
}

func makeTwoPagePDF(first, second string) []byte {
	stream1 := "BT\n/F1 18 Tf\n72 120 Td\n(" + escapePDFString(first) + ") Tj\nET"
	stream2 := "BT\n/F1 18 Tf\n72 120 Td\n(" + escapePDFString(second) + ") Tj\nET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 5 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 6 0 R /Resources << /Font << /F1 7 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream1), stream1),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream2), stream2),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var doc bytes.Buffer
	doc.WriteString("%PDF-1.4\n")

	offsets := make([]int, len(objects)+1)
	for idx, object := range objects {
		offsets[idx+1] = doc.Len()
		fmt.Fprintf(&doc, "%d 0 obj\n%s\nendobj\n", idx+1, object)
	}

	xrefOffset := doc.Len()
	fmt.Fprintf(&doc, "xref\n0 %d\n", len(objects)+1)
	doc.WriteString("0000000000 65535 f \n")
	for idx := 1; idx <= len(objects); idx++ {
		fmt.Fprintf(&doc, "%010d 00000 n \n", offsets[idx])
	}
	fmt.Fprintf(&doc, "trailer\n<< /Root 1 0 R /Size %d >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)

	return doc.Bytes()
}

func makeGrayImagePDF(width, height int, pixels []byte) []byte {
	content := []byte("q\n2 0 0 1 0 0 cm\n/Im1 Do\nQ")

	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R >> >> >>"),
		[]byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)),
		makeGrayImageXObject(width, height, pixels),
	}
	return makeBinaryPDF(objects)
}

func makeGrayImagePDFWithUnusedResource() []byte {
	content := []byte("q\n2 0 0 1 0 0 cm\n/Im1 Do\nQ")

	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R /Im2 6 0 R >> >> >>"),
		[]byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)),
		makeGrayImageXObject(1, 1, []byte{0x00}),
		makeGrayImageXObject(1, 1, []byte{0xFF}),
	}
	return makeBinaryPDF(objects)
}

func makeMaskedJPEGImagePDF() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 0xFF, A: 0xFF})
	img.Set(1, 0, color.RGBA{G: 0xFF, A: 0xFF})

	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, img, nil); err != nil {
		panic(err)
	}

	content := []byte("q\n2 0 0 1 0 0 cm\n/Im1 Do\nQ")
	imageStream := append([]byte(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Mask 6 0 R /Length %d >>\nstream\n", jpegBytes.Len())), jpegBytes.Bytes()...)
	imageStream = append(imageStream, []byte("\nendstream")...)
	maskStream := []byte("<< /Type /XObject /Subtype /Image /Width 2 /Height 1 /ImageMask true /BitsPerComponent 1 /Length 1 >>\nstream\n@\nendstream")

	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R /Im2 6 0 R >> >> >>"),
		[]byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)),
		imageStream,
		maskStream,
	}
	return makeBinaryPDF(objects)
}

func makeGrayImageXObject(width, height int, pixels []byte) []byte {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(pixels); err != nil {
		panic(err)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	imageStream := append([]byte(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", width, height, compressed.Len())), compressed.Bytes()...)
	imageStream = append(imageStream, []byte("\nendstream")...)
	return imageStream
}

func makeJPEGImagePDF() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 0xFF, A: 0xFF})
	img.Set(1, 0, color.RGBA{G: 0xFF, A: 0xFF})

	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, img, nil); err != nil {
		panic(err)
	}

	content := []byte("q\n2 0 0 1 0 0 cm\n/Im1 Do\nQ")
	imageStream := append([]byte(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 2 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>\nstream\n", jpegBytes.Len())), jpegBytes.Bytes()...)
	imageStream = append(imageStream, []byte("\nendstream")...)

	objects := [][]byte{
		[]byte("<< /Type /Catalog /Pages 2 0 R >>"),
		[]byte("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		[]byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Contents 4 0 R /Resources << /XObject << /Im1 5 0 R >> >> >>"),
		[]byte(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)),
		imageStream,
	}
	return makeBinaryPDF(objects)
}

func makeBinaryPDF(objects [][]byte) []byte {
	var doc bytes.Buffer
	doc.WriteString("%PDF-1.4\n")

	offsets := make([]int, len(objects)+1)
	for idx, object := range objects {
		offsets[idx+1] = doc.Len()
		fmt.Fprintf(&doc, "%d 0 obj\n", idx+1)
		doc.Write(object)
		doc.WriteString("\nendobj\n")
	}

	xrefOffset := doc.Len()
	fmt.Fprintf(&doc, "xref\n0 %d\n", len(objects)+1)
	doc.WriteString("0000000000 65535 f \n")
	for idx := 1; idx <= len(objects); idx++ {
		fmt.Fprintf(&doc, "%010d 00000 n \n", offsets[idx])
	}
	fmt.Fprintf(&doc, "trailer\n<< /Root 1 0 R /Size %d >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefOffset)
	return doc.Bytes()
}

func escapePDFString(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "(", `\(`)
	value = strings.ReplaceAll(value, ")", `\)`)
	return value
}
