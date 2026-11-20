package printcanvas

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"titao/internal/imgutil"
	"titao/internal/psx"
)

// Plan freezes alpha content and one transform; it never renders a padding canvas.
func Plan(pattern, output string, req psx.RenderRequest, scale float64) (*psx.PrintCanvas, error) {
	if strings.ToLower(filepath.Ext(output)) != ".json" {
		return nil, fmt.Errorf("direct artwork plan must be a JSON manifest")
	}
	sourceAbs, err := filepath.Abs(pattern)
	if err != nil {
		return nil, err
	}
	outAbs, err := filepath.Abs(output)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(sourceAbs, outAbs) {
		return nil, fmt.Errorf("artwork plan must not overwrite its source")
	}
	sourceInfo, err := os.Stat(pattern)
	if err != nil {
		return nil, err
	}
	if target, err := os.Stat(output); err == nil && os.SameFile(sourceInfo, target) {
		return nil, fmt.Errorf("artwork plan must not overwrite its source")
	}
	data, err := os.ReadFile(pattern)
	if err != nil {
		return nil, err
	}
	originalSHA := sum(data)
	im, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	snapshot, box, err := contentPNG(im)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return nil, err
	}
	sourcePath := filepath.Join(filepath.Dir(output), "artwork.png")
	snapshotAbs, _ := filepath.Abs(sourcePath)
	target, _ := os.Stat(sourcePath)
	if strings.EqualFold(sourceAbs, snapshotAbs) || (target != nil && os.SameFile(sourceInfo, target)) {
		return nil, fmt.Errorf("artwork snapshot must not overwrite its source")
	}
	if !req.IgnoreCutAnchors && (req.AnchorX == 0 || req.AnchorY == 0) {
		f, err := os.CreateTemp(filepath.Dir(output), ".artwork-detection-*.png")
		if err != nil {
			return nil, err
		}
		path := f.Name()
		if err = f.Close(); err != nil {
			return nil, err
		}
		defer os.Remove(path)
		detected, err := imgutil.Normalize(pattern, path, false)
		if err != nil {
			return nil, err
		}
		if req.AnchorX == 0 {
			req.AnchorX = detected.AnchorX
		}
		if req.AnchorY == 0 {
			req.AnchorY = detected.AnchorY
		}
	}
	current, err := os.ReadFile(pattern)
	if err != nil {
		return nil, err
	}
	if sum(current) != originalSHA {
		return nil, fmt.Errorf("artwork source changed while extracting content")
	}
	if err = os.WriteFile(sourcePath, snapshot, 0600); err != nil {
		return nil, err
	}
	// Coordinates and geometry now refer to the exact alpha>0 content rectangle.
	req.ContentBounds = [4]float64{0, 0, 1, 1}
	c, _, err := layout(sourcePath, req, scale, true)
	if err != nil {
		return nil, err
	}
	c.Kind, c.Path, c.SourceFile = psx.DirectArtwork, output, "artwork.png"
	c.OriginalSHA256, c.CropBox = originalSHA, [4]int{box.Min.X, box.Min.Y, box.Max.X, box.Max.Y}
	manifest, err := c.Manifest()
	if err != nil {
		return nil, err
	}
	c.SHA256 = sum(manifest)
	if err := os.WriteFile(output, manifest, 0600); err != nil {
		return nil, err
	}
	return c, c.Validate()
}

func contentPNG(im image.Image) ([]byte, image.Rectangle, error) {
	box := image.Rectangle{}
	for y := im.Bounds().Min.Y; y < im.Bounds().Max.Y; y++ {
		for x := im.Bounds().Min.X; x < im.Bounds().Max.X; x++ {
			_, _, _, a := im.At(x, y).RGBA()
			if a != 0 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	if box.Empty() {
		return nil, box, fmt.Errorf("empty artwork content")
	}
	encoded, err := encodeCropPNG(im, box)
	return encoded, box, err
}

func encodeCropPNG(im image.Image, box image.Rectangle) ([]byte, error) {
	var crop image.Image
	if sub, ok := im.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		crop = sub.SubImage(box)
	} else {
		dst := image.NewNRGBA64(image.Rect(0, 0, box.Dx(), box.Dy()))
		draw.Draw(dst, dst.Bounds(), im, box.Min, draw.Src)
		crop = dst
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, crop); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}
