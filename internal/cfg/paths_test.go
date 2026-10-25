package cfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNewConfigLeavesDirectoriesUnselected(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []Config{s.Get(), func() Config {
		var persisted Config
		data, err := os.ReadFile(filepath.Join(dir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &persisted); err != nil {
			t.Fatal(err)
		}
		return persisted
	}()} {
		if c.TemplateDir != "" || c.PatternDir != "" || c.OutputDir != "" {
			t.Fatal("first launch must leave user directories unselected")
		}
	}
}

func TestDirectoryChoicesSurviveSaveAndReload(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(t.TempDir(), "自选目录"), ""} {
		_, err := s.Update(func(c *Config) { c.TemplateDir, c.PatternDir, c.OutputDir = path, path, path })
		if err != nil {
			t.Fatal(err)
		}
		reloaded, err := NewStore(filepath.Dir(s.Path()))
		if err != nil {
			t.Fatal(err)
		}
		c := reloaded.Get()
		if c.TemplateDir != path || c.PatternDir != path || c.OutputDir != path {
			t.Fatal("saving or reloading replaced the user's directory selection")
		}
	}
}
