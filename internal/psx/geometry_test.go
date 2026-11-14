package psx

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"os"
	"testing"
)

func TestArtworkHullIncludesFaintPixelOuterCorners(t *testing.T) {
	im := image.NewNRGBA(image.Rect(10, 20, 110, 120))
	im.SetNRGBA(25, 40, color.NRGBA{A: 1})
	im.SetNRGBA(85, 90, color.NRGBA{A: 255})
	hull := ArtworkHull(im)
	for _, p := range []Point{{.15, .20}, {.16, .21}, {.75, .70}, {.76, .71}} {
		if !geometryTestInside(hull, p, 1e-10) {
			t.Fatalf("faint pixel corner %v missing from hull %v", p, hull)
		}
	}
	if got := ArtworkHull(image.NewNRGBA(image.Rect(0, 0, 2, 2))); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestFitArtworkKeepsRequestedPlacementWhenSafe(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, Cell: 3}
	h := []Point{{.3, .2}, {.7, .2}, {.7, .8}, {.3, .8}}
	p, err := FitArtwork(a, h, 800, 800, 1000, 1000, 1.2, .02, -.03, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Scale != 1.2 || math.Abs(p.OffsetX-.02) > 1e-9 || math.Abs(p.OffsetY+.03) > 1e-9 || p.Constrained || p.Cell != 3 {
		t.Fatalf("safe request changed: %+v", p)
	}
}

func TestFitArtworkMovesBothAxesBeforeShrinking(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.9, .9}, {.1, .9}}}
	h := []Point{{.2, .2}, {.8, .2}, {.8, .8}, {.2, .8}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, 1, .2, .2, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(p.Scale-1) > 1e-9 || math.Abs(p.OffsetX-.1) > 1e-7 || math.Abs(p.OffsetY-.1) > 1e-7 || !p.Constrained {
		t.Fatalf("want scale 1 offset (.1,.1), got %+v", p)
	}
}

func TestFitArtworkPreferOffsetsShrinksToKeepRequestedMove(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.9, .9}, {.1, .9}}}
	h := []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, .8, 0, .1, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(p.Scale-.6) > 1e-10 || math.Abs(p.OffsetX) > 1e-10 || math.Abs(p.OffsetY-.1) > 1e-10 || !p.Constrained {
		t.Fatalf("wanted scale .6 and requested move (0,.1): %+v", p)
	}
	geometryTestPlacement(t, a, h, p, 800, 800, 800, 800)
}

func TestFitArtworkPreferOffsetsPreservesSafeFallbackAndAnchors(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.9, .9}, {.1, .9}}}
	h := []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	for _, tc := range []struct {
		name             string
		x, y             float64
		anchorX, anchorY int
	}{
		{"requested position never fits", .7, 0, 0, 0},
		{"single anchor retains priority", .1, .1, 1, 0},
		{"paired anchor retains priority", .1, .1, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := FitArtwork(a, h, 800, 800, 800, 800, .8, tc.x, tc.y, tc.anchorX, tc.anchorY)
			if err != nil {
				t.Fatal(err)
			}
			got, err := FitArtwork(a, h, 800, 800, 800, 800, .8, tc.x, tc.y, tc.anchorX, tc.anchorY, true)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("legacy placement changed: got %+v want %+v", got, want)
			}
			geometryTestPlacement(t, a, h, got, 800, 800, 800, 800)
		})
	}
}

