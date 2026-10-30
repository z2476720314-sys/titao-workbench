package tuner

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"titao/internal/judge"
)

func writeReply(w http.ResponseWriter, content, reasoning, finish string) {
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{"content": content, "reasoning_content": reasoning}}}})
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
