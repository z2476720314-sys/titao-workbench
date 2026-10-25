package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"titao/internal/jobs"
)

func TestCLIHelper(t *testing.T) {
	if os.Getenv("TITAO_PROBE_TEST") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"probe"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("probe", flag.ExitOnError)
	main()
	os.Exit(0)
}

func TestCheckResultsRequiresSuccessfulFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "result.jpg")
	if err := os.WriteFile(file, []byte("result"), 0600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.jpg")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		tasks     []*jobs.Task
		wantError bool
	}{
		{"none", nil, true},
		{"failed", []*jobs.Task{{Phase: jobs.PhaseFailed}}, true},
		{"canceled", []*jobs.Task{{Phase: jobs.PhaseCanceled}}, true},
		{"missing", []*jobs.Task{{Phase: jobs.PhaseDone, Files: []string{filepath.Join(dir, "missing.jpg")}}}, true},
		{"empty", []*jobs.Task{{Phase: jobs.PhaseDone, Files: []string{empty}}}, true},
		{"directory", []*jobs.Task{{Phase: jobs.PhaseDone, Files: []string{dir}}}, true},
		{"noFiles", []*jobs.Task{{Phase: jobs.PhaseDone}}, true},
		{"done", []*jobs.Task{{Phase: jobs.PhaseDone, Files: []string{file}}}, false},
		{"skipped", []*jobs.Task{{Phase: jobs.PhaseSkipped}}, true},
		{"doneAndSkipped", []*jobs.Task{{Phase: jobs.PhaseDone, Files: []string{file}}, {Phase: jobs.PhaseSkipped}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkResults(tc.tasks); (err != nil) != tc.wantError {
				t.Fatalf("want error=%v, got %v", tc.wantError, err)
			}
		})
	}
}

func TestRejectInvalidArguments(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "input.png")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"-config", dir, "-pattern", file, "-template", file, "-out", filepath.Join(dir, "output")}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"unexpected", []string{"-config", dir, "-pattern", "missing", "-template", "missing", "-out", dir, "extra"}, "多余参数"},
		{"missing", nil, "缺少参数"},
		{"directoryPattern", []string{"-config", dir, "-pattern", dir, "-template", file, "-out", dir}, "图案必须是文件"},
		{"directoryTemplate", []string{"-config", dir, "-pattern", file, "-template", dir, "-out", dir}, "模板必须是文件"},
		{"missingConfig", base, "config.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--"}, tc.args...)...)
			cmd.Env = append(os.Environ(), "TITAO_PROBE_TEST=1")
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) || strings.Contains(string(out), "panic:") {
				t.Fatalf("want nonzero validation error %q, got %v: %s", tc.want, err, out)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid invocation created config: %v", err)
	}
}
