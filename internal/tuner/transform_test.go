package tuner

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"titao/internal/judge"
)

func TestJointAdviceRequiresCurrentCameraClassification(t *testing.T) {
	for _, camera := range []string{"", `,"camera_clear":null`, `,"camera_clear":"true"`, `,"camera_clear":false`} {
		raw := `{"good":true,"score":90,"problem":"无","dx":0,"dy":0,"scale_ratio":1` + camera + `}`
		if adv := parseAdvice(raw); adv.Direction != "" || adv.Good {
			t.Fatalf("missing or contradictory current camera classification was accepted: %s", raw)
		}
	}
}

func TestCameraClassificationSurvivesAdviceAndRoundSerialization(t *testing.T) {
	adv := parseAdvice(`{"good":false,"score":55,"problem":"底部截边悬空","camera_clear":true,"dx":0,"dy":0.03,"scale_ratio":1}`)
	data, err := json.Marshal(adv)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if adv.Good || fields["camera_clear"] != true {
		t.Fatal("a clear camera must remain independent of failed edge alignment")
	}
	var round Round
	if err := json.Unmarshal([]byte(`{"camera_clear":true}`), &round); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(round)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["camera_clear"] != true {
		t.Fatal("round history lost the camera classification")
	}
	round.CameraClear = false
	data, err = json.Marshal(round)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["camera_clear"] != false {
		t.Fatal("false camera classification must remain explicit in round history")
	}
}

func TestLegacyCameraClassificationDefaultsConservativelyFromGood(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"good":true,"score":90,"direction":"none"}`, true},
		{`{"score":88,"direction":"none"}`, true},
		{`{"good":false,"score":88,"direction":"none"}`, false},
		{`{"good":false,"score":60,"direction":"down"}`, false},
		{`{"good":false,"camera_clear":true,"score":55,"problem":"截边悬空","direction":"down"}`, true},
	} {
		adv := parseAdvice(tc.raw)
		if adv.Direction == "" || adv.CameraClear != tc.want {
			t.Fatalf("legacy camera classification changed: %s", tc.raw)
		}
	}
	for _, raw := range []string{
		`{"good":true,"camera_clear":false,"score":90,"direction":"none"}`,
		`{"good":true,"camera_clear":null,"score":90,"direction":"none"}`,
	} {
		if adv := parseAdvice(raw); adv.Direction != "" || adv.Good || adv.CameraClear {
			t.Fatalf("contradictory legacy camera classification accepted: %s", raw)
		}
	}
}

func TestCritiqueRepairsMissingCameraClassificationOnce(t *testing.T) {
	for _, repaired := range []bool{false, true} {
		t.Run(fmt.Sprint(repaired), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct {
					Messages []struct {
						Content any `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				texts := []string{body.Messages[0].Content.(string), body.Messages[1].Content.([]any)[0].(map[string]any)["text"].(string)}
				if calls == 2 {
					texts = append(texts, body.Messages[len(body.Messages)-1].Content.(string))
				}
				for _, text := range texts {
					for _, field := range []string{"good", "camera_clear", "score", "problem", "dx", "dy", "scale_ratio"} {
						if !strings.Contains(text, field) {
							t.Errorf("request contract lost %s", field)
						}
					}
				}
				if calls == 1 || !repaired {
					writeReply(w, `{"good":false,"score":55,"problem":"截边悬空","dx":0,"dy":0.03,"scale_ratio":1}`, `{"camera_clear":true}`, "stop")
					return
				}
				writeReply(w, `{"good":false,"camera_clear":true,"score":55,"problem":"截边悬空","dx":0,"dy":0.03,"scale_ratio":1}`, "", "stop")
			}))
			defer srv.Close()
			adv := Critique(testCfg(srv.URL), []byte("fixturejpeg"), judge.Placement{})
			if calls != 2 || adv.Good || adv.Fallback == repaired || adv.CameraClear != repaired {
				t.Fatalf("missing camera field was inferred or not retried: calls=%d advice=%+v", calls, adv)
			}
		})
	}
}

