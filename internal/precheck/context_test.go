package precheck

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWatermarkZoomRetainsWholeArtworkContext(t *testing.T) {
	calls, zooms := 0, 0
	views := []string{"whole", "top", "bottom", "q1", "q2", "q3", "q4"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := `{"watermark":false,"lens":false,"case_edge":false,"case_photo":false,"reason":"独立文字标志"}`
		if calls == 2 {
			content = `{"rounded_panels":false,"layout":"无"}`
		}
		if calls >= 3 {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			parts := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
			var images []string
			for _, p := range parts {
				item := p.(map[string]any)
				if item["type"] == "image_url" {
					images = append(images, item["image_url"].(map[string]any)["url"].(string))
				}
			}
			if len(images) != 2 || images[0] != "whole" || images[1] != views[6-zooms] {
				t.Errorf("zoom lost context: %v", images)
			}
			zooms++
			content = `{"watermark":false,"uncertain":false,"evidence":"主体笔刷文字，未见叠字"}`
		}
		w.Write([]byte(respWith(content, "")))
	}))
	defer srv.Close()
	v := Inspect(cfgFor(srv), views)
	if v.Fallback || !v.OK || zooms != 4 {
		t.Fatalf("inspection=%+v zooms=%d", v, zooms)
	}
}

func TestExplicitNegativeChecksDoNotRequirePositiveEvidenceText(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		content := `{"watermark":false,"lens":false,"case_edge":false,"case_photo":false,"reason":"干净图案"}`
		if calls == 2 {
			content = `{"rounded_panels":false,"layout":""}`
		}
		if calls == 3 {
			content = `{"watermark":false,"uncertain":false,"evidence":""}`
		}
		w.Write([]byte(respWith(content, "")))
	}))
	defer srv.Close()
	if v := Inspect(cfgFor(srv), []string{"whole"}); v.Fallback || !v.OK {
		t.Fatalf("explicit negative was rejected: %+v", v)
	}
}

func TestIncompleteFocusedDecisionsRemainUnconfirmed(t *testing.T) {
	for _, tc := range []struct{ name, panel, watermark string }{
		{"missing_panel_flag", `{"layout":"无"}`, ""},
		{"positive_panel_without_evidence", `{"rounded_panels":true}`, ""},
		{"missing_uncertainty_flag", "", `{"watermark":false,"evidence":"无"}`},
		{"wrong_flag_type", "", `{"watermark":"false","uncertain":false,"evidence":"无"}`},
		{"positive_watermark_without_evidence", "", `{"watermark":true,"uncertain":false}`},
		{"uncertain_without_evidence", "", `{"watermark":false,"uncertain":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				content := `{"watermark":false,"lens":false,"case_edge":false,"case_photo":false,"reason":"干净图案"}`
				if calls == 2 {
					content = `{"rounded_panels":false,"layout":"无"}`
					if tc.panel != "" {
						content = tc.panel
					}
				}
				if calls == 3 {
					content = tc.watermark
				}
				w.Write([]byte(respWith(content, "")))
			}))
			defer srv.Close()
			if v := Inspect(cfgFor(srv), []string{"whole"}); v.OK || !v.Fallback {
				t.Fatalf("incomplete decision accepted: %+v", v)
			}
		})
	}
}
