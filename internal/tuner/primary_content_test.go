package tuner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"titao/internal/judge"
)

func TestCritiqueChecksPrimaryObjectsBeyondFaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		body, _ := json.Marshal(request.Messages)
		for _, rule := range []string{"主要物体整体", "树", "镜头遮挡主体显著部分", "背景陪衬即使不重复", "主体附属部分"} {
			if !strings.Contains(string(body), rule) {
				t.Errorf("review omitted primary-content rule: %s", rule)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"good":false,"camera_clear":false,"score":55,"problem":"树冠被镜头遮挡","dx":0,"dy":0.08,"scale_ratio":0.95}`}}}})
	}))
	defer srv.Close()
	got := Critique(Config{BaseURL: srv.URL, Model: "fixture", Timeout: time.Second}, []byte("review JPEG fixture"), judge.Placement{})
	if got.Good || got.CameraClear || got.Fallback {
		t.Fatalf("bad primary-content critique: %+v", got)
	}
}
