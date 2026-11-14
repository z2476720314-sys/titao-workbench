package psx

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
)

// Point uses normalized source-canvas coordinates.
type Point [2]float64

// Cell is the zero-based index in the vertically stacked calibration image.
type VisibleArea struct {
	Polygon []Point
	Cell    int
	// EdgeTolerance is an anchor-only UV sampling tolerance; zero keeps 1e-7.
	EdgeTolerance float64
}

// Scale multiplies the source canvas's contain scale; offsets are canvas fractions.
// Constrained reports adjusted requests or anchors that cannot meet both edges.
type AppliedPlacement struct {
	Scale, OffsetX, OffsetY float64
	Cell                    int
	Constrained             bool
	AnchorUnmet             bool
	// AnchorImpossible certifies no common scale/translation for a paired anchor.
	AnchorImpossible bool
}

// CalibrateVisibleAreas decodes R=u*255, G=v*255, B=128 opaque UV exports.
// The SO-mask hull is inset by one UV quantization step (about 3px at 800px).
// The actual alpha-mask hull and photo frame are inset by 4 screen pixels.
// shortcut: mask concavities and obscured edges use their observed convex hull;
// add template-specific occlusion recovery only with separately verified masks.
func CalibrateVisibleAreas(im image.Image, cellSize int) ([]VisibleArea, error) {
	if im == nil || cellSize <= 0 || im.Bounds().Dx() <= 0 || im.Bounds().Dy() == 0 || im.Bounds().Dy()%cellSize != 0 {
		return nil, fmt.Errorf("invalid UV image or cell size")
	}
	b := im.Bounds()
	width := b.Dx()
	areas := make([]VisibleArea, 0, b.Dy()/cellSize)
	for cell := 0; cell < b.Dy()/cellSize; cell++ {
		var normal [8][9]float64
		var present [256 * 256]bool
		pixels := make([]color.NRGBA, width*cellSize)
		screenPoints := make([]Point, 0, cellSize*2)
		stride := width + 1
		invalid := make([]int, stride*(cellSize+1))
		for y := 0; y < cellSize; y++ {
			left, right := width, -1
			for x := 0; x < width; x++ {
				c := color.NRGBAModel.Convert(im.At(b.Min.X+x, b.Min.Y+cell*cellSize+y)).(color.NRGBA)
				pixels[y*width+x] = c
				bad := 0
				if c.A != 255 || c.B < 127 || c.B > 129 {
					bad = 1
				}
				invalid[(y+1)*stride+x+1] = bad + invalid[y*stride+x+1] + invalid[(y+1)*stride+x] - invalid[y*stride+x]
				if c.A > 128 && c.B >= 127 && c.B <= 129 {
					present[int(c.R)*256+int(c.G)] = true
					if left == width {
						left = x
					}
					right = x
				}
			}
			if right >= left {
				screenPoints = append(screenPoints, Point{float64(left) + .5, float64(y) + .5}, Point{float64(right) + .5, float64(y) + .5})
			}
		}
		// Feather compositing corrupts UV colors despite a valid mask alpha.
		// Fit and verify only opaque cores; retain every valid mask point above.
		reliable := func(x, y int) bool {
			if x < 3 || y < 3 || x+3 >= width || y+3 >= cellSize {
				return false
			}
			return invalid[(y+4)*stride+x+4]-invalid[(y-3)*stride+x+4]-invalid[(y+4)*stride+x-3]+invalid[(y-3)*stride+x-3] == 0
		}
		count := 0
		for y := 0; y < cellSize; y++ {
			for x := 0; x < width; x++ {
				if x%4 != 0 || y%4 != 0 || !reliable(x, y) {
					continue
				}
				c := pixels[y*width+x]
				u, v := float64(c.R)/255, float64(c.G)/255
				sx, sy := (float64(x)+.5)/float64(width), (float64(y)+.5)/float64(cellSize)
				geometryAccumulate(&normal, [8]float64{u, v, 1, 0, 0, 0, -sx * u, -sx * v}, sx)
				geometryAccumulate(&normal, [8]float64{0, 0, 0, u, v, 1, -sy * u, -sy * v}, sy)
				count++
			}
		}
		if count < 16 {
			return nil, fmt.Errorf("cell %d: insufficient UV samples", cell)
		}
		h, err := geometrySolve(normal)
		if err != nil {
			return nil, fmt.Errorf("cell %d: %w", cell, err)
		}
		for _, p := range []Point{{0, 0}, {1, 0}, {1, 1}, {0, 1}} {
			if h[6]*p[0]+h[7]*p[1]+1 <= 1e-6 {
				return nil, fmt.Errorf("cell %d: homography crosses its horizon", cell)
			}
		}
		maxError, sumError, n := 0.0, 0.0, 0
		for y := 0; y < cellSize; y++ {
			for x := 0; x < width; x++ {
				if !reliable(x, y) {
					continue
				}
				c := pixels[y*width+x]
				u, v := float64(c.R)/255, float64(c.G)/255
				den := h[6]*u + h[7]*v + 1
				dx := (h[0]*u+h[1]*v+h[2])/den*float64(width) - (float64(x) + .5)
				dy := (h[3]*u+h[4]*v+h[5])/den*float64(cellSize) - (float64(y) + .5)
				e := math.Hypot(dx, dy)
				maxError = math.Max(maxError, e)
				sumError += e * e
				n++
			}
		}
		if !geometryFinite(maxError) || maxError > 4 || math.Sqrt(sumError/float64(n)) > 2 {
			return nil, fmt.Errorf("cell %d: UV homography error too large (max %.2fpx, rms %.2fpx)", cell, maxError, math.Sqrt(sumError/float64(n)))
		}
		points := make([]Point, 0, 256*256)
		for key, ok := range present {
			if ok {
				points = append(points, Point{float64(key/256) / 255, float64(key%256) / 255})
			}
		}
		poly := geometryHull(points)
		if len(poly) < 3 {
			return nil, fmt.Errorf("cell %d: degenerate UV mask", cell)
		}
		for _, plane := range geometryPlanes(poly) {
			poly = geometryClip(poly, plane[0], plane[1], plane[2]-1.0/255)
		}
		// A feather's decoded UV can lie outside the actual shell. Pull the
		// measured screen mask back through H to bound those colors separately.
		for _, plane := range geometryPlanes(geometryHull(screenPoints)) {
			nx, ny, limit := plane[0]*float64(width), plane[1]*float64(cellSize), plane[2]-4
			poly = geometryClip(poly, nx*h[0]+ny*h[3]-limit*h[6], nx*h[1]+ny*h[4]-limit*h[7], limit-nx*h[2]-ny*h[5])
		}
		mx, my := 4/float64(width), 4/float64(cellSize)
		// Frame clipping is independent of the mask: an obscured edge must never
		// restore artwork beyond the final photograph.
		for _, plane := range [][3]float64{
			{mx*h[6] - h[0], mx*h[7] - h[1], h[2] - mx},
			{h[0] - (1-mx)*h[6], h[1] - (1-mx)*h[7], 1 - mx - h[2]},
			{my*h[6] - h[3], my*h[7] - h[4], h[5] - my},
			{h[3] - (1-my)*h[6], h[4] - (1-my)*h[7], 1 - my - h[5]},
		} {
			poly = geometryClip(poly, plane[0], plane[1], plane[2])
		}
		if len(poly) < 3 || geometrySignedArea(poly) < 1e-10 {
			return nil, fmt.Errorf("cell %d: no safe visible area", cell)
		}
		areas = append(areas, VisibleArea{Polygon: poly, Cell: cell, EdgeTolerance: 1.0 / 255})
	}
	return areas, nil
}

