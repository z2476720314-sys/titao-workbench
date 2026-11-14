package judge

import (
	"strings"
	"testing"
)

func TestAlphaGuideIsDescribedAsReferenceRatherThanPrintedArtwork(t *testing.T) {
	if strings.Contains((Placement{}).Description(), "Alpha guide") {
		t.Fatal("ordinary reference was described as a diagnostic panel")
	}
	text := (Placement{SourceAlphaGuide: true}).Description()
	for _, want := range []string{"左幅Art", "Alpha guide", "不代表实际印刷颜色", "头部轮廓"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing reference interpretation %q", want)
		}
	}
}

func TestDiagnosticFirstAngleIsGeometryOnly(t *testing.T) {
	if strings.Contains((Placement{}).Description(), "首个成品角度") {
		t.Fatal("ordinary views received diagnostic instructions")
	}
	text := (Placement{DiagnosticFirstView: true}).Description()
	for _, want := range []string{"首个成品角度", "弱 alpha 轮廓", "品红", "不透明的正文", "眼睛和底色", "保留原色", "同一实际摆放", "不评配色"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing diagnostic interpretation %q", want)
		}
	}
}
