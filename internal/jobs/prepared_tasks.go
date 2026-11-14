package jobs

import (
	"fmt"
	"path/filepath"
)

// Capture the approved input before completion keys are computed; later imports do not replace it.
func (e *Engine) prepareTaskInputs(tasks []*Task) error {
	if e.store == nil {
		return nil
	}
	catalog, err := LoadPreparedCatalog(filepath.Dir(e.store.Path()))
	if err != nil {
		return err
	}
	resolved := map[string]*PreparedArtwork{}
	for _, t := range tasks {
		if t.OriginalPattern != "" {
			continue
		}
		ref, ok := resolved[t.Pattern]
		if !ok {
			ref, err = catalog.Resolve(t.Pattern)
			if err != nil {
				return fmt.Errorf("%s 重排副本不可用: %w", t.PatternName, err)
			}
			resolved[t.Pattern] = ref
		}
		if ref != nil {
			t.OriginalPattern, t.OriginalSHA256, t.PreparedSHA256 = t.Pattern, ref.SourceSHA256, ref.PreparedSHA256
			t.KeepWhole = ref.KeepWhole
			t.Pattern = ref.Path
		}
	}
	return nil
}

func (e *Engine) validatePreparedTask(t *Task) error {
	if t.OriginalPattern == "" && t.OriginalSHA256 == "" && t.PreparedSHA256 == "" {
		return nil
	}
	if e.store == nil {
		return fmt.Errorf("配置未加载，无法确认重排副本")
	}
	return ValidatePreparedInput(filepath.Dir(e.store.Path()), t.OriginalPattern, t.OriginalSHA256, t.Pattern, t.PreparedSHA256)
}
