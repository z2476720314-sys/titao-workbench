package fileutil

import (
	"strings"
	"testing"
)

func TestRevealHRESULTChecksSignedCOMResult(t *testing.T) {
	for _, code := range []uintptr{0, 1} {
		if err := revealHRESULT("select", code); err != nil {
			t.Fatal(err)
		}
	}
	if err := revealHRESULT("select", uintptr(0x80004005)); err == nil || !strings.Contains(err.Error(), "0x80004005") || !strings.Contains(err.Error(), "select") {
		t.Fatalf("lost Shell error: %v", err)
	}
}