func TestFitArtworkPreferOffsetsHandlesNegativeSupportLowerBound(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.4, .1}, {.4, .9}, {.1, .9}}}
	h := []Point{{.1, .4}, {.3, .4}, {.3, .6}, {.1, .6}}
	// At requested XY, only scales in [.5,1] fit; scale zero is outside.
	p, err := FitArtwork(a, h, 800, 800, 800, 800, 2, 0, 0, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(p.Scale-1) > 1e-10 || math.Abs(p.OffsetX) > 1e-10 || math.Abs(p.OffsetY) > 1e-10 {
		t.Fatalf("lost positive lower-bound interval: %+v", p)
	}
	geometryTestPlacement(t, a, h, p, 800, 800, 800, 800)
	want, err := FitArtwork(a, h, 800, 800, 800, 800, .25, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FitArtwork(a, h, 800, 800, 800, 800, .25, 0, 0, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || got.OffsetX >= 0 {
		t.Fatalf("scale below positive lower bound must safely fall back: %+v want %+v", got, want)
	}
}

func TestFitArtworkDefaultStillPrefersScaleOverOffsets(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.9, .9}, {.1, .9}}}
	h := []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	want, err := FitArtwork(a, h, 800, 800, 800, 800, .8, 0, .1, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FitArtwork(a, h, 800, 800, 800, 800, .8, 0, .1, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || want.Scale != .8 || math.Abs(want.OffsetY) > 1e-10 {
		t.Fatalf("default changed: %+v explicit false: %+v", want, got)
	}
}

func TestFitArtworkPreferOffsetsRejectsImpossibleZeroSupport(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.6, .1}, {.9, .1}, {.9, .9}, {.6, .9}}}
	h := []Point{{.5, .4}, {.7, .4}, {.7, .6}, {.5, .6}}
	// The left content edge remains at x=.5 for every scale at requested XY.
	want, err := FitArtwork(a, h, 800, 800, 800, 800, .25, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FitArtwork(a, h, 800, 800, 800, 800, .25, 0, 0, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || got.OffsetX <= 0 {
		t.Fatalf("zero-support inequality must fall back: got %+v want %+v", got, want)
	}
	geometryTestPlacement(t, a, h, got, 800, 800, 800, 800)
}

func TestFitArtworkShrinksToMaximumUniformScale(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .2}, {.9, .2}, {.9, .8}, {.1, .8}}}
	h := []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, 2, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(p.Scale-.6) > 1e-7 || !p.Constrained {
		t.Fatalf("want max scale .6, got %+v", p)
	}
	geometryTestPlacement(t, a, h, p, 800, 800, 800, 800)
}

func TestFitArtworkTransparentPaddingMayLeaveVisibleArea(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.2, .2}, {.8, .2}, {.8, .8}, {.2, .8}}}
	h := []Point{{.4, .4}, {.6, .4}, {.6, .6}, {.4, .6}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, 2.5, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Scale != 2.5 {
		t.Fatalf("transparent canvas was constrained: %+v", p)
	}
	geometryTestPlacement(t, a, h, p, 800, 800, 800, 800)
}

func TestFitArtworkNoCropWinsOverDoubleEdgeAnchor(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.8, .9}, {.2, .9}}}
	h := []Point{{.3, .3}, {.7, .3}, {.7, .7}, {.3, .7}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, 1, .4, 0, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Constrained || !p.AnchorUnmet || p.Scale > 1 {
		t.Fatalf("impossible double edge was claimed satisfied: %+v", p)
	}
	geometryTestPlacement(t, a, h, p, 800, 800, 800, 800)
}

func TestFitArtworkSingleEdgeAnchorTouchesSafeBoundary(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.9, .9}, {.1, .9}}}
	h := []Point{{.2, .3}, {.8, .3}, {.8, .7}, {.2, .7}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, 1, 0, 0, -1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(p.OffsetX+.1) > 1e-7 || math.Abs(p.OffsetY-.2) > 1e-7 {
		t.Fatalf("anchors did not meet safe edges: %+v", p)
	}
	geometryTestPlacement(t, a, h, p, 800, 800, 800, 800)
}

func TestFitArtworkSingleAnchorMovesOtherAxisInsteadOfLosingContact(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.9, .5}, {.1, .9}}}
	h := []Point{{.4, .4}, {.6, .4}, {.6, .6}, {.4, .6}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, 1, 0, .3, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(p.OffsetX-.3) > 1e-7 || p.OffsetY > -.1+1e-7 || p.AnchorUnmet || p.AnchorImpossible || p.Scale != 1 {
		t.Fatalf("right-edge anchor must move Y while preserving scale and content: %+v", p)
	}
	geometryTestPlacement(t, a, h, p, 800, 800, 800, 800)
}

