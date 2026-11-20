package tuner

import (
	"fmt"
	"math"
	"testing"
)

func TestJointPlanHonorsAdvertisedScaleRangeAndCumulativeFloor(t *testing.T) {
	for _, tc := range []struct{ scale, ratio, want float64 }{
		{1, .55, .55}, {.8, .55, .55}, {.55, .55, .55}, {2, .55, 1.1}, {1, 2, 2},
	} {
		adv := parseAdvice(fmt.Sprintf(`{"good":false,"camera_clear":false,"score":50,"problem":"主字被挡","dx":0,"dy":0,"scale_ratio":%g,"stretch_y":1}`, tc.ratio))
		if adv.Transform == nil {
			t.Fatalf("advertised ratio rejected: %g", tc.ratio)
		}
		scale, _, _, _, iterate := Plan(adv, tc.scale, 0, 0, 1)
		if math.Abs(scale-tc.want) > 1e-12 || iterate != (tc.want != tc.scale) {
			t.Errorf("scale=%g ratio=%g: got=%g iterate=%t want=%g", tc.scale, tc.ratio, scale, iterate, tc.want)
		}
	}
	for _, ratio := range []float64{.05, .54, 2.01} {
		adv := parseAdvice(fmt.Sprintf(`{"good":false,"camera_clear":false,"score":50,"problem":"主字被挡","dx":0,"dy":0,"scale_ratio":%g,"stretch_y":1}`, ratio))
		if adv.Transform != nil {
			t.Errorf("unadvertised ratio accepted: %g", ratio)
		}
	}
}

func TestAdvertisedStretchEndpointsParseApplyAndAccumulateSafely(t *testing.T) {
	for _, tc := range []struct{ ratio, after float64 }{{.90, .90}, {1.12, 1.12}} {
		adv := parseAdvice(fmt.Sprintf(`{"good":false,"camera_clear":false,"score":50,"problem":"顶部补齐","dx":0,"dy":0,"scale_ratio":1,"stretch_y":%g}`, tc.ratio))
		if adv.Transform == nil {
			t.Fatalf("advertised stretch endpoint rejected: %g", tc.ratio)
		}
		stretch := 1.0
		for i := 0; i < 3; i++ {
			_, _, _, stretch, _ = Plan(adv, 1, 0, 0, stretch)
			if math.Abs(stretch-tc.after) > 1e-12 {
				t.Errorf("ratio=%g round=%d cumulative=%g want=%g", tc.ratio, i+1, stretch, tc.after)
			}
		}
	}
}
