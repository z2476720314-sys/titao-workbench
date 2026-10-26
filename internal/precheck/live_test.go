package precheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"titao/internal/cfg"
	"titao/internal/imgutil"
)

// Opt-in regression inputs and credentials are supplied at runtime, never stored here.
func TestLivePrecheck(t *testing.T) {
	config, casesJSON := os.Getenv("TITAO_PRECHECK_CONFIG"), os.Getenv("TITAO_PRECHECK_CASES")
	if config == "" || casesJSON == "" {
		t.Skip("live precheck inputs not configured")
	}
	b, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	settings := cfg.Default()
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal("invalid runtime config")
	}
	var cases []struct {
		Path string `json:"path"`
		OK   bool   `json:"ok"`
	}
	if err := json.Unmarshal([]byte(casesJSON), &cases); err != nil || len(cases) == 0 {
		t.Fatal("invalid regression cases")
	}
	for _, tc := range cases {
		t.Run(filepath.Base(tc.Path), func(t *testing.T) {
			thumb, err := imgutil.ThumbDataURL(tc.Path, settings.ImageMaxSide)
			if err != nil {
				t.Fatal(err)
			}
			v := Check(Config{BaseURL: settings.BaseURL, APIKey: settings.APIKey, Model: settings.Model, MaxTokens: settings.MaxTokens, Timeout: time.Duration(settings.TimeoutSec) * time.Second, Temperature: settings.Temperature, Thinking: settings.Thinking}, thumb)
			if v.Fallback {
				t.Fatalf("unconfirmed: %v", v.Err)
			}
			t.Logf("ok=%t tags=%v reason=%s", v.OK, v.Tags, v.Reason)
			if v.OK != tc.OK {
				t.Fatalf("ok=%t want=%t", v.OK, tc.OK)
			}
		})
	}
}