func TestFitArtworkUVToleranceAcceptsNearPairedEdgesWithoutCropping(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.899, .9}, {.101, .9}}, EdgeTolerance: 1.0 / 255}
	h := []Point{{.3, .45}, {.7, .45}, {.7, .55}, {.3, .55}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, 2, 0, 0, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.AnchorUnmet || p.AnchorImpossible {
		t.Fatalf("sub-bin side drift is visually attached: %+v", p)
	}
	gap := .5 + p.OffsetX + (.3-.5)*p.Scale - .1
	if gap <= 1e-7 || gap > a.EdgeTolerance {
		t.Fatalf("fixture needs a real sub-bin gap, got %.12g", gap)
	}
	for _, v := range h {
		q := Point{.5 + p.OffsetX + (v[0]-.5)*p.Scale, .5 + p.OffsetY + (v[1]-.5)*p.Scale}
		if !geometryTestInside(a.Polygon, q, 1e-12) {
			t.Fatalf("anchor tolerance expanded the hard content boundary: %v at %+v", q, p)
		}
	}
}

func TestFitArtworkUVToleranceKeepsApproximateGlobalCertificateConservative(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.899, .9}, {.101, .9}}, EdgeTolerance: 1.0 / 255}
	h := []Point{{.3, .45}, {.7, .45}, {.7, .55}, {.3, .55}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, .5, 0, 0, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !p.AnchorUnmet || p.AnchorImpossible || p.Scale != .5 {
		t.Fatalf("small current request must not prove approximate anchors impossible: %+v", p)
	}
	a.Polygon = []Point{{.1, .2}, {.9, .2}, {.9, .8}, {.1, .8}}
	h = []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	p, err = FitArtwork(a, h, 800, 800, 800, 800, 1, 0, 0, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !p.AnchorUnmet || !p.AnchorImpossible {
		t.Fatalf("full-height content truly prevents visual paired width: %+v", p)
	}
	geometryTestPlacement(t, a, h, p, 800, 800, 800, 800)
}

func TestFitArtworkLaterPairedProposalRetainsMinimumSpan(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.899, .9}, {.101, .9}}, EdgeTolerance: 1.0 / 255}
	h := []Point{{.3, .45}, {.7, .45}, {.7, .55}, {.3, .55}}
	minimum := (.8 - 2.0/255) / .4
	p, err := FitArtwork(a, h, 800, 800, 800, 800, .5, 0, 0, 2, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.Scale < minimum-1e-10 || !p.Constrained || p.AnchorUnmet || p.AnchorImpossible {
		t.Fatalf("later paired proposal became a floating sticker: minimum %.12g, got %+v", minimum, p)
	}
	geometryTestPlacement(t, a, h, p, 800, 800, 800, 800)
	initial, err := FitArtwork(a, h, 800, 800, 800, 800, .5, 0, 0, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	manual, err := FitArtwork(a, h, 800, 800, 800, 800, .5, 0, 0, 2, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if initial != manual || initial.Scale != .5 {
		t.Fatalf("initial/manual behavior changed: %+v %+v", initial, manual)
	}
	a.Polygon = []Point{{.1, .2}, {.9, .2}, {.9, .8}, {.1, .8}}
	h = []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	blocked, err := FitArtwork(a, h, 800, 800, 800, 800, .2, 0, 0, 2, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Scale > .6+1e-12 || !blocked.Constrained || !blocked.AnchorUnmet || !blocked.AnchorImpossible {
		t.Fatalf("paired minimum must not override hard NoCrop or fake contact: %+v", blocked)
	}
	geometryTestPlacement(t, a, h, blocked, 800, 800, 800, 800)
}

func TestFitArtworkUVToleranceStillPrefersNumericalContact(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.1, .1}, {.9, .1}, {.9, .9}, {.1, .9}}, EdgeTolerance: 1.0 / 255}
	h := []Point{{.4, .4}, {.6, .4}, {.6, .6}, {.4, .6}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, 1, 0, 0, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(p.OffsetX-.3) > 1e-7+1e-12 || p.AnchorUnmet {
		t.Fatalf("visual tolerance replaced available exact contact: %+v", p)
	}
}

func TestFitArtworkPairedFloorRetriesULPContact(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{0.032128922188293184, .1}, {0.86754433563997813, .1}, {0.86754433563997813, .9}, {0.032128922188293184, .9}}, EdgeTolerance: 1.0 / 255}
	h := []Point{{0.28674088072410636, .48}, {0.83697970746352701, .48}, {0.83697970746352701, .52}, {0.28674088072410636, .52}}
	p, err := FitArtwork(a, h, 800, 800, 800, 800, .2, .1, .1, 2, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.AnchorUnmet || p.AnchorImpossible || !p.Constrained {
		t.Fatalf("paired floor needs a rounding-safe contact: %+v", p)
	}
	geometryTestPlacement(t, a, h, p, 800, 800, 800, 800)
}