func TestJointAdviceAppliesSmallCombinedChangesFromActualPlacement(t *testing.T) {
	adv := parseAdvice(`{"camera_clear":false,"good":false,"score":72,"problem":"主字靠近镜头","dx":0.003,"dy":0.012,"scale_ratio":0.999}`)
	if adv.Direction == "" || adv.Good {
		t.Fatal("a complete joint adjustment must be recognized as an unsuccessful review")
	}
	scale, x, y, iterate := Plan(adv, 1.2, 0.45, -0.38)
	if !iterate || math.Abs(scale-1.1988) > 1e-12 || math.Abs(x-0.453) > 1e-12 || math.Abs(y-(-0.368)) > 1e-12 {
		t.Fatalf("joint change was clamped or quantized: scale=%g offset=(%g,%g) iterate=%t", scale, x, y, iterate)
	}
}

func TestJointAdviceIdentityPreservesItsQualityDecision(t *testing.T) {
	for _, good := range []bool{false, true} {
		adv := parseAdvice(fmt.Sprintf(`{"camera_clear":true,"good":%t,"score":80,"problem":"约束冲突或无","dx":0,"dy":0,"scale_ratio":1}`, good))
		if adv.Direction == "" || adv.Good != good {
			t.Fatalf("identity adjustment changed the review result: good=%t", good)
		}
		_, _, _, iterate := Plan(adv, 0.2, 0.4, 0.5)
		if iterate {
			t.Fatal("identity adjustment must not create an empty rerender")
		}
	}
}

func TestJointPlanLeavesAbsoluteSafetyLimitsToRenderer(t *testing.T) {
	for _, tc := range []struct{ scale, ratio, want float64 }{{0.2, 0.05, 0.01}, {2, 2, 4}} {
		adv := parseAdvice(fmt.Sprintf(`{"camera_clear":false,"good":false,"score":60,"problem":"调整","dx":1,"dy":-1,"scale_ratio":%g}`, tc.ratio))
		scale, x, y, iterate := Plan(adv, tc.scale, 0.8, -0.7)
		if !iterate || math.Abs(scale-tc.want) > 1e-12 || x != 1.8 || y != -1.7 {
			t.Fatalf("joint adjustment retained legacy absolute clamps: %g,%g,%g", scale, x, y)
		}
	}
}

func TestJointPlanRejectsInvalidManualAdjustmentsAndOverflow(t *testing.T) {
	for _, adjustment := range []Adjustment{
		{DX: math.NaN(), ScaleRatio: 1}, {DY: math.Inf(1), ScaleRatio: 1}, {ScaleRatio: math.Inf(1)},
		{DX: 1.01, ScaleRatio: 1}, {ScaleRatio: 0}, {ScaleRatio: 2.01},
	} {
		_, _, _, iterate := Plan(Advice{Transform: &adjustment}, 1, 0, 0)
		if iterate {
			t.Fatal("invalid manually constructed advice must not reach the renderer")
		}
	}
	for _, advice := range []Advice{
		{Transform: &Adjustment{ScaleRatio: 2}},
		{Good: true, Transform: &Adjustment{ScaleRatio: 0.9}},
		{Fallback: true, Transform: &Adjustment{ScaleRatio: 0.9}},
	} {
		_, _, _, iterate := Plan(advice, math.MaxFloat64, 0, 0)
		if iterate {
			t.Fatal("overflow, failed or already-good reviews must not cause another render")
		}
	}
}

func TestJointAdviceRequiresCompleteFiniteConsistentFields(t *testing.T) {
	for _, raw := range []string{
		`{"camera_clear":false,"score":70,"problem":"偏位","dx":0,"dy":0.01,"scale_ratio":1}`,
		`{"camera_clear":false,"good":false,"problem":"偏位","dx":0,"dy":0.01,"scale_ratio":1}`,
		`{"camera_clear":false,"good":false,"score":70,"dx":0,"dy":0.01,"scale_ratio":1}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dy":0.01,"scale_ratio":1}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dx":0,"scale_ratio":1}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dx":0,"dy":0.01}`,
		`{"camera_clear":false,"good":null,"score":70,"problem":"偏位","dx":0,"dy":0.01,"scale_ratio":1}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":null,"dx":0,"dy":0.01,"scale_ratio":1}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dx":null,"dy":0.01,"scale_ratio":1}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dx":0,"dy":0.01,"scale_ratio":0}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dx":0,"dy":0.01,"scale_ratio":2.01}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dx":1.01,"dy":0.01,"scale_ratio":1}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dx":0,"dy":-1.01,"scale_ratio":1}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dx":1e309,"dy":0.01,"scale_ratio":1}`,
		`{"camera_clear":true,"good":true,"score":90,"problem":"无","dx":0.001,"dy":0,"scale_ratio":1}`,
		`{"camera_clear":true,"good":true,"score":90,"problem":"无","dx":0,"dy":0,"scale_ratio":0.99}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dx":0,"dy":0.01,"scale_ratio":1,"direction":"up","step":0.2}`,
		`{"camera_clear":true,"good":true,"score":90,"problem":"偏位","direction":"bigger","step":0.1}`,
		`{"camera_clear":false,"good":false,"score":70,"problem":"偏位","dx":0,"dy":0.01,"scale_ratio":1`,
	} {
		if adv := parseAdvice(raw); adv.Direction != "" || adv.Good {
			t.Fatalf("incomplete or contradictory adjustment accepted: %s", raw)
		}
	}
}

