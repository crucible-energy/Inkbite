package pdfconv

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"regexp"

	"github.com/dslipak/pdf"
	"golang.org/x/image/ccitt"
)

var doOperatorPattern = regexp.MustCompile(`/([^\s<>\[\]\(\)%/]+)\s+Do\b`)

func extractPageAssets(page pdf.Page, pageNum int) ([]string, error) {
	referenced, err := referencedXObjectNames(page)
	if err != nil {
		return nil, err
	}
	if len(referenced) == 0 {
		return nil, nil
	}

	xobjects := page.Resources().Key("XObject")
	names := xobjects.Keys()
	if len(names) == 0 {
		return nil, nil
	}

	lines := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := referenced[name]; !ok {
			continue
		}
		xobject := xobjects.Key(name)
		if xobject.IsNull() || xobject.Key("Subtype").Name() != "Image" {
			continue
		}
		line, err := markdownImageForXObject(xobject, pageNum, name)
		if err != nil {
			return nil, err
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func referencedXObjectNames(page pdf.Page) (map[string]struct{}, error) {
	content, err := contentStreamBytes(page.V.Key("Contents"))
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, nil
	}
	matches := doOperatorPattern.FindAllSubmatch(content, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	names := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		names[string(match[1])] = struct{}{}
	}
	return names, nil
}

func contentStreamBytes(contents pdf.Value) ([]byte, error) {
	switch contents.Kind() {
	case pdf.Null:
		return nil, nil
	case pdf.Stream:
		return readAllStreamBytes(contents)
	case pdf.Array:
		var combined bytes.Buffer
		for i := 0; i < contents.Len(); i++ {
			chunk, err := contentStreamBytes(contents.Index(i))
			if err != nil {
				return nil, err
			}
			if len(chunk) == 0 {
				continue
			}
			if combined.Len() > 0 {
				combined.WriteByte('\n')
			}
			combined.Write(chunk)
		}
		return combined.Bytes(), nil
	default:
		return nil, fmt.Errorf("unsupported page contents kind %v", contents.Kind())
	}
}

func markdownImageForXObject(xobject pdf.Value, pageNum int, name string) (string, error) {
	data, mediaType, err := imageAssetData(xobject)
	if err != nil {
		return fmt.Sprintf("[PDF image page %d %s error: %v]", pageNum, name, err), nil
	}
	if len(data) == 0 {
		return fmt.Sprintf("[PDF image page %d %s skipped: empty image stream]", pageNum, name), nil
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("![PDF image page %d %s](data:%s;base64,%s)", pageNum, name, mediaType, encoded), nil
}

func imageAssetData(xobject pdf.Value) ([]byte, string, error) {
	width, height, bitsPerComponent, err := imageGeometry(xobject)
	if err != nil {
		return nil, "", err
	}

	filter := terminalFilterName(xobject.Key("Filter"))
	switch filter {
	case "DCTDecode":
		data, err := readAllStreamBytes(xobject)
		if err != nil {
			return nil, "", err
		}
		if _, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil {
			return nil, "", fmt.Errorf("invalid JPEG image stream: %w", err)
		}
		return data, "image/jpeg", nil
	case "FlateDecode":
		raw, err := readAllStreamBytes(xobject)
		if err != nil {
			return nil, "", err
		}
		data, err := rasterSamplesToPNG(raw, xobject.Key("ColorSpace"), width, height, bitsPerComponent)
		if err != nil {
			return nil, "", err
		}
		return data, "image/png", nil
	case "CCITTFaxDecode":
		data, err := ccittImageToPNG(xobject, width, height)
		if err != nil {
			return nil, "", err
		}
		return data, "image/png", nil
	case "":
		data, err := rawMaskToPNG(xobject, width, height, bitsPerComponent)
		if err != nil {
			return nil, "", err
		}
		return data, "image/png", nil
	default:
		return nil, "", fmt.Errorf("unsupported PDF image filter %q", filter)
	}
}

func imageGeometry(xobject pdf.Value) (width int, height int, bitsPerComponent int, err error) {
	width = int(xobject.Key("Width").Int64())
	height = int(xobject.Key("Height").Int64())
	if width <= 0 || height <= 0 {
		return 0, 0, 0, fmt.Errorf("invalid PDF image dimensions %dx%d", width, height)
	}
	bitsPerComponent = int(xobject.Key("BitsPerComponent").Int64())
	if bitsPerComponent == 0 && xobject.Key("ImageMask").Bool() {
		bitsPerComponent = 1
	}
	if bitsPerComponent <= 0 {
		return 0, 0, 0, fmt.Errorf("invalid PDF image bits-per-component %d", bitsPerComponent)
	}
	return width, height, bitsPerComponent, nil
}

func terminalFilterName(filter pdf.Value) string {
	switch filter.Kind() {
	case pdf.Name:
		return filter.Name()
	case pdf.Array:
		if filter.Len() == 0 {
			return ""
		}
		return filter.Index(filter.Len() - 1).Name()
	default:
		return ""
	}
}

func readAllStreamBytes(stream pdf.Value) ([]byte, error) {
	reader := stream.Reader()
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func rawMaskToPNG(xobject pdf.Value, width int, height int, bitsPerComponent int) ([]byte, error) {
	if bitsPerComponent != 1 {
		return nil, fmt.Errorf("unsupported unfiltered PDF image bits-per-component %d", bitsPerComponent)
	}
	raw, err := readAllStreamBytes(xobject)
	if err != nil {
		return nil, err
	}
	return packBitmapToPNG(raw, width, height)
}

func ccittImageToPNG(xobject pdf.Value, width int, height int) ([]byte, error) {
	stream := xobject.Reader()
	defer stream.Close()

	subformat := ccitt.Group4
	if xobject.Key("DecodeParms").Key("K").Int64() >= 0 {
		subformat = ccitt.Group3
	}
	reader := ccitt.NewReader(stream, ccitt.MSB, subformat, width, height, &ccitt.Options{})
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("invalid CCITT image stream: %w", err)
	}
	return packBitmapToPNG(raw, width, height)
}

func rasterSamplesToPNG(raw []byte, colorSpace pdf.Value, width int, height int, bitsPerComponent int) ([]byte, error) {
	if bitsPerComponent == 1 {
		switch colorSpaceName(colorSpace) {
		case "", "DeviceGray":
			return packBitmapToPNG(raw, width, height)
		case "Indexed":
			return nil, fmt.Errorf("unsupported 1-bit indexed PDF image")
		default:
			return nil, fmt.Errorf("unsupported 1-bit PDF colorspace %q", colorSpaceName(colorSpace))
		}
	}

	switch colorSpaceName(colorSpace) {
	case "DeviceGray", "":
		if bitsPerComponent != 8 {
			return nil, fmt.Errorf("unsupported grayscale PDF image bits-per-component %d", bitsPerComponent)
		}
		expected := width * height
		if len(raw) < expected {
			return nil, fmt.Errorf("truncated grayscale PDF image data: got %d bytes want %d", len(raw), expected)
		}
		img := image.NewGray(image.Rect(0, 0, width, height))
		copy(img.Pix, raw[:expected])
		return encodePNG(img)
	case "DeviceRGB":
		if bitsPerComponent != 8 {
			return nil, fmt.Errorf("unsupported RGB PDF image bits-per-component %d", bitsPerComponent)
		}
		expected := width * height * 3
		if len(raw) < expected {
			return nil, fmt.Errorf("truncated RGB PDF image data: got %d bytes want %d", len(raw), expected)
		}
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		src := raw[:expected]
		dst := img.Pix
		for i, j := 0, 0; i < expected; i, j = i+3, j+4 {
			dst[j+0] = src[i+0]
			dst[j+1] = src[i+1]
			dst[j+2] = src[i+2]
			dst[j+3] = 0xFF
		}
		return encodePNG(img)
	case "Indexed":
		if bitsPerComponent != 8 {
			return nil, fmt.Errorf("unsupported indexed PDF image bits-per-component %d", bitsPerComponent)
		}
		palette, err := indexedPalette(colorSpace)
		if err != nil {
			return nil, err
		}
		expected := width * height
		if len(raw) < expected {
			return nil, fmt.Errorf("truncated indexed PDF image data: got %d bytes want %d", len(raw), expected)
		}
		img := image.NewPaletted(image.Rect(0, 0, width, height), palette)
		copy(img.Pix, raw[:expected])
		return encodePNG(img)
	default:
		return nil, fmt.Errorf("unsupported PDF image colorspace %q", colorSpaceName(colorSpace))
	}
}

func colorSpaceName(colorSpace pdf.Value) string {
	switch colorSpace.Kind() {
	case pdf.Name:
		return colorSpace.Name()
	case pdf.Array:
		return colorSpace.Index(0).Name()
	default:
		return ""
	}
}

func indexedPalette(colorSpace pdf.Value) (color.Palette, error) {
	if colorSpace.Kind() != pdf.Array || colorSpace.Len() < 4 || colorSpace.Index(0).Name() != "Indexed" {
		return nil, fmt.Errorf("unsupported indexed PDF colorspace")
	}
	if colorSpace.Index(1).Name() != "DeviceRGB" {
		return nil, fmt.Errorf("unsupported indexed PDF base colorspace %q", colorSpace.Index(1).Name())
	}
	hi := int(colorSpace.Index(2).Int64())
	if hi < 0 {
		return nil, fmt.Errorf("invalid indexed PDF palette size %d", hi)
	}
	lookup, err := lookupBytes(colorSpace.Index(3))
	if err != nil {
		return nil, err
	}
	required := (hi + 1) * 3
	if len(lookup) < required {
		return nil, fmt.Errorf("truncated indexed PDF palette: got %d bytes want %d", len(lookup), required)
	}
	palette := make(color.Palette, hi+1)
	for index := range palette {
		offset := index * 3
		palette[index] = color.RGBA{
			R: lookup[offset+0],
			G: lookup[offset+1],
			B: lookup[offset+2],
			A: 0xFF,
		}
	}
	return palette, nil
}

func lookupBytes(value pdf.Value) ([]byte, error) {
	switch value.Kind() {
	case pdf.String:
		return []byte(value.RawString()), nil
	case pdf.Stream:
		return readAllStreamBytes(value)
	default:
		return nil, fmt.Errorf("unsupported indexed PDF palette lookup type %v", value.Kind())
	}
}

func packBitmapToPNG(raw []byte, width int, height int) ([]byte, error) {
	img := image.NewGray(image.Rect(0, 0, width, height))
	rowBytes := (width + 7) / 8
	for y := 0; y < height; y++ {
		row := raw[y*rowBytes:]
		for x := 0; x < width; x++ {
			byteIndex := x / 8
			bitIndex := uint(7 - (x % 8))
			if byteIndex >= len(row) {
				break
			}
			bit := (row[byteIndex] >> bitIndex) & 1
			if bit == 1 {
				img.SetGray(x, y, color.Gray{Y: 0xFF})
			} else {
				img.SetGray(x, y, color.Gray{Y: 0x00})
			}
		}
	}
	return encodePNG(img)
}

func encodePNG(img image.Image) ([]byte, error) {
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
