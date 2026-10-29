package jobs

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/imgutil"
	"titao/internal/judge"
	"titao/internal/psx"
)

type backgroundEditor struct {
	reviewPhotoshop
	size        image.Point
	cornerAlpha uint32
}

func (p *backgroundEditor) Render(r psx.RenderRequest) (string, error) {
	im, err := imgutil.Decode(r.Pattern)
	if err != nil {
		return "", err
	}
	p.size = im.Bounds().Size()
	_, _, _, p.cornerAlpha = im.At(0, 0).RGBA()
	return p.reviewPhotoshop.Render(r)
}

func TestAutoStartsFullOnOriginalCanvasRegardlessOfTransparency(t *testing.T) {
	for _, tc := range []struct {
		height    int
		direction string
		calls     int
		alpha     uint8
		inset     int
	}{{70, "smaller", 1, 255, 0}, {90, "smaller", 1, 255, 0}, {70, "right", 2, 255, 0}, {70, "smaller", 1, 150, 0}, {90, "smaller", 1, 255, 10}} {
		height := tc.height
		t.Run(strconv.Itoa(height)+tc.direction+strconv.Itoa(int(tc.alpha)), func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			editor := &backgroundEditor{}
			e.ps = editor
			dir := t.TempDir()
			path := filepath.Join(dir, "pattern.png")
			im := image.NewNRGBA(image.Rect(0, 0, 40, height))
			draw.Draw(im, im.Bounds().Inset(tc.inset), image.NewUniform(color.NRGBA{20, 100, 190, tc.alpha}), image.Point{}, draw.Src)
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, im)
			f.Close()
			tpl := filepath.Join(dir, "template.tif")
			os.WriteFile(tpl, []byte("template"), 0600)
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				content := `{"good":false,"score":70,"problem":"主体遮挡","direction":"` + tc.direction + `","step":0.1}`
				if calls > 1 {
					content = `{"good":true,"score":90,"problem":"无","direction":"none","step":0.1}`
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
			}))
			defer srv.Close()
			settings := cfg.Default()
			task := &Task{ID: 1, Pattern: path, PatternName: "background", Template: tpl, TemplateName: "template", FillTop: true}
			opt := Options{Auto: true, NoCrop: true, Fit: "auto", TrimAlpha: true, Scales: settings.Candidates, CellSize: 80, JPGQuality: 90}
			e.processOne(context.Background(), opt, settings, judge.Config{BaseURL: srv.URL, APIKey: "dummy", Model: "test", MaxTokens: 100, Timeout: time.Second}, task, filepath.Join(dir, "out"), 1, 1)
			if task.Phase != PhaseDone || editor.size != image.Pt(40, height) || len(editor.requests) != tc.calls || editor.requests[0].Fit != "cover" || len(editor.requests[0].Scales) != 1 || calls != tc.calls {
				t.Fatalf("background flow: phase=%s size=%v renders=%d calls=%d", task.Phase, editor.size, len(editor.requests), calls)
			}
			wantAlpha := uint32(tc.alpha) * 257
			if tc.inset > 0 {
				wantAlpha = 0
			}
			if editor.cornerAlpha != wantAlpha {
				t.Fatalf("original alpha changed: %d want %d", editor.cornerAlpha, wantAlpha)
			}
		})
	}
}
