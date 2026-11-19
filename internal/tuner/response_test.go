package tuner

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"titao/internal/judge"
	"titao/internal/psx"
)

func writeReply(w http.ResponseWriter, content, reasoning, finish string) {
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{"content": content, "reasoning_content": reasoning}}}})
}

func TestCritiqueRechecksRejectedNoOpWithSameImages(t *testing.T) {
	for _, repaired := range []bool{true, false} {
		t.Run(fmt.Sprint(repaired), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				images := 0
				for _, m := range body["messages"].([]any) {
					if parts, ok := m.(map[string]any)["content"].([]any); ok {
						for _, part := range parts {
							if part.(map[string]any)["type"] == "image_url" {
								images++
							}
						}
					}
				}
				// 原图参考 + 本轮成品 = 2 张；不再有额外角度
				if images != 2 {
					t.Errorf("repair lost original or the rendered view: %d", images)
				}
				reply := `{"good":false,"camera_clear":false,"score":48,"problem":"主脸被镜头遮挡","dx":0,"dy":0,"scale_ratio":1}`
				if calls == 2 && repaired {
					reply = `{"good":false,"camera_clear":false,"score":48,"problem":"主脸被镜头遮挡","dx":0,"dy":0.12,"scale_ratio":0.9}`
				}
				writeReply(w, reply, "", "stop")
			}))
			defer srv.Close()
			// 每轮只评审一张图，不再有第二视角参数
				adv := Critique(testCfg(srv.URL), []byte("view1"), judge.Placement{SourceDataURL: "source"})
			if calls != 2 || adv.Fallback || adv.Good || adv.Score != 48 {
				t.Fatalf("rejected no-op not rechecked: calls=%d advice=%+v", calls, adv)
			}
			_, _, _, iterate := Plan(adv, 1, 0, 0)
			if iterate != repaired {
				t.Fatalf("repair action=%t want %t", iterate, repaired)
			}
		})
	}
}

// 遮挡必须在这一轮就报出来，不能靠"别的角度合格"掩盖。
// 现在每轮只评审一张，所以这里验证：单张被判不合格时，Advice 必须带着调整返回。
func TestBlockedViewIsReportedNotApproved(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		b, _ := json.Marshal(body["messages"])
		// 原图参考必须和成品一起发过去，否则模型无从比对内容是否被切
		if !strings.Contains(string(b), base64.StdEncoding.EncodeToString([]byte("source-ref"))) {
			t.Error("review request dropped the source reference image")
		}
		reply := `{"good":false,"camera_clear":false,"score":50,"problem":"眼睛被镜头挡住","dx":0,"dy":0.2,"scale_ratio":0.85}`
		writeReply(w, reply, "", "stop")
	}))
	defer srv.Close()
	adv := Critique(testCfg(srv.URL), []byte("single-view"), judge.Placement{SourceDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("source-ref"))})
	if adv.Good || adv.CameraClear || adv.Fallback || adv.Score != 50 || adv.Transform == nil || adv.Transform.DY != .2 {
		t.Fatalf("blocked view was not reported: %+v calls=%d", adv, calls)
	}
}

func TestSingleAngleApprovalUsesOnlyItsOwnBaseline(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body["messages"])
		if calls > 1 && strings.Contains(string(b), "增量以03号") {
			t.Error("single angle still uses group reference")
		}
		if calls == 3 && (!strings.Contains(string(b), "scale=0.8") || !strings.Contains(string(b), "offset_y=0.2")) {
			t.Error("single angle lost actual parameters")
		}
		writeReply(w, `{"good":true,"camera_clear":true,"score":94,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`, "", "stop")
	}))
	defer srv.Close()
	adv := Critique(testCfg(srv.URL), []byte("v1"), judge.Placement{AppliedSummary: "增量以03号实际参数为参考", ViewPlacements: []psx.AppliedPlacement{{Cell: 0, Scale: 1}, {Cell: 3, Scale: .8, OffsetY: .2}}})
	if !adv.Good || calls != 1 {
		t.Fatalf("single-view approval failed: %+v calls=%d", adv, calls)
	}
}

func TestCameraOnlyClearNoOpDoesNotAskForAestheticAdjustment(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		reply := `{"good":true,"camera_clear":true,"score":94,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`
		if calls > 1 {
			reply = `{"good":false,"camera_clear":true,"score":70,"problem":"正常留白","dx":0,"dy":0,"scale_ratio":1}`
		}
		writeReply(w, reply, "", "stop")
	}))
	defer srv.Close()
	adv := Critique(testCfg(srv.URL), []byte("v1"), judge.Placement{NoCrop: true, FrameVerified: true})
	if !adv.Good || !adv.CameraClear || calls != 1 {
		t.Fatalf("clear camera caused unrelated repair: calls=%d advice=%+v", calls, adv)
	}
}

func TestLowScoreCannotDeclareCurrentPictureApproved(t *testing.T) {
	for _, reply := range []string{`{"good":true,"camera_clear":true,"score":48,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`, `{"good":true,"camera_clear":true,"score":80,"problem":"无","direction":"none"}`} {
		adv := parseAdvice(reply)
		if adv.Good || adv.Direction != "" {
			t.Fatalf("low score accepted as approved: %+v", adv)
		}
	}
}

