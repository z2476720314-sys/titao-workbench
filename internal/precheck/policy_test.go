package precheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLensEvidenceOverridesOverallAccept(t *testing.T) {
	v := Parse(`{"ok":true,"watermark":false,"lens":true,"case_edge":true,"case_photo":false,"reason":"顶部有镜头框"}`, "")
	if v.OK || v.Fallback || len(v.Tags) != 2 {
		t.Fatalf("frame evidence must reject: %+v", v)
	}
}

func TestIncompleteChecklistIsUnconfirmed(t *testing.T) {
	v := Parse(`{"ok":true,"reason":"干净图案"}`, "")
	if v.OK || !v.Fallback {
		t.Fatalf("missing checks must not pass: %+v", v)
	}
}

func TestLegacyPassIsNotCurrentPolicy(t *testing.T) {
	p := filepath.Join(t.TempDir(), "precheck.jsonl")
	if err := os.WriteFile(p, []byte("{\"path\":\"old.png\",\"size\":100,\"mtime\":10,\"ok\":true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, hit := s.Lookup("old.png", 100, 10); hit {
		t.Fatal("old accepted record must be rechecked")
	}
}
