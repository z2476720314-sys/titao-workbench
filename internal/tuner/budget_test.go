package tuner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"titao/internal/judge"
)

func TestCritiqueAppliesCompactBudgetOnlyWhenThinkingDisabled(t *testing.T) {
	for _, model := range []string{"fixture-legacy", "gpt-6.1-terra"} {
		for _, tc := range []struct {
			thinking         bool
			configured, want int
		}{
			{false, 4000, 512},
			{false, 128, 128},
			{false, 0, 512},
			{false, -1, 512},
			{true, 4000, 4000},
		} {
			t.Run(fmt.Sprintf("%s/thinking-%t/budget-%d", model, tc.thinking, tc.configured), func(t *testing.T) {
				field, other := "max_tokens", "max_completion_tokens"
				if model == "gpt-6.1-terra" {
					field, other = other, field
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body[field] != float64(tc.want) {
						t.Errorf("%s=%v want %d", field, body[field], tc.want)
					}
					if _, present := body[other]; present {
						t.Errorf("unexpected unmapped budget field %s", other)
					}
					writeReply(w, `{"good":true,"camera_clear":true,"score":95,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`, "", "stop")
				}))
				defer srv.Close()
				adv := Critique(Config{BaseURL: srv.URL, Model: model, MaxTokens: tc.configured, Thinking: tc.thinking, Timeout: time.Second}, []byte("fixturejpeg"), judge.Placement{})
				if adv.Fallback || !adv.Good {
					t.Fatalf("budget request changed seven-field review behavior: fallback=%t err=%v", adv.Fallback, adv.Err)
				}
			})
		}
	}
}
