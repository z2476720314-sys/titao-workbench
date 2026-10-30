package judge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeekDisabledThinkingAlsoLimitsRelayReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["thinking"].(map[string]any)["type"] != "disabled" || body["reasoning_effort"] != "low" {
			t.Error("disabled thinking request must retain native switch and low relay effort")
		}
		w.Write([]byte(respWith(`{"pick":1,"reason":"完整"}`, "")))
	}))
	defer srv.Close()
	c := cfgFor(srv)
	c.Model = "deepseek-v4.1-flash"
	if result := Choose(c, cands(2), "pattern", Placement{}); result.Fallback {
		t.Fatal(result.Err)
	}
}
