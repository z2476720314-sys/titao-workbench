package psx

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestPhotoshopDirectSourceUsesNativeNormalizedTransformWithoutPaddingPNG(t *testing.T) {
	c := frozenTestCanvas(t)
	original, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatal(err)
	}
	im, err := png.Decode(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	box := image.Rect(35, 80, 435, 980)
	var encoded bytes.Buffer
	if err = png.Encode(&encoded, im.(interface {
		SubImage(image.Rectangle) image.Image
	}).SubImage(box)); err != nil {
		t.Fatal(err)
	}
	c.Kind, c.SourceFile, c.SourceWidth, c.SourceHeight = DirectArtwork, "artwork.png", 400, 900
	c.OriginalSHA256, c.CropBox = c.SHA256, [4]int{35, 80, 435, 980}
	c.Transform = [6]float64{.5, 0, 0, .5, 100, 200}
	c.SourceSHA256 = fmt.Sprintf("%x", sha256.Sum256(encoded.Bytes()))
	c.Path = filepath.Join(t.TempDir(), "artwork_plan.json")
	if err = os.WriteFile(c.SourcePath(), encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	manifest, err := c.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	c.SHA256 = fmt.Sprintf("%x", sha256.Sum256(manifest))
	if err = os.WriteFile(c.Path, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	req := RenderRequest{Canvas: c, Scales: []float64{1}, NoCrop: true, OutDir: out, CellSize: 800, Group: "art"}
	result := executeFrozenScript(t, req, 946, 1890, "var allowOriginal=true;")
	if err = checkRenderLog(result.Log); err != nil {
		t.Fatal(err)
	}
	want := [4]float64{100 * 946.0 / 945, 200, 300 * 946.0 / 945, 650}
	for i := range want {
		if math.Abs(result.Bounds[i]-want[i]) > 1e-8 {
			t.Fatalf("native direct bounds=%v want=%v", result.Bounds, want)
		}
	}
	if result.OuterChanged || result.Places != 1 || result.Saves != 1 {
		t.Fatalf("shared source or parent projection changed: %+v", result)
	}
	if paths, _ := filepath.Glob(filepath.Join(out, "print_canvas*.png")); len(paths) != 0 {
		t.Fatalf("direct PS baked padding: %v", paths)
	}
	proof, err := LoadArtworkPlan(req.ArtworkPlanPath(0))
	if err != nil || proof.SHA256 != c.SHA256 {
		t.Fatalf("direct staged proof invalid: %v", err)
	}
	var receipt RenderMetadata
	if err = json.Unmarshal([]byte(result.Files[req.MetadataPath(0)]), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ArtifactKind != DirectArtwork || receipt.Transform != c.Transform || receipt.SourceSHA256 != c.SourceSHA256 || receipt.FrameVerified {
		t.Fatalf("dishonest direct proof: %+v", receipt)
	}
}
