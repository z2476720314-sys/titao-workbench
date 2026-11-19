package imgutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewQuarterTurnsPreserveEverySourcePixel(t *testing.T) {
	im := image.NewNRGBA(image.Rect(7, 9, 9, 12))
	for y := 0; y < 3; y++ {
		for x := 0; x < 2; x++ {
			im.SetNRGBA(7+x, 9+y, color.NRGBA{R: uint8(y*2 + x + 1), G: 50, A: 128})
		}
	}
	for turns, want := range [][]uint8{{1, 2, 3, 4, 5, 6}, {5, 3, 1, 6, 4, 2}, {6, 5, 4, 3, 2, 1}, {2, 4, 6, 1, 3, 5}} {
		got := rotateReviewQuarter(im, turns)
		n := 0
		for y := 0; y < got.Bounds().Dy(); y++ {
			for x := 0; x < got.Bounds().Dx(); x++ {
				p := got.NRGBAAt(x, y)
				if p.R != want[n] || p.A != 128 || p.G != 50 {
					t.Fatalf("turn%d pixel%d=%+v wantR%d", turns, n, p, want[n])
				}
				n++
			}
		}
	}
}

func TestReviewRotationDoesNotReencodeUprightInput(t *testing.T) {
	input := []byte("already oriented fixture")
	got, err := ReviewUprightJPEG(input, 0)
	if err != nil || !bytes.Equal(got, input) {
		t.Fatal("upright input changed")
	}
	if _, err := ReviewUprightJPEG(input, 4); err == nil {
		t.Fatal("invalid rotation accepted")
	}
	if _, err := ReviewUprightJPEG(input, 1); err == nil {
		t.Fatal("invalid JPEG accepted")
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewNRGBA(image.Rect(0, 0, 2, 3)), nil); err != nil {
		t.Fatal(err)
	}
	rotated, err := ReviewUprightJPEG(encoded.Bytes(), 1)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(rotated))
	if err != nil || cfg.Width != 3 || cfg.Height != 2 {
		t.Fatalf("rotation lost frame: %+v %v", cfg, err)
	}
}

func TestLiveReviewOrientationImages(t *testing.T) {
	directions, source, output := os.Getenv("TITAO_REVIEW_DIRECTIONS"), os.Getenv("TITAO_REVIEW_IMAGES"), os.Getenv("TITAO_REVIEW_OUTPUT")
	if directions == "" || source == "" || output == "" {
		t.Skip("live review fixtures not configured")
	}
	files, err := filepath.Glob(filepath.Join(directions, "*.orientation.json"))
	if err != nil || len(files) == 0 {
		t.Fatal("missing directions")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var turns map[int]int
		if err := json.Unmarshal(data, &turns); err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(filepath.Base(path), ".orientation.json")
		for cell, n := range turns {
			photo := fmt.Sprintf("%s_diagnostic_%02d.jpg", name, cell+1)
			original, err := os.ReadFile(filepath.Join(source, photo))
			if err != nil {
				t.Fatal(err)
			}
			upright, err := ReviewUprightJPEG(original, n)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(output, photo), upright, 0600); err != nil {
				t.Fatal(err)
			}
			count++
		}
	}
	t.Logf("checked %d printed review photos", count)
	if count != 23 {
		t.Fatal("unexpected printed photo coverage")
	}
}
