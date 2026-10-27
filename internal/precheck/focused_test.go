package precheck

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInspectFindsWatermarkAfterOverallPass(t *testing.T) {
	for _, status := range []string{"clean", "watermark", "uncertain"} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				content := `{"watermark":false,"lens":false,"case_edge":false,"case_photo":false,"reason":"整体检查通过"}`
				if calls == 2 {
					content = `{"rounded_panels":false,"layout":"无"}`
				}
				if calls >= 3 {
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if !strings.Contains(body["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string), "水印痕迹") {
						t.Error("missing focused check")
					}
					result := map[string]any{"watermark": status == "watermark", "uncertain": status == "uncertain", "evidence": "球衣数字属于设计内容，残缺字母需复核"}
					data, _ := json.Marshal(result)
					content = string(data)
				}
				w.Write([]byte(respWith(content, "")))
			}))
			defer srv.Close()
			v := Inspect(cfgFor(srv), []string{"whole"})
			if calls != 3 || v.OK != (status == "clean") || v.Fallback != (status == "uncertain") {
				t.Fatalf("status=%s calls=%d verdict=%+v", status, calls, v)
			}
		})
	}
}

func TestObservedSplitPanelsRejectEvenWhenOverallLensFlagIsFalse(t *testing.T) {
	v := Parse(`{"watermark":false,"lens":false,"case_edge":false,"case_photo":false,"panel_layout":"split_case","reason":"上下独立圆角面板"}`, "")
	if v.OK || len(v.Tags) == 0 {
		t.Fatalf("structural evidence ignored: %+v", v)
	}
}
