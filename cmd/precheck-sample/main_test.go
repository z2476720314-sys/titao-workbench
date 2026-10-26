package main

import (
	"encoding/json"
	"flag"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"titao/internal/cfg"
)

func TestCLIHelper(t *testing.T) {
	if os.Getenv("TITAO_SAMPLE_TEST") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"precheck-sample"}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("precheck-sample", flag.ExitOnError)
	main()
	os.Exit(0)
}

func invoke(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLIHelper$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "TITAO_SAMPLE_TEST=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRejectInvalidArguments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.png"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"zeroCount", []string{"-n", "0"}, "-n"},
		{"negativeCount", []string{"-n", "-1"}, "-n"},
		{"negativeStart", []string{"-start", "-1"}, "-start"},
		{"pastEnd", []string{"-start", "1"}, "-start"},
		{"negativeStride", []string{"-stride", "-1"}, "-stride"},
		{"unexpected", []string{"extra"}, "多余参数"},
		{"missingConfig", nil, "config.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"-config", dir, "-dir", dir}, tc.args...)
			out, err := invoke(t, args...)
			if err == nil || !strings.Contains(out, tc.want) || strings.Contains(out, "panic:") {
				t.Fatalf("want nonzero validation error %q, got %v: %s", tc.want, err, out)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("invalid invocation created config: %v", err)
	}
}

func TestReportsFailuresAndPreservesRecords(t *testing.T) {
	for _, tc := range []struct {
		name            string
		status          int
		brokenImage     bool
		recordDirectory bool
		wantError       bool
	}{
		{"success", 200, false, false, false},
		{"modelFailure", 503, false, false, true},
		{"imageFailure", 200, true, false, true},
		{"recordFailure", 200, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			library := filepath.Join(dir, "library")
			if err := os.Mkdir(library, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(library, "input.png")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.brokenImage {
				if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"watermark\":false,\"lens\":false,\"case_edge\":false,\"case_photo\":false,\"reason\":\"clean\"}"}}]}`))
			}))
			defer srv.Close()
			c := cfg.Default()
			c.BaseURL = srv.URL
			c.APIKey = "dummy"
			data, _ := json.Marshal(c)
			if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"precheck.jsonl", "precheck.sample.jsonl"} {
				if name == "precheck.sample.jsonl" && tc.recordDirectory {
					if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte("existing-record\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := invoke(t, "-config", dir, "-dir", library)
			if (err != nil) != tc.wantError {
				t.Fatalf("want error=%v, got %v: %s", tc.wantError, err, out)
			}
			for _, name := range []string{"precheck.jsonl", "precheck.sample.jsonl"} {
				if name == "precheck.sample.jsonl" && tc.recordDirectory {
					continue
				}
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || !strings.HasPrefix(string(got), "existing-record\n") {
					t.Fatalf("record overwritten: %s %v", name, err)
				}
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("source image changed")
			}
		})
	}
}
