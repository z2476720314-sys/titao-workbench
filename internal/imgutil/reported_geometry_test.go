package imgutil

import (
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func TestReportedThreePatternGeometry(t *testing.T) {
	root := os.Getenv("TITAO_THREE_SOURCES")
	if root == "" {
		t.Skip("reported source fixtures not configured")
	}
	for _, name := range []string{"Кот из@WMTAI5966", "МАИСОН МИСФИТ@WMTAI273", "Милый Доль@WMTAI6144"} {
		t.Run(name, func(t *testing.T) {
			source := filepath.Join(root, name+".png")
			pr, err := Normalize(source, filepath.Join(t.TempDir(), "normalized.png"), false)
			if err != nil {
				t.Fatal(err)
			}
			im, err := Decode(source)
			if err != nil {
				t.Fatal(err)
			}
			runs := [2]int{}
			for side, x := range []int{0, im.Bounds().Dx() - 1} {
				run := 0
				for y := 0; y < im.Bounds().Dy(); y++ {
					if color.NRGBAModel.Convert(im.At(x, y)).(color.NRGBA).A >= 8 {
						run++
					} else {
						run = 0
					}
					runs[side] = max(runs[side], run)
				}
			}
			data, err := json.Marshal(struct {
				Pattern  PatternResult
				SideRuns [2]int
			}{pr, runs})
			if err != nil {
				t.Fatal(err)
			}
			t.Log(string(data))
		})
	}
}
