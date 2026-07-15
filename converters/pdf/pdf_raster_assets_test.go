package pdfconv

import (
	"bytes"
	"image/png"
	"testing"
)

func TestExtractPageRasterAssetsPreservesOriginalJPEGBytes(t *testing.T) {
	document := makeJPEGImagePDF()
	assets, err := ExtractPageRasterAssets(document, 1)
	if err != nil {
		t.Fatalf("ExtractPageRasterAssets() error = %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("asset count = %d, want 1", len(assets))
	}
	asset := assets[0]
	if asset.Name != "Im1" || asset.Role != "image" || asset.Encoding != "original_jpeg" || asset.MediaType != "image/jpeg" {
		t.Fatalf("unexpected JPEG asset: %#v", asset)
	}
	if !bytes.Contains(document, asset.Bytes) {
		t.Fatal("JPEG asset bytes were not preserved from the PDF stream")
	}
}

func TestExtractPageRasterAssetsKeepsMaskSeparate(t *testing.T) {
	assets, err := ExtractPageRasterAssets(makeMaskedJPEGImagePDF(), 1)
	if err != nil {
		t.Fatalf("ExtractPageRasterAssets() error = %v", err)
	}
	if len(assets) != 2 {
		t.Fatalf("asset count = %d, want image and mask", len(assets))
	}
	if assets[0].Name != "Im1" || assets[0].Role != "image" || assets[0].Encoding != "original_jpeg" {
		t.Fatalf("unexpected image asset: %#v", assets[0])
	}
	if assets[1].Name != "Im1-mask" || assets[1].Role != "mask" || assets[1].MaskFor != "Im1" || assets[1].Encoding != "lossless_png" {
		t.Fatalf("unexpected mask asset: %#v", assets[1])
	}
}

func TestExtractPageRasterAssetsKeepsSoftMaskSeparate(t *testing.T) {
	document := bytes.Replace(makeMaskedJPEGImagePDF(), []byte("/Mask 6 0 R"), []byte("/SMask 6 0 R"), 1)
	assets, err := ExtractPageRasterAssets(document, 1)
	if err != nil {
		t.Fatalf("ExtractPageRasterAssets() error = %v", err)
	}
	if len(assets) != 2 || assets[1].Name != "Im1-smask" || assets[1].Role != "mask" || assets[1].MaskFor != "Im1" {
		t.Fatalf("unexpected soft mask assets: %#v", assets)
	}
}

func TestExtractPageRasterAssetsSkipsUnpaintedResources(t *testing.T) {
	assets, err := ExtractPageRasterAssets(makeGrayImagePDFWithUnusedResource(), 1)
	if err != nil {
		t.Fatalf("ExtractPageRasterAssets() error = %v", err)
	}
	if len(assets) != 1 || assets[0].Name != "Im1" {
		t.Fatalf("unexpected painted assets: %#v", assets)
	}
}

func TestExtractPageRasterAssetsPreservesFlateDecodedPixels(t *testing.T) {
	assets, err := ExtractPageRasterAssets(makeGrayImagePDF(2, 1, []byte{0x00, 0xFF}), 1)
	if err != nil {
		t.Fatalf("ExtractPageRasterAssets() error = %v", err)
	}
	if len(assets) != 1 || assets[0].Encoding != "lossless_png" || assets[0].MediaType != "image/png" {
		t.Fatalf("unexpected Flate asset: %#v", assets)
	}
	decoded, err := png.Decode(bytes.NewReader(assets[0].Bytes))
	if err != nil {
		t.Fatalf("decode PNG = %v", err)
	}
	if left, _, _, _ := decoded.At(0, 0).RGBA(); left != 0 || decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 1 {
		t.Fatalf("unexpected first pixel or dimensions: bounds=%v pixel=%d", decoded.Bounds(), left)
	}
	if right, _, _, _ := decoded.At(1, 0).RGBA(); right != 0xFFFF {
		t.Fatalf("second pixel = %d, want 65535", right)
	}
}
