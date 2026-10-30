package tuner

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/imgutil"
	"titao/internal/judge"
)

type replyMetadata struct {
	Status           int    `json:"status"`
	Model            string `json:"model"`
	FinishReason     string `json:"finish_reason"`
	ContentBytes     int    `json:"content_bytes"`
	ReasoningBytes   int    `json:"reasoning_bytes"`
	CompletionTokens *int   `json:"completion_tokens"`
}

type replyObserver struct {
	http.RoundTripper
	replies *[]replyMetadata
}

func (o replyObserver) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := o.RoundTripper.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	var data struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens *int `json:"completion_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(b, &data)
	m := replyMetadata{Status: resp.StatusCode, Model: data.Model, CompletionTokens: data.Usage.CompletionTokens}
	if len(data.Choices) > 0 {
		m.FinishReason = data.Choices[0].FinishReason
		m.ContentBytes = len(data.Choices[0].Message.Content)
		m.ReasoningBytes = len(data.Choices[0].Message.Reasoning)
	}
	*o.replies = append(*o.replies, m)
	return resp, nil
}

// Opt-in protocol QA records response structure, never credentials or model reasoning.
func TestLiveTunerReply(t *testing.T) {
	manifest, config := os.Getenv("TITAO_TUNER_QA"), os.Getenv("TITAO_PRECHECK_CONFIG")
	if manifest == "" || config == "" {
		t.Skip("live tuner inputs not configured")
	}
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	settings := cfg.Default()
	if json.Unmarshal(b, &settings) != nil {
		t.Fatal("invalid runtime configuration")
	}
	var plan struct {
		Output string `json:"output"`
		Cases  []struct {
			Source  string `json:"source"`
			Preview string `json:"preview"`
		} `json:"cases"`
	}
	b, err = os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(b, &plan) != nil || plan.Output == "" || len(plan.Cases) == 0 {
		t.Fatal("invalid live QA plan")
	}
	if err := os.MkdirAll(plan.Output, 0700); err != nil {
		t.Fatal(err)
	}
	key := settings.TuneAPIKey
	if key == "" {
		key = settings.APIKey
	}
	model := settings.TuneModel
	if model == "" {
		model = settings.Model
	}
	var results []map[string]any
	for i, tc := range plan.Cases {
		source, err := imgutil.ThumbDataURL(tc.Source, settings.ImageMaxSide)
		if err != nil {
			t.Fatal(err)
		}
		jpeg, err := imgutil.FirstCellJPEG(tc.Preview, settings.CellSize, settings.ImageMaxSide, 90)
		if err != nil {
			t.Fatal(err)
		}
		var replies []replyMetadata
		original := http.DefaultTransport
		http.DefaultTransport = replyObserver{original, &replies}
		start := time.Now()
		adv := func() Advice {
			defer func() { http.DefaultTransport = original }()
			return Critique(Config{BaseURL: settings.BaseURL, APIKey: key, Model: model, MaxTokens: settings.MaxTokens, Timeout: time.Duration(settings.TimeoutSec) * time.Second, Temperature: settings.Temperature, Thinking: settings.Thinking}, jpeg, judge.Placement{FullBleed: true, Fit: "cover", NoCrop: true, Scale: 1, SourceDataURL: source})
		}()
		result := map[string]any{"case": i + 1, "source": tc.Source, "seconds": time.Since(start).Seconds(), "fallback": adv.Fallback, "good": adv.Good, "score": adv.Score, "direction": adv.Direction, "responses": replies}
		if adv.Err != nil {
			result["error_type"] = "review_unconfirmed"
		}
		results = append(results, result)
		metadata, _ := json.Marshal(replies)
		t.Logf("case=%d fallback=%t direction=%s responses=%s", i+1, adv.Fallback, adv.Direction, metadata)
		if adv.Fallback {
			t.Errorf("case=%d review unconfirmed", i+1)
		}
	}
	b, err = json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.Output, "reply-results.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
