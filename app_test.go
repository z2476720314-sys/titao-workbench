package main

import (
	"strings"
	"testing"

	"titao/internal/cfg"
)

func TestUnselectedOutputShowsEmptyLibraryAndActionableError(t *testing.T) {
	s, err := cfg.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{store: s}
	outputs, err := a.ListOutputs()
	if err != nil || outputs == nil || len(outputs) != 0 {
		t.Fatalf("empty library: %v, %v", outputs, err)
	}
	generated, err := a.ListGenerated()
	if err != nil || generated == nil || len(generated) != 0 {
		t.Fatalf("empty generated library: %v, %v", generated, err)
	}
	if err := a.OpenOutputDir(); err == nil || !strings.Contains(err.Error(), "选择输出目录") {
		t.Fatalf("missing directory must explain what to select: %v", err)
	}
}