func TestFitArtworkPairedFloorRandomRectanglesStayAttachedAndInside(t *testing.T) {
	rng := rand.New(rand.NewSource(20261114))
	for i := 0; i < 2000; i++ {
		left, right := .01+rng.Float64()*.2, .75+rng.Float64()*.24
		u0, u1 := .05+rng.Float64()*.35, .6+rng.Float64()*.35
		a := VisibleArea{Polygon: []Point{{left, .1}, {right, .1}, {right, .9}, {left, .9}}, EdgeTolerance: 1.0 / 255}
		h := []Point{{u0, .48}, {u1, .48}, {u1, .52}, {u0, .52}}
		anchorX, anchorY := 2, 0
		if i%2 != 0 {
			a.Polygon = []Point{{.1, left}, {.9, left}, {.9, right}, {.1, right}}
			h = []Point{{.48, u0}, {.52, u0}, {.52, u1}, {.48, u1}}
			anchorX, anchorY = 0, 2
		}
		p, err := FitArtwork(a, h, 800, 800, 800, 800, .2, rng.Float64()*.4-.2, rng.Float64()*.4-.2, anchorX, anchorY, true)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		minimum := (right - left - 2*a.EdgeTolerance) / (u1 - u0)
		if p.AnchorUnmet || p.AnchorImpossible || p.Scale < minimum-1e-12 || p.Scale > minimum+1e-10 {
			t.Fatalf("case %d has a legal paired contact at floor %.17g, got %+v area=%v hull=%v", i, minimum, p, a.Polygon, h)
		}
		for _, v := range h {
			q := Point{.5 + p.OffsetX + (v[0]-.5)*p.Scale, .5 + p.OffsetY + (v[1]-.5)*p.Scale}
			if !geometryTestInside(a.Polygon, q, 1e-12) {
				t.Fatalf("case %d content escaped strict NoCrop: %v at %+v", i, q, p)
			}
		}
	}
}

func TestFitArtworkAnchorImpossibleRequiresGlobalPairedAnchorConflict(t *testing.T) {
	for _, tc := range []struct {
		name       string
		area, hull []Point
		scale      float64
		x, y       int
		impossible bool
	}{
		{"small request can enlarge to both edges", []Point{{.1, .1}, {.9, .1}, {.9, .9}, {.1, .9}}, []Point{{.3, .3}, {.7, .3}, {.7, .7}, {.3, .7}}, .5, 2, 0, false},
		{"full content height prevents both width edges", []Point{{.1, .2}, {.9, .2}, {.9, .8}, {.1, .8}}, []Point{{.3, .1}, {.7, .1}, {.7, .9}, {.3, .9}}, .5, 2, 0, true},
		{"rectangle exactly matches both axes", []Point{{.1, .1}, {.9, .1}, {.9, .9}, {.1, .9}}, []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, 1, 2, 2, false},
		{"double axis ratios conflict", []Point{{.1, .2}, {.9, .2}, {.9, .8}, {.1, .8}}, []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, .5, 2, 2, true},
		{"decimal rectangle exact boundary", []Point{{.13, .21}, {.69, .21}, {.69, .63}, {.13, .63}}, []Point{{.1, .2}, {.9, .2}, {.9, .8}, {.1, .8}}, 1, 2, 2, false},
		{"single anchors never assert global impossibility", []Point{{.1, .1}, {.9, .1}, {.8, .9}, {.2, .9}}, []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, 1, -1, 1, false},
		{"paired edges fit with other axis free", []Point{{.5, 0}, {1, .5}, {.5, 1}, {0, .5}}, []Point{{0, .5}, {1, .5}, {.5, 0}}, .5, 2, 0, false},
		{"paired edges conflict with other requested single edge", []Point{{.5, 0}, {1, .5}, {.5, 1}, {0, .5}}, []Point{{0, .5}, {1, .5}, {.5, 0}}, .5, 2, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := VisibleArea{Polygon: tc.area}
			got, err := FitArtwork(a, tc.hull, 800, 800, 800, 800, tc.scale, 0, 0, tc.x, tc.y)
			if err != nil {
				t.Fatal(err)
			}
			if got.AnchorImpossible != tc.impossible {
				t.Fatalf("AnchorImpossible=%v want=%v: %+v", got.AnchorImpossible, tc.impossible, got)
			}
			geometryTestPlacement(t, a, tc.hull, got, 800, 800, 800, 800)
			if tc.name == "small request can enlarge to both edges" && !got.AnchorUnmet {
				t.Fatal("fixture must remain unmet at current small requested scale")
			}
		})
	}
}

