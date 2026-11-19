package printcanvas

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
	"titao/internal/imgutil"
	"titao/internal/psx"
)

const Width = 945
const Height = 1890

func Build(pattern, output string, req psx.RenderRequest, scale float64) (*psx.PrintCanvas, error) {
	if err := validRequest(req, scale); err != nil {
		return nil, err
	}
	if strings.TrimSpace(output) == "" {
		return nil, fmt.Errorf("empty print canvas path")
	}
	sourcePath, err := filepath.Abs(pattern)
	if err != nil {
		return nil, err
	}
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return nil, err
	}
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return nil, err
	}
	outputInfo, statErr := os.Stat(outputPath)
	if strings.EqualFold(sourcePath, outputPath) || (statErr == nil && os.SameFile(sourceInfo, outputInfo)) {
		return nil, fmt.Errorf("print canvas must not overwrite its source")
	}
	data, err := os.ReadFile(pattern)
	if err != nil {
		return nil, err
	}
	sourceSHA := sum(data)
	im, err := imgutil.Decode(pattern)
	if err != nil {
		return nil, err
	}
	data, err = os.ReadFile(pattern)
	if err != nil {
		return nil, err
	}
	if sum(data) != sourceSHA {
		return nil, fmt.Errorf("print source changed while decoding")
	}
	b := im.Bounds()
	hull := psx.ArtworkHull(im)
	if b.Empty() || len(hull) < 3 {
		return nil, fmt.Errorf("empty print artwork")
	}
	base := math.Min(float64(Width)/float64(b.Dx()), float64(Height)/float64(b.Dy()))
	var p psx.AppliedPlacement
	var sx, sy, cx, cy float64
	if req.NoCrop {
		// Only anchor comparisons tolerate roundoff; source containment stays strict.
		area := psx.VisibleArea{Polygon: []psx.Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}}, EdgeTolerance: 1e-6}
		fitAtRatio := func(ratio float64, preferOffsets bool) (psx.AppliedPlacement, error) {
			virtualHeight := max(int(math.Ceil(float64(b.Dy())*psx.MinVerticalStretch)), min(int(math.Floor(float64(b.Dy())*psx.MaxVerticalStretch)), int(math.Round(float64(b.Dy())*ratio))))
			virtualBase := math.Min(float64(Width)/float64(b.Dx()), float64(Height)/float64(virtualHeight))
			result, err := psx.FitArtwork(area, hull, Width, Height, b.Dx(), virtualHeight, scale*base/virtualBase, req.OffsetX, req.OffsetY, req.AnchorX, req.AnchorY, preferOffsets)
			result.Scale *= virtualBase / base
			if virtualHeight != b.Dy() {
				result.StretchY = float64(virtualHeight) / float64(b.Dy())
				result.Constrained = true
			}
			return result, err
		}
		previousRatio := (psx.AppliedPlacement{StretchY: req.StretchY}).VerticalStretch()
		p, err = fitAtRatio(previousRatio, req.PreferOffsets)
		if err != nil {
			return nil, err
		}
		if (req.PreferOffsets || p.AnchorImpossible) && (req.AnchorX == 2 || req.AnchorY == 2) {
			low, high := hull[0], hull[0]
			for _, point := range hull[1:] {
				for axis := range low {
					low[axis], high[axis] = math.Min(low[axis], point[axis]), math.Max(high[axis], point[axis])
				}
			}
			fixedX := float64(Width) / ((high[0] - low[0]) * float64(b.Dx()))
			fixedY := float64(Height) / ((high[1] - low[1]) * float64(b.Dy()))
			ratio := base * scale * previousRatio / fixedX
			if req.AnchorY == 2 {
				ratio = fixedY / (base * scale)
				if req.AnchorX == 2 {
					ratio = fixedY / fixedX
				}
			}
			adjusted, err := fitAtRatio(math.Max(.97, math.Min(1.03, ratio)), true)
			if err != nil {
				return nil, err
			}
			if !adjusted.AnchorUnmet {
				p = adjusted
			}
		}
		sx, sy = base*p.Scale, base*p.Scale*p.VerticalStretch()
		cx, cy = (.5+p.OffsetX)*Width, (.5+p.OffsetY)*Height
	} else {
		p, sx, sy, cx, cy, err = manualPlacement(req, scale, b.Dx(), b.Dy())
		if err != nil {
			return nil, err
		}
	}
	tx, ty := cx-float64(b.Dx())*sx/2, cy-float64(b.Dy())*sy/2
	if !finite(sx) || !finite(sy) || sx <= 0 || sy <= 0 || !finite(tx) || !finite(ty) {
		return nil, fmt.Errorf("nonfinite print transform")
	}
	verified := true
	for _, point := range hull {
		x, y := tx+point[0]*float64(b.Dx())*sx, ty+point[1]*float64(b.Dy())*sy
		if !finite(x) || !finite(y) || x < -1e-5 || y < -1e-5 || x > Width+1e-5 || y > Height+1e-5 {
			verified = false
		}
	}
	if req.NoCrop && !verified {
		return nil, fmt.Errorf("print placement leaves canonical domain")
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, Width, Height))
	draw.BiLinear.Transform(canvas, f64.Aff3{sx, 0, tx - sx*float64(b.Min.X), 0, sy, ty - sy*float64(b.Min.Y)}, im, b, draw.Src, nil)
	var encoded bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&encoded, canvas); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(output), ".print-canvas-*.png")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(encoded.Bytes())
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := os.Rename(f.Name(), output); err != nil {
		return nil, err
	}
	return &psx.PrintCanvas{Path: output, SHA256: sum(encoded.Bytes()), SourceSHA256: sourceSHA, Width: Width, Height: Height, Placement: p, Verified: verified}, nil
}

