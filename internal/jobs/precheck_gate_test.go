package jobs

import (
	"context"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"titao/internal/judge"
)

func TestStrictPrecheckFailureBlocksTask(t *testing.T) {
	e, store, _ := newTestEngine(t)
	path := filepath.Join(t.TempDir(), "pattern.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	f.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 500) }))
	defer srv.Close()
	task := &Task{ID: 1, Pattern: path, PatternName: "pattern"}
	c := store.Get()
	c.PrecheckOn = true
	c.PrecheckSkip = true
	blocked := e.precheckOne(context.Background(), c, judge.Config{BaseURL: srv.URL, APIKey: "test", Model: "test", Timeout: time.Second}, task, 1, 1)
	if !blocked || task.Phase != PhaseFailed {
		t.Fatalf("unconfirmed input reached rendering: blocked=%v task=%+v", blocked, task)
	}
}
