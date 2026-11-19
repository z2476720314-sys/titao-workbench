package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func publicationFixture(t *testing.T, missingFirst bool) (map[string]string, []string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	in, out := filepath.Join(dir, "input"), filepath.Join(dir, "output")
	for _, path := range []string{in, out} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	files, before := map[string]string{}, map[string]string{}
	var targets []string
	for i, name := range []string{"01_canvas.png", "02_canvas.json", "03_black.jpg", "04_white.jpg"} {
		src, dst := filepath.Join(in, name), filepath.Join(out, name)
		if err := os.WriteFile(src, []byte("new:"+name), 0600); err != nil {
			t.Fatal(err)
		}
		if !missingFirst || i != 0 {
			before[dst] = "old:" + name
			if err := os.WriteFile(dst, []byte(before[dst]), 0600); err != nil {
				t.Fatal(err)
			}
		}
		files[dst] = src
		targets = append(targets, dst)
	}
	return files, targets, before
}

func publicationContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("%s: got %q, %v; want %q", path, data, err, want)
	}
}

func publicationRestored(t *testing.T, targets []string, before map[string]string) {
	t.Helper()
	for _, dst := range targets {
		if want, ok := before[dst]; ok {
			publicationContent(t, dst, want)
		} else if _, err := os.Lstat(dst); !os.IsNotExist(err) {
			t.Fatalf("new target retained after rollback: %s %v", dst, err)
		}
	}
}

func publicationNoStage(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".pattern-publish-*"))
	if err != nil || len(matches) > 0 {
		t.Fatalf("publication staging retained: %v %v", matches, err)
	}
}

func TestPublishPatternFilesPublishesCompleteSet(t *testing.T) {
	files, targets, _ := publicationFixture(t, true)
	untouched := filepath.Join(filepath.Dir(targets[0]), "unselected.jpg")
	if err := os.WriteFile(untouched, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishPatternFiles(files); err != nil {
		t.Fatal(err)
	}
	for dst, src := range files {
		want := "new:" + filepath.Base(src)
		publicationContent(t, dst, want)
		publicationContent(t, src, want)
	}
	publicationContent(t, untouched, "keep")
	publicationNoStage(t, filepath.Dir(targets[0]))
}

func TestPublishPatternFilesStagesEveryFileBeforeReplacing(t *testing.T) {
	files, targets, before := publicationFixture(t, false)
	checked := false
	err := publishPatternFilesWithRename(files, func(src, dst string) error {
		if !checked {
			checked = true
			publicationRestored(t, targets, before)
			stages, err := filepath.Glob(filepath.Join(filepath.Dir(targets[0]), ".pattern-publish-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(stages) != 4 {
				t.Fatalf("only %d artifacts staged before publish", len(stages))
			}
			var old, new int
			for _, stage := range stages {
				data, err := os.ReadFile(filepath.Join(stage, "new"))
				if err != nil || !strings.HasPrefix(string(data), "new:") {
					t.Fatalf("incomplete new artifact: %s %v", stage, err)
				}
				new++
				data, err = os.ReadFile(filepath.Join(stage, "old"))
				if err != nil || !strings.HasPrefix(string(data), "old:") {
					t.Fatalf("incomplete rollback artifact: %s %v", stage, err)
				}
				old++
			}
			if old != 4 || new != 4 {
				t.Fatal("incomplete stage")
			}
		}
		return os.Rename(src, dst)
	})
	if err != nil || !checked {
		t.Fatalf("publish did not use complete staging: %v", err)
	}
	publicationNoStage(t, filepath.Dir(targets[0]))
}

func TestPublishPatternFilesRollsBackRenameFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			files, targets, before := publicationFixture(t, missing)
			failure := errors.New("target occupied")
			err := publishPatternFilesWithRename(files, func(src, dst string) error {
				if filepath.Base(src) == "new" && dst == targets[2] {
					return failure
				}
				return os.Rename(src, dst)
			})
			if !errors.Is(err, failure) {
				t.Fatalf("rename failure was hidden: %v", err)
			}
			publicationRestored(t, targets, before)
			publicationNoStage(t, filepath.Dir(targets[0]))
		})
	}
}

func TestPublishPatternFilesPreservesBackupWhenRollbackFails(t *testing.T) {
	files, targets, before := publicationFixture(t, false)
	commitErr, restoreErr := errors.New("commit blocked"), errors.New("restore blocked")
	var backup string
	err := publishPatternFilesWithRename(files, func(src, dst string) error {
		if filepath.Base(src) == "new" && dst == targets[1] {
			return commitErr
		}
		if filepath.Base(src) == "old" && dst == targets[0] {
			backup = src
			return restoreErr
		}
		return os.Rename(src, dst)
	})
	if !errors.Is(err, commitErr) || !errors.Is(err, restoreErr) || backup == "" || !strings.Contains(err.Error(), backup) {
		t.Fatalf("lost rollback cause or recovery path: %v, backup %q", err, backup)
	}
	publicationContent(t, backup, before[targets[0]])
	publicationContent(t, targets[0], "new:"+filepath.Base(targets[0]))
	for _, dst := range targets[1:] {
		publicationContent(t, dst, before[dst])
	}
}

func TestPublishPatternFilesRejectsInvalidPathsBeforeChangingTargets(t *testing.T) {
	for _, kind := range []string{"missing-source", "directory-source", "directory-target", "readonly-target", "same-path", "hardlink", "cross-source"} {
		t.Run(kind, func(t *testing.T) {
			files, targets, before := publicationFixture(t, false)
			switch kind {
			case "missing-source":
				files[targets[3]] = filepath.Join(t.TempDir(), "missing")
			case "directory-source":
				files[targets[3]] = t.TempDir()
			case "directory-target":
				if err := os.Remove(targets[3]); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(targets[3], 0755); err != nil {
					t.Fatal(err)
				}
				delete(before, targets[3])
				targets = targets[:3]
			case "readonly-target":
				if err := os.Chmod(targets[3], 0444); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(targets[3], 0600) })
			case "same-path":
				files[targets[3]] = targets[3]
			case "hardlink":
				if err := os.Remove(targets[3]); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(files[targets[3]], targets[3]); err != nil {
					t.Fatal(err)
				}
				before[targets[3]] = "new:" + filepath.Base(targets[3])
			case "cross-source":
				files[targets[0]] = targets[3]
			}
			if err := publishPatternFiles(files); err == nil {
				t.Fatal("invalid publication accepted")
			}
			publicationRestored(t, targets, before)
			publicationNoStage(t, filepath.Dir(targets[0]))
		})
	}
}

func TestPublishPatternFilesCreatesMissingParents(t *testing.T) {
	files, targets, _ := publicationFixture(t, true)
	dst := filepath.Join(filepath.Dir(targets[0]), "nested", "deep", "canvas.png")
	if err := publishPatternFiles(map[string]string{dst: files[targets[0]]}); err != nil {
		t.Fatal(err)
	}
	publicationContent(t, dst, "new:"+filepath.Base(targets[0]))
	publicationNoStage(t, filepath.Dir(dst))
}