// ArtworkHull includes the full outer squares of every pixel with nonzero alpha.
// Row endpoints suffice because the convex hull contains every intervening pixel.
func ArtworkHull(im image.Image) []Point {
	if im == nil {
		return nil
	}
	b := im.Bounds()
	if b.Empty() {
		return nil
	}
	points := make([]Point, 0, b.Dy()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		left, right := b.Max.X, b.Min.X-1
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := im.At(x, y).RGBA()
			if a != 0 {
				if left == b.Max.X {
					left = x
				}
				right = x
			}
		}
		if right < left {
			continue
		}
		x0, x1 := float64(left-b.Min.X)/float64(b.Dx()), float64(right+1-b.Min.X)/float64(b.Dx())
		y0, y1 := float64(y-b.Min.Y)/float64(b.Dy()), float64(y+1-b.Min.Y)/float64(b.Dy())
		points = append(points, Point{x0, y0}, Point{x1, y0}, Point{x1, y1}, Point{x0, y1})
	}
	return geometryHull(points)
}

// FitArtwork first keeps the requested scale and jointly moves both axes. Only
// when no translation fits does it find the largest smaller uniform scale.
// Anchors are -1 for left/top, +1 for right/bottom, 2 for both edges, 0 for none.
// preferOffsets=true instead keeps requested XY by shrinking when unanchored.
func FitArtwork(area VisibleArea, hull []Point, cw, ch, pw, ph int, desiredScale, offsetX, offsetY float64, anchorX, anchorY int, preferOffsets ...bool) (AppliedPlacement, error) {
	result := AppliedPlacement{Cell: area.Cell}
	if cw <= 0 || ch <= 0 || pw <= 0 || ph <= 0 || !geometryFinite(desiredScale) || desiredScale <= 0 || !geometryFinite(offsetX) || !geometryFinite(offsetY) || anchorX < -1 || anchorX > 2 || anchorY < -1 || anchorY > 2 || !geometryFinite(area.EdgeTolerance) || area.EdgeTolerance < 0 {
		return result, fmt.Errorf("invalid placement dimensions, scale, offsets, anchors or edge tolerance")
	}
	poly := append([]Point(nil), area.Polygon...)
	if len(poly) < 3 {
		return result, fmt.Errorf("empty visible area")
	}
	for _, p := range append(append([]Point(nil), poly...), hull...) {
		if !geometryFinite(p[0]) || !geometryFinite(p[1]) || p[0] < 0 || p[0] > 1 || p[1] < 0 || p[1] > 1 {
			return result, fmt.Errorf("nonfinite or non-normalized geometry")
		}
	}
	if geometrySignedArea(poly) < 0 {
		for i, j := 0, len(poly)-1; i < j; i, j = i+1, j-1 {
			poly[i], poly[j] = poly[j], poly[i]
		}
	}
	if geometrySignedArea(poly) < 1e-12 {
		return result, fmt.Errorf("degenerate visible area")
	}
	for i, a := range poly {
		b := poly[(i+1)%len(poly)]
		if math.Hypot(b[0]-a[0], b[1]-a[1]) < 1e-12 {
			return result, fmt.Errorf("duplicate visible-area vertex")
		}
		for _, p := range poly {
			if geometryCross(a, b, p) < -1e-10 {
				return result, fmt.Errorf("visible area must be convex")
			}
		}
	}
	hull = geometryHull(append([]Point(nil), hull...))
	if len(hull) < 3 || geometrySignedArea(hull) < 1e-15 {
		return result, fmt.Errorf("empty or degenerate artwork hull")
	}
	base := math.Min(float64(cw)/float64(pw), float64(ch)/float64(ph))
	rx, ry := float64(pw)*base/float64(cw), float64(ph)*base/float64(ch)
	const anchorTolerance = 1e-7
	visualTolerance := math.Max(anchorTolerance, area.EdgeTolerance)
	pmin, pmax := geometryBounds(poly)
	hmin, hmax := geometryBounds(hull)
	ratios := Point{rx, ry}
	spanLimit := math.Min((pmax[0]-pmin[0])/((hmax[0]-hmin[0])*rx), (pmax[1]-pmin[1])/((hmax[1]-hmin[1])*ry))
	requiredMinimum := 0.0
	for axis, anchor := range []int{anchorX, anchorY} {
		if anchor == 2 {
			requiredMinimum = math.Max(requiredMinimum, (pmax[axis]-pmin[axis]-2*visualTolerance)/((hmax[axis]-hmin[axis])*ratios[axis]))
		}
	}
	requestedScale := desiredScale
	floorRaised := len(preferOffsets) > 0 && preferOffsets[0] && requiredMinimum > desiredScale
	if floorRaised {
		// Later proposals retain the paired span, while hard NoCrop still wins.
		desiredScale = math.Nextafter(requiredMinimum, math.Inf(1))
	}
	planes := geometryPlanes(poly)
	feasible := func(scale, tolerance float64) []Point {
		first := Point{(hull[0][0] - .5) * rx * scale, (hull[0][1] - .5) * ry * scale}
		out := make([]Point, len(poly))
		for i, p := range poly {
			out[i] = Point{p[0] - .5 - first[0], p[1] - .5 - first[1]}
		}
		if tolerance > 0 {
			// Only the impossibility probe tolerates roundoff at exact contacts.
			lo, hi := geometryBounds(out)
			out = []Point{{lo[0] - tolerance, lo[1] - tolerance}, {hi[0] + tolerance, lo[1] - tolerance}, {hi[0] + tolerance, hi[1] + tolerance}, {lo[0] - tolerance, hi[1] + tolerance}}
		}
		for _, plane := range planes {
			support := math.Inf(-1)
			for _, p := range hull {
				support = math.Max(support, plane[0]*(p[0]-.5)*rx*scale+plane[1]*(p[1]-.5)*ry*scale)
			}
			out = geometryClip(out, plane[0], plane[1], plane[2]-.5*(plane[0]+plane[1])-support+tolerance)
			if len(out) == 0 {
				break
			}
		}
		return out
	}
	maximumScaleBounds := func(hi float64) (float64, float64) {
		lo := 0.0
		for i := 0; i < 60; i++ {
			mid := (lo + hi) / 2
			if len(feasible(mid, 0)) != 0 {
				lo = mid
			} else {
				hi = mid
			}
		}
		return lo, hi
	}
	scale := desiredScale
	legal := feasible(scale, 0)
	if len(preferOffsets) > 0 && preferOffsets[0] && anchorX == 0 && anchorY == 0 {
		lower, upper := 0.0, desiredScale
		possible := true
		for _, plane := range planes {
			support := math.Inf(-1)
			for _, p := range hull {
				support = math.Max(support, plane[0]*(p[0]-.5)*rx+plane[1]*(p[1]-.5)*ry)
			}
			limit := plane[2] - plane[0]*(.5+offsetX) - plane[1]*(.5+offsetY)
			switch {
			case support > 0:
				upper = math.Min(upper, limit/support)
			case support < 0:
				lower = math.Max(lower, limit/support)
			case limit < 0:
				possible = false
			}
		}
		if possible && geometryFinite(upper) && upper > 0 && upper >= lower {
			request := Point{offsetX, offsetY}
			candidate := upper
			for attempt := 0; attempt < 8 && candidate > 0 && candidate >= lower; attempt++ {
				trial := feasible(candidate, 0)
				if len(trial) > 0 && geometryNearest(trial, request) == request {
					scale, legal = candidate, trial
					break
				}
				// The interval calculation may round an upper endpoint outward.
				candidate = math.Nextafter(candidate, 0)
			}
		}
	}
	if len(legal) == 0 {
		// Axis extents bound the binary search even for an enormous request.
		scale, _ = maximumScaleBounds(math.Min(desiredScale, spanLimit))
		legal = feasible(scale, 0)
		if len(legal) == 0 || scale <= 0 {
			return result, fmt.Errorf("no positive no-crop placement")
		}
	}
	anchorDomain := func(domain []Point, scale, tolerance float64) []Point {
		for axis, anchor := range []int{anchorX, anchorY} {
			for side, edge := range []float64{pmin[axis], pmax[axis]} {
				if anchor != 2 && !((anchor == -1 && side == 0) || (anchor == 1 && side == 1)) {
					continue
				}
				content := hmin[axis]
				if side == 1 {
					content = hmax[axis]
				}
				offset := edge - .5 - (content-.5)*ratios[axis]*scale
				normal := Point{}
				normal[axis] = 1
				domain = geometryClip(domain, normal[0], normal[1], offset+tolerance)
				domain = geometryClip(domain, -normal[0], -normal[1], -offset+tolerance)
			}
		}
		return domain
	}
	request := Point{offsetX, offsetY}
	target := request
	min, max := geometryBounds(legal)
	for axis, anchor := range []int{anchorX, anchorY} {
		switch anchor {
		case -1:
			target[axis] = min[axis]
		case 1:
			target[axis] = max[axis]
		case 2:
			target[axis] = (min[axis] + max[axis]) / 2
		}
	}
	chosen := geometryNearest(legal, target)
	if anchorX != 0 || anchorY != 0 {
		contact := anchorDomain(legal, scale, anchorTolerance)
		if len(contact) == 0 && visualTolerance > anchorTolerance {
			contact = anchorDomain(legal, scale, visualTolerance)
		}
		if len(contact) != 0 {
			chosen = geometryNearest(contact, request)
		}
	}
	// Check the same offset bands without re-adding canvas coordinates, which
	// can round an inclusive contact past the 1e-7 boundary.
	anchorUnmet := len(anchorDomain([]Point{chosen}, scale, visualTolerance)) == 0
	if floorRaised && anchorUnmet {
		// A floor rounded to a line can miss its own contact band by an ULP.
		candidate := scale
		for attempt := 0; attempt < 16; attempt++ {
			candidate = math.Nextafter(candidate, math.Inf(1))
			trial := feasible(candidate, 0)
			if len(trial) == 0 {
				break
			}
			contact := anchorDomain(trial, candidate, anchorTolerance)
			if len(contact) == 0 && visualTolerance > anchorTolerance {
				contact = anchorDomain(trial, candidate, visualTolerance)
			}
			if len(contact) == 0 {
				continue
			}
			point := geometryNearest(contact, request)
			if len(anchorDomain([]Point{point}, candidate, visualTolerance)) != 0 {
				scale, chosen = candidate, point
				anchorUnmet = false
				break
			}
		}
	}
	constrained := math.Abs(scale-requestedScale) > 1e-9 || math.Hypot(chosen[0]-request[0], chosen[1]-request[1]) > 1e-9 || anchorUnmet
	anchorImpossible := false
	if anchorUnmet && (anchorX == 2 || anchorY == 2) {
		if area.EdgeTolerance > 0 {
			// A visual band permits an interval of scales. Only the necessary
			// minimum span exceeding a conservative global maximum proves failure.
			_, upper := maximumScaleBounds(spanLimit)
			anchorImpossible = requiredMinimum-upper > 1e-12*math.Max(1, requiredMinimum)
		} else {
			axis := 0
			if anchorX != 2 {
				axis = 1
			}
			candidate := (pmax[axis] - pmin[axis]) / ((hmax[axis] - hmin[axis]) * ratios[axis])
			// Legacy areas retain the exact paired-span certificate.
			trial := anchorDomain(feasible(candidate, anchorTolerance), candidate, anchorTolerance)
			anchorImpossible = len(trial) == 0
		}
	}
	return AppliedPlacement{Scale: scale, OffsetX: chosen[0], OffsetY: chosen[1], Cell: area.Cell, Constrained: constrained, AnchorUnmet: anchorUnmet, AnchorImpossible: anchorImpossible}, nil
}

