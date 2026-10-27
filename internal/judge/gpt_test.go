package judge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGPT6LunaUsesNativeThinkingAndCompletionLimit(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				for _, key := range []string{"enable_thinking", "max_tokens"} {
					if _, exists := body[key]; exists {
						t.Errorf("unsupported GPT parameter: %s", key)
					}
				}
				effort := "none"
				if enabled {
					effort = "medium"
				}
				if body["reasoning_effort"] != effort || body["max_completion_tokens"] != float64(200) {
					t.Errorf("invalid native options: effort=%v limit=%v", body["reasoning_effort"], body["max_completion_tokens"])
				}
				format, ok := body["response_format"].(map[string]any)
				if !ok || format["type"] != "json_object" {
					t.Error("structured judgment must request JSON output")
				}
				_, temperature := body["temperature"]
				if temperature == enabled {
					t.Error("sampling parameters must follow the reasoning setting")
				}
				w.Write([]byte(respWith(`{"pick":1,"reason":"完整清晰"}`, "")))
			}))
			defer srv.Close()
			settings := cfgFor(srv)
			settings.Model, settings.Thinking = "gpt-6-luna", enabled
			settings.MaxTokens = 200
			if result := Choose(settings, cands(2), "pattern", Placement{}); result.Fallback {
				t.Fatal(result.Err)
			}
		})
	}
}