func TestCritiqueRepairsIncompleteReplyOnceUsingSameImages(t *testing.T) {
	for _, tc := range []struct{ name, content, reasoning, finish string }{
		{"analysis only", "", "We need evaluate final product. Need output one line JSON.", "stop"},
		{"blank content", "   ", "", "stop"},
		{"missing direction", `{"good":true,"score":90}`, "", "stop"},
		{"truncated content", `{"good":true,"score":99,"direction":"none"`, "", "length"},
		{"truncated reasoning", "", `Example: {"good":true,"score":99,"direction":"none"}`, "length"},
		{"example in reasoning", "", `For example {"good":true,"score":99,"direction":"none"}. I still need to compare the images.`, "stop"},
		{"example in final content", `For example {"good":true,"score":99,"direction":"none"}.`, "", "stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid request body")
				}
				messages := body["messages"].([]any)
				images := 0
				for _, m := range messages {
					if content, ok := m.(map[string]any)["content"].([]any); ok {
						for _, part := range content {
							if part.(map[string]any)["type"] == "image_url" {
								images++
							}
						}
					}
				}
				if images != 2 {
					t.Errorf("request %d lost original/source images: %d", calls, images)
				}
				if calls == 1 {
					writeReply(w, tc.content, tc.reasoning, tc.finish)
					return
				}
				writeReply(w, `{"good":false,"score":60,"problem":"主要文字被挡","direction":"right","step":0.1}`, "", "stop")
			}))
			defer srv.Close()
			adv := Critique(testCfg(srv.URL), []byte("fakejpeg"), judge.Placement{SourceDataURL: "data:image/png;base64,c3Jj"})
			if calls != 2 || adv.Fallback || adv.Good || adv.Direction != DirRight || adv.Score != 60 {
				t.Fatalf("calls=%d fallback=%t good=%t direction=%s score=%d", calls, adv.Fallback, adv.Good, adv.Direction, adv.Score)
			}
		})
	}
}

func TestCritiqueInvalidReplyRemainsUnreviewedWithoutLeakingAnalysis(t *testing.T) {
	calls := 0
	const analysis = "We need evaluate final product. Need compare original reference"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; writeReply(w, "", analysis, "stop") }))
	defer srv.Close()
	adv := Critique(testCfg(srv.URL), []byte("fakejpeg"), judge.Placement{})
	if calls != 2 || !adv.Fallback || adv.Good || adv.Err == nil {
		t.Fatalf("calls=%d fallback=%t good=%t", calls, adv.Fallback, adv.Good)
	}
	if strings.Contains(adv.Err.Error(), analysis) {
		t.Fatal("user error contains model analysis")
	}
}

func TestCritiqueFinalContentDoesNotBorrowFieldsFromReasoning(t *testing.T) {
	srv := fakeChat(t, `{"good":false,"score":60,"direction":"right"}`, `{"good":true,"score":99,"direction":"none","problem":"analysis only","step":0.3}`)
	defer srv.Close()
	adv := Critique(testCfg(srv.URL), []byte("fakejpeg"), judge.Placement{})
	if adv.Fallback || adv.Good || adv.Score != 60 || adv.Direction != DirRight || adv.Problem != "" || adv.Step != 0.1 {
		t.Fatalf("fields from different replies mixed: good=%t score=%d direction=%s step=%g", adv.Good, adv.Score, adv.Direction, adv.Step)
	}
}

func TestParseAdviceRequiresOneCompleteTypedObject(t *testing.T) {
	for _, raw := range []string{
		`{"good":true,"score":99,"direction":"none"`,
		`{"good":"true","score":99,"direction":"none"}`,
		`{"good":true,"score":"99","direction":"none"}`,
		`{"good":true,"score":101,"direction":"none"}`,
		`{"good":true,"direction":"none"}`,
		`{"good":true,"score":99} {"direction":"none"}`,
		`Example: {"good":true,"score":99,"direction":"none"}`,
	} {
		if adv := parseAdvice(raw); adv.Direction != "" || adv.Good {
			t.Fatalf("malformed review accepted: %s", raw)
		}
	}
	adv := parseAdvice("```json\n" + `{"good":false,"score":70,"problem":"文字\"偏位\"","direction":"right"}` + "\n```")
	if adv.Direction != DirRight || adv.Problem != "文字\"偏位\"" {
		t.Fatalf("valid JSON string was not decoded: %q", adv.Problem)
	}
}

func TestCritiqueDoesNotRepairHTTPPermissionError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "not allowed", http.StatusForbidden)
	}))
	defer srv.Close()
	adv := Critique(testCfg(srv.URL), []byte("fakejpeg"), judge.Placement{})
	if calls != 1 || !adv.Fallback || adv.Good {
		t.Fatalf("calls=%d fallback=%t", calls, adv.Fallback)
	}
}

func TestCritiqueRepairSharesOriginalTimeout(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeReply(w, "invalid", "", "stop")
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
			writeReply(w, `{"good":true,"score":90,"direction":"none"}`, "", "stop")
		}
	}))
	defer srv.Close()
	settings := testCfg(srv.URL)
	settings.Timeout = 100 * time.Millisecond
	start := time.Now()
	adv := Critique(settings, []byte("fakejpeg"), judge.Placement{})
	if calls.Load() != 2 || !adv.Fallback || time.Since(start) > 600*time.Millisecond {
		t.Fatalf("calls=%d fallback=%t elapsed=%v", calls.Load(), adv.Fallback, time.Since(start))
	}
}