func geometryAccumulate(normal *[8][9]float64, row [8]float64, value float64) {
	for i := 0; i < 8; i++ {
		for j := 0; j < 8; j++ {
			normal[i][j] += row[i] * row[j]
		}
		normal[i][8] += row[i] * value
	}
}

func geometrySolve(a [8][9]float64) ([8]float64, error) {
	var out [8]float64
	for col := 0; col < 8; col++ {
		pivot := col
		for row := col + 1; row < 8; row++ {
			if math.Abs(a[row][col]) > math.Abs(a[pivot][col]) {
				pivot = row
			}
		}
		if math.Abs(a[pivot][col]) < 1e-10 {
			return out, fmt.Errorf("degenerate UV homography")
		}
		a[pivot], a[col] = a[col], a[pivot]
		divisor := a[col][col]
		for j := col; j <= 8; j++ {
			a[col][j] /= divisor
		}
		for row := 0; row < 8; row++ {
			if row == col {
				continue
			}
			factor := a[row][col]
			for j := col; j <= 8; j++ {
				a[row][j] -= factor * a[col][j]
			}
		}
	}
	for i := range out {
		out[i] = a[i][8]
		if !geometryFinite(out[i]) {
			return out, fmt.Errorf("nonfinite UV homography")
		}
	}
	return out, nil
}

