package precheck

import (
	"path/filepath"
	"testing"
)

func TestBackgroundExtensionRequiresAcceptedPattern(t *testing.T) {
	clean := Parse(`{"watermark":false,"lens":false,"case_edge":false,"case_photo":false,"fill_top":true,"reason":"连续几何背景"}`, "")
	if !clean.OK || !clean.FillTop {
		t.Fatalf("background layout missing: %+v", clean)
	}
	bad := Parse(`{"watermark":false,"lens":true,"case_edge":false,"case_photo":false,"fill_top":true,"reason":"有镜头框"}`, "")
	if bad.OK || bad.FillTop {
		t.Fatal("rejected source was marked for background fill")
	}
}

func TestBackgroundModeSurvivesCacheReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cache.jsonl")
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(Record{Path: "pattern.png", Size: 1, MTime: 2, OK: true, FillTop: true}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, hit := s.Lookup("pattern.png", 1, 2)
	if !hit || !r.FillTop {
		t.Fatal("background mode lost in cache")
	}
}
