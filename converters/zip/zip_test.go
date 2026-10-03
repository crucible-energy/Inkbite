package zipconv_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LynnColeArt/Inkbite"
	"github.com/LynnColeArt/Inkbite/builtins"
	"github.com/LynnColeArt/Inkbite/internal/testutil"
)

func TestZIPConversionFixture(t *testing.T) {
	engine := inkbite.New()
	builtins.RegisterDefaultConverters(engine)

	result, err := engine.Convert(context.Background(), testutil.BuildZipFixture(t, filepath.Join("testdata", "simple")), &inkbite.StreamInfo{
		Extension: ".zip",
		Filename:  "bundle.zip",
	}, inkbite.ConvertOptions{})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}

	for _, fragment := range []string{
		"Content from zip file `bundle.zip`",
		"## File: notes.txt",
		"hello from zip",
		"## File: data.csv",
		"| name | age |",
	} {
		if !strings.Contains(result.Markdown, fragment) {
			t.Fatalf("expected %q in markdown, got %q", fragment, result.Markdown)
		}
	}
}

type rejectedMemberConverter struct{ err error }

func (rejectedMemberConverter) Name() string      { return "rejected-member" }
func (rejectedMemberConverter) Priority() float64 { return 1 }
func (rejectedMemberConverter) Accepts(_ context.Context, _ io.ReadSeeker, info inkbite.StreamInfo, _ inkbite.ConvertOptions) bool {
	return info.Extension == ".reject"
}
func (c rejectedMemberConverter) Convert(context.Context, io.ReadSeeker, inkbite.StreamInfo, inkbite.ConvertOptions) (inkbite.Result, error) {
	return inkbite.Result{}, c.err
}

func TestZIPCannotSkipJoinedTerminalAndUnsupportedError(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	writeZipFile(t, w, "source.reject", "source")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, terminal := range []error{inkbite.ErrResourceLimit, context.Canceled, context.DeadlineExceeded} {
		t.Run(terminal.Error(), func(t *testing.T) {
			engine := inkbite.New()
			builtins.RegisterDefaultConverters(engine)
			engine.RegisterConverter(rejectedMemberConverter{err: errors.Join(inkbite.ErrUnsupportedFormat, terminal)})
			result, err := engine.Convert(context.Background(), buf.Bytes(), &inkbite.StreamInfo{Extension: ".zip"}, inkbite.ConvertOptions{})
			if !errors.Is(err, terminal) || result.Markdown != "" {
				t.Fatalf("ZIP skipped terminal member error: %#v, %v", result, err)
			}
		})
	}
}

func BenchmarkZIPConversionFixture(b *testing.B) {
	engine := inkbite.New()
	builtins.RegisterDefaultConverters(engine)
	archive := testutil.BuildZipFixture(b, filepath.Join("testdata", "simple"))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := engine.Convert(context.Background(), archive, &inkbite.StreamInfo{
			Extension: ".zip",
			Filename:  "bundle.zip",
		}, inkbite.ConvertOptions{}); err != nil {
			b.Fatalf("Convert() error = %v", err)
		}
	}
}

func TestZIPRejectsTooManyEntries(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 300; i++ {
		writeZipFile(t, zw, fmt.Sprintf("note-%03d.txt", i), "hello")
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	engine := inkbite.New()
	builtins.RegisterDefaultConverters(engine)

	_, err := engine.Convert(context.Background(), buf.Bytes(), &inkbite.StreamInfo{
		Extension: ".zip",
		Filename:  "many.zip",
	}, inkbite.ConvertOptions{})
	if err == nil {
		t.Fatal("expected archive entry limit error")
	}
	if !strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("expected entry limit error, got %v", err)
	}
}

func TestZIPRejectsInvalidArchive(t *testing.T) {
	engine := inkbite.New()
	builtins.RegisterDefaultConverters(engine)

	_, err := engine.Convert(context.Background(), []byte("not a zip archive\x00"), &inkbite.StreamInfo{
		Extension: ".zip",
		Filename:  "broken.zip",
	}, inkbite.ConvertOptions{})
	if err == nil {
		t.Fatal("expected invalid archive error")
	}
}

func TestZIPRejectsDeeplyNestedArchives(t *testing.T) {
	engine := inkbite.New()
	builtins.RegisterDefaultConverters(engine)

	archive := nestedArchive(t, 6)
	_, err := engine.Convert(context.Background(), archive, &inkbite.StreamInfo{
		Extension: ".zip",
		Filename:  "nested.zip",
	}, inkbite.ConvertOptions{})
	if err == nil {
		t.Fatal("expected recursion depth error")
	}
	if !strings.Contains(err.Error(), "recursion depth") {
		t.Fatalf("expected recursion depth error, got %v", err)
	}
}

func nestedArchive(t *testing.T, depth int) []byte {
	t.Helper()

	if depth <= 1 {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		writeZipFile(t, zw, "leaf.txt", "bottom")
		if err := zw.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		return buf.Bytes()
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	writer, err := zw.Create(fmt.Sprintf("nested-%d.zip", depth-1))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := writer.Write(nestedArchive(t, depth-1)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return buf.Bytes()
}

func writeZipFile(t *testing.T, zw *zip.Writer, name string, content string) {
	t.Helper()

	writer, err := zw.Create(name)
	if err != nil {
		t.Fatalf("Create(%q) error = %v", name, err)
	}
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatalf("Write(%q) error = %v", name, err)
	}
}