func TestFitArtworkArbitraryContentNeverCrossesSlantedRegion(t *testing.T) {
	a := VisibleArea{Polygon: []Point{{.15, .1}, {.85, .2}, {.9, .75}, {.3, .95}, {.05, .5}}}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 80; i++ {
		im := image.NewNRGBA(image.Rect(0, 0, 53, 91))
		for j := 0; j < 100; j++ {
			im.SetNRGBA(rng.Intn(53), rng.Intn(91), color.NRGBA{A: uint8(1 + rng.Intn(255))})
		}
		h := ArtworkHull(im)
		p, err := FitArtwork(a, h, 600, 900, 53, 91, .1+rng.Float64()*4, rng.Float64()-.5, rng.Float64()-.5, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		geometryTestPlacement(t, a, h, p, 600, 900, 53, 91)
		// Check original pixels as well, so a hull omission cannot hide a crop.
		for y := 0; y < 91; y++ {
			for x := 0; x < 53; x++ {
				if im.NRGBAAt(x, y).A == 0 {
					continue
				}
				geometryTestPlacement(t, a, []Point{{float64(x) / 53, float64(y) / 91}, {float64(x+1) / 53, float64(y) / 91}, {float64(x+1) / 53, float64(y+1) / 91}, {float64(x) / 53, float64(y+1) / 91}}, p, 600, 900, 53, 91)
			}
		}
	}
}

func TestCalibrateVisibleAreasPreservesPhotoBottomCrop(t *testing.T) {
	h := [9]float64{.7, 0, .15, 0, 1.05, .15, 0, 0, 1}
	a, err := CalibrateVisibleAreas(geometryTestUV(h, 400), 400)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 {
		t.Fatal(a)
	}
	maxV := 0.0
	for _, p := range a[0].Polygon {
		maxV = math.Max(maxV, p[1])
		q := geometryTestProject(h, p)
		if q[1] > .995 {
			t.Fatalf("photo bottom was recovered outside frame: %v => %v", p, q)
		}
	}
	if maxV < .77 || maxV > .81 {
		t.Fatalf("wrong cropped v extent %.6f", maxV)
	}
}

func TestCalibrateVisibleAreasHandlesRotationPerspective(t *testing.T) {
	h := [9]float64{.72, -.22, .28, .20, .82, .10, .18, -.10, 1}
	a, err := CalibrateVisibleAreas(geometryTestUV(h, 420), 420)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || len(a[0].Polygon) < 3 {
		t.Fatal(a)
	}
	if a[0].EdgeTolerance != 1.0/255 {
		t.Fatalf("calibrated anchor tolerance must match one UV bin: %v", a[0].EdgeTolerance)
	}
	for _, p := range a[0].Polygon {
		q := geometryTestProject(h, p)
		if q[0] < .002 || q[1] < .002 || q[0] > .998 || q[1] > .998 {
			t.Fatalf("projected hull outside safe photo: %v => %v", p, q)
		}
	}
	if !geometryTestInside(a[0].Polygon, Point{.5, .5}, 1e-9) {
		t.Fatal("center missing", a)
	}
}

func TestCalibrateVisibleAreasIgnoresFeatheredUVForHomography(t *testing.T) {
	h := [9]float64{.7, 0, .15, 0, .8, .1, 0, 0, 1}
	im := geometryTestUV(h, 400)
	// Composited feather colors do not encode a trustworthy source UV.
	for y := 40; y < 360; y++ {
		im.SetNRGBA(60, y, color.NRGBA{R: 16, G: uint8((y - 40) * 255 / 320), B: 129, A: 230})
	}
	areas, err := CalibrateVisibleAreas(im, 400)
	if err != nil {
		t.Fatal(err)
	}
	if !geometryTestInside(areas[0].Polygon, Point{.5, .5}, 1e-9) {
		t.Fatal("core visible area missing")
	}
}

func TestCalibrateVisibleAreasRejectsNonProjectiveUV(t *testing.T) {
	im := geometryTestUV([9]float64{.7, 0, .15, 0, .8, .1, 0, 0, 1}, 400)
	for y := 0; y < 400; y++ {
		for x := 0; x < 400; x++ {
			c := im.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			r := float64(c.R) + 14*math.Sin(float64(c.G)*math.Pi/64)
			c.R = uint8(math.Max(0, math.Min(255, r)))
			im.SetNRGBA(x, y, c)
		}
	}
	if _, err := CalibrateVisibleAreas(im, 400); err == nil {
		t.Fatal("non-projective UV distortion was reported calibrated")
	}
}

func TestCalibrateVisibleAreasAcceptsOneBlueQuantizationStep(t *testing.T) {
	for _, blue := range []uint8{127, 129} {
		im := geometryTestUV([9]float64{.7, 0, .15, 0, .8, .1, 0, 0, 1}, 200)
		for y := 0; y < 200; y++ {
			for x := 0; x < 200; x++ {
				c := im.NRGBAAt(x, y)
				if c.A > 0 {
					c.B = blue
					im.SetNRGBA(x, y, c)
				}
			}
		}
		if _, err := CalibrateVisibleAreas(im, 200); err != nil {
			t.Fatalf("valid B=%d UV rejected: %v", blue, err)
		}
	}
}

func TestCalibrateVisibleAreasConstrainsActualMaskDespiteFeatheredUV(t *testing.T) {
	h := [9]float64{.7, 0, .15, 0, .8, .1, 0, 0, 1}
	im := geometryTestUV(h, 400)
	for y := 0; y < 400; y++ {
		for x := 256; x < 400; x++ {
			im.SetNRGBA(x, y, color.NRGBA{})
		}
		c := im.NRGBAAt(255, y)
		if c.A > 0 {
			c.R = 230
			c.A = 230
			c.B = 129
			im.SetNRGBA(255, y, c)
		}
	}
	areas, err := CalibrateVisibleAreas(im, 400)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range areas[0].Polygon {
		q := geometryTestProject(h, p)
		if q[0]*400 > 252 {
			t.Fatalf("UV colors expanded source beyond actual shell mask: %v => %vpx", p, q[0]*400)
		}
	}
}

func TestGeometryRejectsUncalibratedOrInvalidInputs(t *testing.T) {
	if _, err := CalibrateVisibleAreas(image.NewNRGBA(image.Rect(0, 0, 100, 100)), 100); err == nil {
		t.Fatal("empty calibration accepted")
	}
	if _, err := CalibrateVisibleAreas(image.NewNRGBA(image.Rect(0, 0, 100, 101)), 100); err == nil {
		t.Fatal("partial cell accepted")
	}
	a := VisibleArea{Polygon: []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}}
	h := []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}
	for _, v := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := FitArtwork(a, h, 100, 100, 100, 100, v, 0, 0, 0, 0); err == nil {
			t.Fatalf("invalid scale %v accepted", v)
		}
	}
	for _, tolerance := range []float64{-1, math.NaN(), math.Inf(1)} {
		a.EdgeTolerance = tolerance
		if _, err := FitArtwork(a, h, 100, 100, 100, 100, 1, 0, 0, 0, 0); err == nil {
			t.Fatalf("invalid anchor tolerance %v accepted", tolerance)
		}
	}
	a.EdgeTolerance = 0
	if _, err := FitArtwork(a, nil, 100, 100, 100, 100, 1, 0, 0, 0, 0); err == nil {
		t.Fatal("empty artwork accepted")
	}
	a.Polygon = []Point{{0, 0}, {1, 0}, {.4, .4}, {1, 1}, {0, 1}}
	if _, err := FitArtwork(a, h, 100, 100, 100, 100, 1, 0, 0, 0, 0); err == nil {
		t.Fatal("concave visible area accepted")
	}
}

