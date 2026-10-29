package cfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTuningModelUpgradePreservesPrecheckAndUserSettings(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`{"model":"gpt-6-luna","output_dir":"user-output","no_crop":true}`, "deepseek-v4.1-flash"},
		{`{"model":"gpt-6-luna","tune_model":"custom-tuner","output_dir":"user-output","no_crop":true}`, "custom-tuner"},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.input), 0600); err != nil {
			t.Fatal(err)
		}
		s, err := NewStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		c, err := s.Update(func(c *Config) { c.MaxRounds = 2 })
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(c)
		var saved map[string]any
		json.Unmarshal(b, &saved)
		if saved["tune_model"] != tc.want || c.Model != "gpt-6-luna" || c.OutputDir != "user-output" || !c.NoCropOn() {
			t.Fatalf("upgraded models or user settings changed: tune=%v precheck=%s", saved["tune_model"], c.Model)
		}
	}
}
