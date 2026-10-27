package precheck

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckIncludesAllDetailViews(t *testing.T) {
	images := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		parts := b["messages"].([]any)[0].(map[string]any)["content"].([]any)
		for _, p := range parts {
			if p.(map[string]any)["type"] == "image_url" {
				images++
			}
		}
		w.Write([]byte(respWith(`{"watermark":false,"lens":false,"case_edge":false,"case_photo":false,"reason":"clean"}`, "")))
	}))
	defer srv.Close()
	v := Check(cfgFor(srv), "data:image/jpeg;base64,whole", "data:image/jpeg;base64,top", "data:image/jpeg;base64,bottom")
	if images != 3 || v.Fallback {
		t.Fatalf("images=%d fallback=%t", images, v.Fallback)
	}
}
