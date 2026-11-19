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

func TestReviewComparesClippingAgainstExistingSourceContour(t *testing.T) {
	text := (Placement{NoCrop: true}).Description()
	for _, want := range []string{"原图已有的平直截断轮廓", "只判断成品新增的裁切或镜头遮挡", "不得推想原图不存在的头顶"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing source-boundary interpretation %q", want)
		}
	}
}

func TestAnchoredSourceCutsAreNotReportedAsCameraOcclusion(t *testing.T) {
	text := (Placement{NoCrop: true, AnchorX: 2}).Description()
	for _, want := range []string{"原图左右边缘本来截开的文字", "同样的半个字", "摄像头实际覆盖原图已有笔画"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing original-edge evidence rule %q", want)
		}
	}
}