func geometryCross(a, b, p Point) float64 { return (b[0]-a[0])*(p[1]-a[1]) - (b[1]-a[1])*(p[0]-a[0]) }
func geometryFinite(v float64) bool       { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func geometrySignedArea(poly []Point) float64 {
	sum := 0.0
	for i, a := range poly {
		b := poly[(i+1)%len(poly)]
		sum += a[0]*b[1] - a[1]*b[0]
	}
	return sum / 2
}

func geometryHull(points []Point) []Point {
	if len(points) < 2 {
		return points
	}
	sort.Slice(points, func(i, j int) bool {
		return points[i][0] < points[j][0] || (points[i][0] == points[j][0] && points[i][1] < points[j][1])
	})
	unique := points[:0]
	for _, p := range points {
		if len(unique) == 0 || unique[len(unique)-1] != p {
			unique = append(unique, p)
		}
	}
	points = unique
	if len(points) < 3 {
		return append([]Point(nil), points...)
	}
	lo, hi := make([]Point, 0, len(points)), make([]Point, 0, len(points))
	for _, p := range points {
		for len(lo) >= 2 && geometryCross(lo[len(lo)-2], lo[len(lo)-1], p) <= 0 {
			lo = lo[:len(lo)-1]
		}
		lo = append(lo, p)
	}
	for i := len(points) - 1; i >= 0; i-- {
		p := points[i]
		for len(hi) >= 2 && geometryCross(hi[len(hi)-2], hi[len(hi)-1], p) <= 0 {
			hi = hi[:len(hi)-1]
		}
		hi = append(hi, p)
	}
	return append(lo[:len(lo)-1], hi[:len(hi)-1]...)
}

// Each normalized half-plane is nx*x + ny*y <= b.
func geometryPlanes(poly []Point) [][3]float64 {
	planes := make([][3]float64, 0, len(poly))
	for i, a := range poly {
		b := poly[(i+1)%len(poly)]
		nx, ny := b[1]-a[1], a[0]-b[0]
		length := math.Hypot(nx, ny)
		nx /= length
		ny /= length
		planes = append(planes, [3]float64{nx, ny, nx*a[0] + ny*a[1]})
	}
	return planes
}

func geometryClip(poly []Point, nx, ny, b float64) []Point {
	if len(poly) == 0 {
		return nil
	}
	out := make([]Point, 0, len(poly)+1)
	previous := poly[len(poly)-1]
	pv := nx*previous[0] + ny*previous[1] - b
	for _, current := range poly {
		cv := nx*current[0] + ny*current[1] - b
		if (pv <= 0) != (cv <= 0) {
			fraction := pv / (pv - cv)
			out = append(out, Point{previous[0] + fraction*(current[0]-previous[0]), previous[1] + fraction*(current[1]-previous[1])})
		}
		if cv <= 0 {
			out = append(out, current)
		}
		previous, pv = current, cv
	}
	clean := out[:0]
	for _, p := range out {
		if len(clean) == 0 || math.Hypot(p[0]-clean[len(clean)-1][0], p[1]-clean[len(clean)-1][1]) > 1e-12 {
			clean = append(clean, p)
		}
	}
	if len(clean) > 1 && math.Hypot(clean[0][0]-clean[len(clean)-1][0], clean[0][1]-clean[len(clean)-1][1]) <= 1e-12 {
		clean = clean[:len(clean)-1]
	}
	return clean
}

func geometryBounds(poly []Point) (Point, Point) {
	min, max := Point{math.Inf(1), math.Inf(1)}, Point{math.Inf(-1), math.Inf(-1)}
	for _, p := range poly {
		for axis := 0; axis < 2; axis++ {
			min[axis] = math.Min(min[axis], p[axis])
			max[axis] = math.Max(max[axis], p[axis])
		}
	}
	return min, max
}

func geometryNearest(poly []Point, p Point) Point {
	inside := len(poly) >= 3 && geometrySignedArea(poly) > 1e-18
	if inside {
		for i, a := range poly {
			if geometryCross(a, poly[(i+1)%len(poly)], p) < 0 {
				inside = false
				break
			}
		}
	}
	if inside {
		return p
	}
	best, distance := poly[0], math.Inf(1)
	for i, a := range poly {
		b := poly[(i+1)%len(poly)]
		dx, dy := b[0]-a[0], b[1]-a[1]
		fraction := 0.0
		if den := dx*dx + dy*dy; den > 0 {
			fraction = math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/den))
		}
		q := Point{a[0] + fraction*dx, a[1] + fraction*dy}
		if d := math.Hypot(q[0]-p[0], q[1]-p[1]); d < distance {
			best, distance = q, d
		}
	}
	return best
}