func TestCritiqueAcceptsJointSSEAndKeepsUsageWithinCompactBudget(t *testing.T) {
	const reply = `{"camera_clear":false,"good":false,"score":73,"problem":"主字靠近镜头","dx":0.012,"dy":0.034,"scale_ratio":0.98}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["max_tokens"] != float64(512) {
			t.Error("caller output budget must remain 512 tokens")
		}
		messages := request["messages"].([]any)
		system := messages[0].(map[string]any)["content"].(string)
		if !strings.Contains(system, "scale_ratio") || strings.Contains(system, "direction") || strings.Contains(system, "step") {
			t.Error("new requests must ask only for the joint numeric contract")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": reply}, "finish_reason": "stop"}}, "usage": map[string]int{"completion_tokens": 101}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
	}))
	defer srv.Close()
	cfg := testCfg(srv.URL)
	cfg.MaxTokens = 512
	adv := Critique(cfg, []byte("fixturejpeg"), judge.Placement{NoCrop: true, FullBleed: true, Scale: 0.876, OffsetX: 0.1234, OffsetY: -0.0456})
	if adv.Fallback || adv.Good || adv.Direction == "" || adv.Usage == nil || adv.Usage.CompletionTokens == nil || *adv.Usage.CompletionTokens != 101 {
		t.Fatalf("joint SSE result was not accepted: fallback=%t good=%t direction=%s usage=%+v", adv.Fallback, adv.Good, adv.Direction, adv.Usage)
	}
}

func TestJointCritiqueRetriesFormatWithoutBorrowingReasoningFields(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			writeReply(w, `{"camera_clear":false,"good":false,"score":70,"problem":"主字被挡","dx":0.01}`, `{"camera_clear":true,"good":true,"score":99,"problem":"无","dx":0,"dy":0,"scale_ratio":1}`, "stop")
			return
		}
		writeReply(w, `{"camera_clear":false,"good":false,"score":65,"problem":"主字被挡","dx":0.02,"dy":0.03,"scale_ratio":0.97}`, "", "stop")
	}))
	defer srv.Close()
	adv := Critique(testCfg(srv.URL), []byte("fixturejpeg"), judge.Placement{})
	if calls != 2 || adv.Fallback || adv.Good || adv.Transform == nil || adv.Transform.DX != 0.02 || adv.Transform.DY != 0.03 || adv.Transform.ScaleRatio != 0.97 {
		t.Fatalf("numeric format retry borrowed or discarded fields: calls=%d advice=%+v", calls, adv)
	}
}

func TestPlacementDescriptionAllowsContainmentCorrectionAndUsesCanvasUnits(t *testing.T) {
	text := (judge.Placement{FullBleed: true, FillTop: true, TransparentPadding: true, NoCrop: true, Scale: 0.876, OffsetX: 0.1234, OffsetY: -0.0456}).Description()
	if strings.Contains(text, "禁止缩小") || strings.Contains(text, "仍须满铺") || strings.Contains(text, "图案原始画布方向") {
		t.Fatal("containment correction must not be forbidden by legacy full-bleed wording")
	}
	if !strings.Contains(text, "实际已应用") || !strings.Contains(text, "壳面画布") || !strings.Contains(text, "安全余量") {
		t.Fatal("the model needs the applied placement and measured canvas-relative correction semantics")
	}
}
