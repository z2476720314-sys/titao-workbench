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
	size image.Point
}

func (p *backgroundEditor) Render(r psx.RenderRequest) (string, error) {
	im, err := imgutil.Decode(r.Pattern)
	if err != nil {
		return "", err
	}
	p.size = im.Bounds().Size()
	return p.reviewPhotoshop.Render(r)
}

func TestAlloverBackgroundExtendsWithoutChangingScale(t *testing.T) {
	for _, height := range []int{70, 90} {
		t.Run(strconv.Itoa(height), func(t *testing.T) {
			e, _, _ := newTestEngine(t)
			editor := &backgroundEditor{}
			e.ps = editor
			dir := t.TempDir()
			path := filepath.Join(dir, "pattern.png")
			im := image.NewNRGBA(image.Rect(0, 0, 40, height))
			draw.Draw(im, im.Bounds(), image.NewUniform(color.NRGBA{20, 100, 190, 255}), image.Point{}, draw.Src)
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
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"good":true,"score":90,"problem":"无","direction":"none","step":0.1}`}}}})
			}))
			defer srv.Close()
			settings := cfg.Default()
			task := &Task{ID: 1, Pattern: path, PatternName: "background", Template: tpl, TemplateName: "template", FillTop: true}
			opt := Options{Auto: true, NoCrop: true, Fit: "auto", Scales: settings.Candidates, CellSize: 80, JPGQuality: 90}
			e.processOne(context.Background(), opt, settings, judge.Config{BaseURL: srv.URL, APIKey: "dummy", Model: "test", MaxTokens: 100, Timeout: time.Second}, task, filepath.Join(dir, "out"), 1, 1)
			wantScales, wantCalls := 1, 1
			if height > 80 {
				wantScales, wantCalls = len(squashScales(settings.Candidates)), 2
			}
			if task.Phase != PhaseDone || editor.size != image.Pt(40, max(80, height)) || len(editor.requests) != 1 || len(editor.requests[0].Scales) != wantScales || calls != wantCalls {
				t.Fatalf("background flow: phase=%s size=%v renders=%d calls=%d", task.Phase, editor.size, len(editor.requests), calls)
			}
		})
	}
}
