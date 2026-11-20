package psx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
)

const DirectArtwork = "direct_artwork_v1"

func (c *PrintCanvas) IsDirect() bool { return c != nil && c.Kind == DirectArtwork }

func (c *PrintCanvas) SourcePath() string { return filepath.Join(filepath.Dir(c.Path), c.SourceFile) }

// Manifest identity includes source bytes and placement, never a machine-local path.
func (c *PrintCanvas) Manifest() ([]byte, error) {
	copy := *c
	copy.Path, copy.SHA256 = "", ""
	return json.Marshal(copy)
}

func LoadArtworkPlan(path string) (*PrintCanvas, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c PrintCanvas
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	c.Path, c.SHA256 = path, fmt.Sprintf("%x", sha256.Sum256(data))
	if err := c.validateDirect(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *PrintCanvas) validateDirect() error {
	if !c.IsDirect() || c.Width != 945 || c.Height != 1890 || c.SourceFile == "" || filepath.Base(c.SourceFile) != c.SourceFile || filepath.VolumeName(c.SourceFile) != "" || c.SourceFile == "." || c.SourceFile == ".." || c.SourceWidth <= 0 || c.SourceHeight <= 0 {
		return fmt.Errorf("invalid direct artwork manifest")
	}
	original, err := hex.DecodeString(c.OriginalSHA256)
	if err != nil || len(original) != sha256.Size || c.CropBox[0] < 0 || c.CropBox[1] < 0 || c.CropBox[2]-c.CropBox[0] != c.SourceWidth || c.CropBox[3]-c.CropBox[1] != c.SourceHeight {
		return fmt.Errorf("invalid original artwork crop evidence")
	}
	p := c.Placement
	if p.Scale <= 0 || math.IsNaN(p.Scale) || math.IsInf(p.Scale, 0) || math.IsNaN(p.OffsetX) || math.IsInf(p.OffsetX, 0) || math.IsNaN(p.OffsetY) || math.IsInf(p.OffsetY, 0) || !p.ValidStretch() || (p.AnchorImpossible && !p.AnchorUnmet) {
		return fmt.Errorf("invalid direct artwork placement")
	}
	for _, v := range c.Transform {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("invalid direct artwork transform")
		}
	}
	if c.Transform[0] <= 0 || c.Transform[3] <= 0 || c.Transform[1] != 0 || c.Transform[2] != 0 {
		return fmt.Errorf("unsupported direct artwork transform")
	}
	data, err := os.ReadFile(c.Path)
	if err != nil {
		return err
	}
	expected, err := c.Manifest()
	if err != nil || !bytes.Equal(data, expected) || fmt.Sprintf("%x", sha256.Sum256(data)) != c.SHA256 {
		return fmt.Errorf("direct artwork manifest SHA256 mismatch")
	}
	source, err := os.ReadFile(c.SourcePath())
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(source)) != c.SourceSHA256 {
		return fmt.Errorf("direct artwork source SHA256 mismatch")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(source))
	if err != nil || config.Width != c.SourceWidth || config.Height != c.SourceHeight {
		return fmt.Errorf("direct artwork source dimensions mismatch")
	}
	return nil
}
