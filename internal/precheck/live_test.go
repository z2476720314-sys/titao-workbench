package precheck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	if model := os.Getenv("TITAO_PRECHECK_MODEL"); model != "" {
		settings.Model = model
	}
	var cases []struct {
		ID   int    `json:"id"`
		Path string `json:"path"`
		OK   bool   `json:"ok"`
	}
	if err := json.Unmarshal([]byte(casesJSON), &cases); err != nil || len(cases) == 0 {
		t.Fatal("invalid regression cases")
	}
	var quotaExhausted atomic.Bool
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%03d-%s", tc.ID, filepath.Base(tc.Path)), func(t *testing.T) {
			t.Parallel()
			if quotaExhausted.Load() {
				t.Skip("provider quota exhausted earlier in this run")
			}
			views, err := imgutil.PrecheckViews(tc.Path, settings.ImageMaxSide)
			if err != nil {
				t.Fatal(err)
			}
			v := Inspect(Config{BaseURL: settings.BaseURL, APIKey: settings.APIKey, Model: settings.Model, MaxTokens: settings.MaxTokens, Timeout: time.Duration(settings.TimeoutSec) * time.Second, Temperature: settings.Temperature, Thinking: settings.Thinking}, views)
			if v.Fallback {
				if strings.Contains(fmt.Sprint(v.Err), "insufficient_user_quota") {
					quotaExhausted.Store(true)
				}
				t.Fatalf("unconfirmed: %v", v.Err)
			}
			t.Logf("ok=%t tags=%v reason=%s", v.OK, v.Tags, v.Reason)
			if v.OK != tc.OK {
				t.Fatalf("ok=%t want=%t", v.OK, tc.OK)
			}
		})
	}
}