func Validate(canvas *psx.PrintCanvas) error {
	if canvas == nil || strings.TrimSpace(canvas.Path) == "" || canvas.Width != Width || canvas.Height != Height || !validSHA(canvas.SHA256) || !validSHA(canvas.SourceSHA256) {
		return fmt.Errorf("invalid canonical print canvas evidence")
	}
	p := canvas.Placement
	if !finite(p.Scale) || p.Scale <= 0 || !finite(p.OffsetX) || !finite(p.OffsetY) || !p.ValidStretch() || (p.AnchorImpossible && !p.AnchorUnmet) {
		return fmt.Errorf("invalid print placement evidence")
	}
	data, err := os.ReadFile(canvas.Path)
	if err != nil {
		return err
	}
	if sum(data) != canvas.SHA256 {
		return fmt.Errorf("print canvas SHA256 mismatch")
	}
	im, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decode print canvas: %w", err)
	}
	if im.Bounds().Dx() != Width || im.Bounds().Dy() != Height {
		return fmt.Errorf("print canvas decoded dimensions mismatch")
	}
	return nil
}

func validRequest(req psx.RenderRequest, scale float64) error {
	if !finite(scale) || scale <= 0 || !finite(req.OffsetX) || !finite(req.OffsetY) || !(psx.AppliedPlacement{StretchY: req.StretchY}).ValidStretch() || req.AnchorX < -1 || req.AnchorX > 2 || req.AnchorY < -1 || req.AnchorY > 2 {
		return fmt.Errorf("invalid print scale, offsets or anchors")
	}
	switch req.Fit {
	case "", psx.FitAuto, psx.FitContain, psx.FitCover, psx.FitStretch, psx.FitNone:
	default:
		return fmt.Errorf("unknown print fit %q", req.Fit)
	}
	b := req.ContentBounds
	for _, v := range b {
		if !finite(v) || v < 0 || v > 1 {
			return fmt.Errorf("invalid print content bounds")
		}
	}
	if b != [4]float64{} && (b[0] >= b[2] || b[1] >= b[3]) {
		return fmt.Errorf("empty print content bounds")
	}
	return nil
}

// Manual fits retain their existing scale basis and intentional crop/stretch.
func manualPlacement(req psx.RenderRequest, scale float64, pw, ph int) (psx.AppliedPlacement, float64, float64, float64, float64, error) {
	bx, by := float64(Width)/float64(pw), float64(Height)/float64(ph)
	switch req.Fit {
	case psx.FitStretch:
	case psx.FitNone:
		bx, by = 1, 1
	case psx.FitCover:
		bx, by = math.Max(bx, by), math.Max(bx, by)
	default:
		bx, by = math.Min(bx, by), math.Min(bx, by)
	}
	sx, sy := bx*scale, by*scale
	b := req.ContentBounds
	if req.AnchorX == 2 && b != [4]float64{} {
		sx = float64(Width) / (float64(pw) * (b[2] - b[0]))
		sy = sx
	}
	w, h := float64(pw)*sx, float64(ph)*sy
	cx, cy := float64(Width)/2, float64(Height)/2
	if req.AlignBottom && (req.Fit == "" || req.Fit == psx.FitAuto || req.Fit == psx.FitContain) {
		cy = Height - h/2
	}
	cx += Width * req.OffsetX
	cy += Height * req.OffsetY
	if b != [4]float64{} && req.Fit != psx.FitStretch {
		if req.AnchorX < 0 {
			cx = w/2 - w*b[0]
		}
		if req.AnchorX > 0 {
			cx = Width + w/2 - w*b[2]
		}
		if req.AnchorY < 0 {
			cy = h/2 - h*b[1]
		}
		if req.AnchorY > 0 {
			cy = Height + h/2 - h*b[3]
		}
	} else if req.Fit == psx.FitCover {
		if w >= Width-1e-9 {
			cx = math.Max(Width-w/2, math.Min(w/2, cx))
		}
		if h >= Height-1e-9 {
			cy = math.Max(Height-h/2, math.Min(h/2, cy))
		}
	}
	p := psx.AppliedPlacement{Scale: sx / bx, OffsetX: cx/Width - .5, OffsetY: cy/Height - .5}
	if !finite(p.Scale) || p.Scale <= 0 || !finite(p.OffsetX) || !finite(p.OffsetY) || !finite(sy/by) || math.Abs(p.Scale-sy/by) > 1e-9*math.Max(p.Scale, sy/by) {
		return p, 0, 0, 0, 0, fmt.Errorf("unrepresentable manual print placement")
	}
	p.Constrained = math.Abs(p.Scale-scale) > 1e-9 || math.Abs(p.OffsetX-req.OffsetX) > 1e-9 || math.Abs(p.OffsetY-req.OffsetY) > 1e-9
	return p, sx, sy, cx, cy, nil
}

func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func sum(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
func validSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
