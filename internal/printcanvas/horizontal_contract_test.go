package printcanvas

import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"titao/internal/psx"
)

func TestDirectPairedSidesChangeOnlyHorizontalScale(t *testing.T) {
	for _, tc := range []struct{ stretch, height, y float64 }{{1, 1200, 690}, {1.05, 1260, 630}} {
		for _, noCrop := range []bool{true, false} {
			for _, preferOffsets := range []bool{false, true} {
				req := psx.RenderRequest{Fit: psx.FitAuto, NoCrop: noCrop, PreferOffsets: preferOffsets, AnchorX: 2, AnchorY: 1, StretchY: tc.stretch}
				plan, err := Plan(patternFile(t, solid(900, 1200)), filepath.Join(t.TempDir(), "plan.json"), req, 1)
				if err != nil {
					t.Fatal(err)
				}
				m := plan.Transform
				if math.Abs(m[0]-1.05) > 1e-5 || math.Abs(m[3]-tc.stretch) > 1e-9 || math.Abs(1200*m[3]-tc.height) > 1e-6 || math.Abs(m[5]-tc.y) > 1e-3 || plan.Placement.AnchorUnmet || !plan.Verified {
					t.Errorf("NoCrop=%t PreferOffsets=%t stretch=%g changed vertical size or lost bottom contact: transform=%v placement=%+v", noCrop, preferOffsets, tc.stretch, m, plan.Placement)
				}
				if !plan.Placement.ValidStretch() || math.Abs(plan.Placement.Scale*plan.Placement.VerticalStretch()-tc.stretch) > 1e-9 {
					t.Errorf("matrix and placement disagree: %+v", plan)
				}
				if err := plan.Validate(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestPairedSidesReportUnsafeHorizontalOnlyCorrection(t *testing.T) {
	for _, tc := range []struct {
		width, height int
		scale         float64
	}{{700, 1200, 1}, {945, 1890, 1.2}} {
		for _, noCrop := range []bool{true, false} {
			for _, preferOffsets := range []bool{false, true} {
				req := psx.RenderRequest{Fit: psx.FitAuto, NoCrop: noCrop, PreferOffsets: preferOffsets, AnchorX: 2, AnchorY: 1}
				if _, err := Plan(patternFile(t, solid(tc.width, tc.height)), filepath.Join(t.TempDir(), "plan.json"), req, tc.scale); err == nil || !strings.Contains(err.Error(), "超出允许范围") {
					t.Errorf("NoCrop=%t PreferOffsets=%t size=%dx%d scale=%g missing explicit safe-ratio conflict: %v", noCrop, preferOffsets, tc.width, tc.height, tc.scale, err)
				}
			}
		}
	}
}

func TestDirectPlanAppliesAdvertisedStretchEndpointsExactly(t *testing.T) {
	for _, ratio := range []float64{.90, 1.12} {
		for _, noCrop := range []bool{true, false} {
			req := psx.RenderRequest{Fit: psx.FitAuto, NoCrop: noCrop, IgnoreCutAnchors: true, StretchY: ratio}
			plan, err := Plan(patternFile(t, solid(101, 203)), filepath.Join(t.TempDir(), "plan.json"), req, 1)
			if err != nil {
				t.Errorf("ratio=%g NoCrop=%t: %v", ratio, noCrop, err)
				continue
			}
			if math.Abs(plan.Transform[0]-1) > 1e-12 || math.Abs(plan.Transform[3]-ratio) > 1e-12 || math.Abs(plan.Placement.VerticalStretch()-ratio) > 1e-12 || !plan.Verified {
				t.Errorf("stretch rounded or changed horizontal scale: ratio=%g transform=%v placement=%+v", ratio, plan.Transform, plan.Placement)
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
		}
	}
}
