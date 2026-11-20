package printcanvas

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"titao/internal/fileutil"
	"titao/internal/imgutil"
	"titao/internal/psx"
)

// MoveToBottom keeps the previous transform except for its y translation.
func MoveToBottom(previous *psx.PrintCanvas, output string) (*psx.PrintCanvas, error) {
	if previous == nil || !previous.IsDirect() || !previous.Verified {
		return nil, fmt.Errorf("bottom translation requires a verified direct artwork plan")
	}
	if err := previous.Validate(); err != nil {
		return nil, err
	}
	if strings.ToLower(filepath.Ext(output)) != ".json" {
		return nil, fmt.Errorf("direct artwork plan must be a JSON manifest")
	}
	c := *previous
	c.Path = output
	oldDir, err := filepath.Abs(filepath.Dir(previous.Path))
	if err != nil {
		return nil, err
	}
	newDir, err := filepath.Abs(filepath.Dir(output))
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(oldDir, newDir) {
		return nil, fmt.Errorf("bottom translation must preserve the previous plan")
	}
	im, err := imgutil.Decode(previous.SourcePath())
	if err != nil {
		return nil, err
	}
	b := im.Bounds()
	visibleBottom := b.Min.Y
	for y := b.Max.Y - 1; y >= b.Min.Y && visibleBottom == b.Min.Y; y-- {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := im.At(x, y).RGBA(); a>>8 >= 8 {
				visibleBottom = y + 1
				break
			}
		}
	}
	if visibleBottom > b.Min.Y && visibleBottom < b.Max.Y {
		b.Max.Y = visibleBottom
		c.SourceHeight = b.Dy()
		c.CropBox[3] = c.CropBox[1] + c.SourceHeight
	}
	bottom := float64(c.Height) - float64(c.SourceHeight)*c.Transform[3]
	if bottom < -1e-6 || bottom < c.Transform[5]-1e-6 || c.Transform[4] < -1e-6 || c.Transform[4]+float64(c.SourceWidth)*c.Transform[0] > float64(c.Width)+1e-6 {
		return nil, fmt.Errorf("cannot move artwork down to bottom without cropping")
	}
	if bottom-c.Transform[5] <= 1e-6 {
		return previous, nil
	}
	c.Transform[5] = bottom
	c.Placement.OffsetY = (bottom+float64(c.SourceHeight)*c.Transform[3]/2)/float64(c.Height) - .5
	if c.SourceHeight == previous.SourceHeight {
		if err := fileutil.Copy(previous.SourcePath(), c.SourcePath()); err != nil {
			return nil, err
		}
	} else {
		encoded, err := encodeCropPNG(im, b)
		if err != nil {
			return nil, err
		}
		c.SourceSHA256 = sum(encoded)
		if err := os.MkdirAll(newDir, 0700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(c.SourcePath(), encoded, 0600); err != nil {
			return nil, err
		}
	}
	manifest, err := c.Manifest()
	if err != nil {
		return nil, err
	}
	c.SHA256 = sum(manifest)
	if err := os.WriteFile(output, manifest, 0600); err != nil {
		return nil, err
	}
	return &c, c.Validate()
}
