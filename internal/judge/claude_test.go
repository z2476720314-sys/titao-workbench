package judge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClaudeOmitsUnsupportedSamplingAndProxyThinkingParameters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		for _, key := range []string{"temperature", "enable_thinking", "response_format"} {
			if _, exists := body[key]; exists {
				t.Errorf("Claude request contains unsupported %s", key)
			}
		}
		if body["model"] != "claude-haiku-5-5" || body["max_tokens"] == nil || body["messages"] == nil {
			t.Error("missing required request fields")
		}
		w.Write([]byte(respWith(`{"pick":1,"reason":"清晰完整"}`, "")))
	}))
	defer srv.Close()
	settings := cfgFor(srv)
	settings.Model, settings.Temperature = "claude-haiku-5-5", 0.1
	if result := Choose(settings, cands(2), "pattern", Placement{}); result.Fallback {
		t.Fatal(result.Err)
	}
}
