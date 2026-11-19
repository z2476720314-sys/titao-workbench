package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

func publishPatternFiles(files map[string]string) error {
	return publishPatternFilesWithRename(files, os.Rename)
}

func publishPatternFilesWithRename(files map[string]string, rename func(string, string) error) error {
	type artifact struct {
		destination, source, stage string
		old                        os.FileInfo
		oldHash                    string
		committed, retain          bool
	}
	var artifacts []*artifact
	var sources []os.FileInfo
	seen := map[string]bool{}
	keys := make([]string, 0, len(files))
	for target := range files {
		keys = append(keys, target)
	}
	sort.Strings(keys)
	for _, target := range keys {
		destination, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		key, err := normalizedCompletionPath(target)
		if err != nil {
			return err
		}
		if seen[key] {
			return fmt.Errorf("重复的发布目标: %s", target)
		}
		seen[key] = true
		source, err := filepath.Abs(files[target])
		if err != nil {
			return err
		}
		info, err := os.Stat(source)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("发布源不是普通文件: %s", source)
		}
		sources = append(sources, info)
		old, err := os.Lstat(destination)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (!old.Mode().IsRegular() || old.Mode().Perm()&0200 == 0) {
			return fmt.Errorf("发布目标不可替换: %s", destination)
		}
		artifacts = append(artifacts, &artifact{destination: destination, source: source, old: old})
	}
	for _, a := range artifacts {
		if a.old != nil {
			for _, source := range sources {
				if os.SameFile(a.old, source) {
					return fmt.Errorf("发布目标不能覆盖输入文件: %s", a.destination)
				}
			}
		}
	}
	defer func() {
		for _, a := range artifacts {
			if a.stage != "" && !a.retain {
				_ = os.Remove(filepath.Join(a.stage, "new"))
				_ = os.Remove(filepath.Join(a.stage, "old"))
				_ = os.Remove(a.stage)
			}
		}
	}()
	for _, a := range artifacts {
		if err := os.MkdirAll(filepath.Dir(a.destination), 0755); err != nil {
			return err
		}
		stage, err := os.MkdirTemp(filepath.Dir(a.destination), ".pattern-publish-")
		if err != nil {
			return err
		}
		a.stage = stage
		expected, err := inputSHA(a.source)
		if err != nil {
			return err
		}
		fresh := filepath.Join(stage, "new")
		if err := copyFile(a.source, fresh); err != nil {
			return err
		}
		actual, err := inputSHA(fresh)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("发布源在暂存时变化: %s", a.source)
		}
		if a.old != nil {
			old := filepath.Join(stage, "old")
			if err := copyFile(a.destination, old); err != nil {
				return err
			}
			hash, err := inputSHA(old)
			if err != nil {
				return err
			}
			a.oldHash = hash
			if err := os.Chtimes(old, a.old.ModTime(), a.old.ModTime()); err != nil {
				return err
			}
			if err := os.Chmod(old, a.old.Mode().Perm()); err != nil {
				return err
			}
		}
	}
	rollback := func(cause error) error {
		result := cause
		for i := len(artifacts) - 1; i >= 0; i-- {
			a := artifacts[i]
			if !a.committed {
				continue
			}
			var err error
			if a.old == nil {
				err = os.Remove(a.destination)
			} else {
				err = rename(filepath.Join(a.stage, "old"), a.destination)
			}
			if err != nil {
				a.retain = true
				result = errors.Join(result, fmt.Errorf("回滚失败，保留恢复副本%s: %w", filepath.Join(a.stage, "old"), err))
			}
		}
		return result
	}
	for _, a := range artifacts {
		if a.old != nil {
			current, err := inputSHA(a.destination)
			if err != nil {
				return rollback(err)
			}
			if current != a.oldHash {
				return rollback(fmt.Errorf("发布目标在暂存后变化: %s", a.destination))
			}
		} else {
			if _, err := os.Lstat(a.destination); !os.IsNotExist(err) {
				return rollback(fmt.Errorf("发布目标在暂存后被创建: %s", a.destination))
			}
		}
		if err := rename(filepath.Join(a.stage, "new"), a.destination); err != nil {
			return rollback(err)
		}
		a.committed = true
	}
	return nil
}
