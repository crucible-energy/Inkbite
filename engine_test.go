package inkbite

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

type stubConverter struct {
	name     string
	priority float64
	accepts  bool
	markdown string
}

func (s stubConverter) Name() string {
	return s.name
}

func (s stubConverter) Priority() float64 {
	return s.priority
}

func (s stubConverter) Accepts(context.Context, io.ReadSeeker, StreamInfo, ConvertOptions) bool {
	return s.accepts
}

func (s stubConverter) Convert(context.Context, io.ReadSeeker, StreamInfo, ConvertOptions) (Result, error) {
	return Result{Markdown: s.markdown}, nil
}

func TestEnginePrefersLowerPriorityValue(t *testing.T) {
	engine := New()
	engine.RegisterConverter(stubConverter{
		name:     "slow",
		priority: 50,
		accepts:  true,
		markdown: "slow",
	})
	engine.RegisterConverter(stubConverter{
		name:     "fast",
		priority: 10,
		accepts:  true,
		markdown: "fast",
	})

	result, err := engine.Convert(context.Background(), []byte("hello"), nil, ConvertOptions{})
	if err != nil {
		t.Fatalf("Convert() error = %v", err)
	}
	if result.Markdown != "fast" {
		t.Fatalf("expected lower priority converter result, got %q", result.Markdown)
	}
}

func TestEngineReturnsUnsupportedFormat(t *testing.T) {
	engine := New()

	_, err := engine.Convert(context.Background(), []byte("hello"), nil, ConvertOptions{})
	if err == nil {
		t.Fatal("expected unsupported format error")
	}
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("expected unsupported format error, got %v", err)
	}
}

func TestEngineRegistrySupportsConcurrentSnapshots(t *testing.T) {
	engine := New()
	const registrations = 128
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		for index := 0; index < registrations; index++ {
			engine.RegisterConverter(stubConverter{name: fmt.Sprintf("converter-%d", index)})
		}
	}()
	go func() {
		defer group.Done()
		<-start
		for index := 0; index < registrations; index++ {
			_ = engine.RegisteredConverters()
		}
	}()
	close(start)
	group.Wait()
	if got := len(engine.RegisteredConverters()); got != registrations {
		t.Fatalf("registered converter count = %d, want %d", got, registrations)
	}
}