func TestCalibrateRealUVWhenProvided(t *testing.T) {
	path := os.Getenv("TITAO_UV_CALIBRATION")
	if path == "" {
		t.Skip("set TITAO_UV_CALIBRATION to verify actual Photoshop UV export")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	areas, err := CalibrateVisibleAreas(im, 800)
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 7 {
		t.Fatalf("expected 7 cells, got %d", len(areas))
	}
	for _, a := range areas {
		min, max := geometryBounds(a.Polygon)
		t.Logf("cell=%d vertices=%d min=%v max=%v", a.Cell, len(a.Polygon), min, max)
		if a.Cell == 2 && max[1] > .96 {
			t.Fatal("03 photo-frame bottom crop was lost")
		}
		p, err := FitArtwork(a, []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, 750, 1500, 750, 1500, 1, 0, 0, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		geometryTestPlacement(t, a, []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, p, 750, 1500, 750, 1500)
	}
}

func geometryTestInside(poly []Point, p Point, epsilon float64) bool {
	if len(poly) < 3 {
		return false
	}
	for i, a := range poly {
		b := poly[(i+1)%len(poly)]
		if (b[0]-a[0])*(p[1]-a[1])-(b[1]-a[1])*(p[0]-a[0]) < -epsilon {
			return false
		}
	}
	return true
}

func geometryTestPlacement(t *testing.T, a VisibleArea, h []Point, p AppliedPlacement, cw, ch, pw, ph int) {
	t.Helper()
	base := math.Min(float64(cw)/float64(pw), float64(ch)/float64(ph))
	for _, v := range h {
		q := Point{.5 + p.OffsetX + (v[0]-.5)*float64(pw)*base*p.Scale/float64(cw), .5 + p.OffsetY + (v[1]-.5)*float64(ph)*base*p.Scale/float64(ch)}
		if !geometryTestInside(a.Polygon, q, 1e-8) {
			t.Fatalf("content %v escaped visible polygon at %v, placement %+v", v, q, p)
		}
	}
}

func geometryTestProject(h [9]float64, p Point) Point {
	d := h[6]*p[0] + h[7]*p[1] + h[8]
	return Point{(h[0]*p[0] + h[1]*p[1] + h[2]) / d, (h[3]*p[0] + h[4]*p[1] + h[5]) / d}
}

func geometryTestUV(h [9]float64, size int) *image.NRGBA {
	a, b, c, d, e, f, g, k, l := h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7], h[8]
	inv := [9]float64{e*l - f*k, c*k - b*l, b*f - c*e, f*g - d*l, a*l - c*g, c*d - a*f, d*k - e*g, b*g - a*k, a*e - b*d}
	im := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			uv := geometryTestProject(inv, Point{(float64(x) + .5) / float64(size), (float64(y) + .5) / float64(size)})
			if uv[0] >= 0 && uv[0] <= 1 && uv[1] >= 0 && uv[1] <= 1 {
				im.SetNRGBA(x, y, color.NRGBA{R: uint8(math.Round(uv[0] * 255)), G: uint8(math.Round(uv[1] * 255)), B: 128, A: 255})
			}
		}
	}
	return im
}
