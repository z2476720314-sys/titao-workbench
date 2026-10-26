package updater

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestUpdateProcessHelper(t *testing.T) {
	if os.Getenv("TITAO_UPDATE_HELPER") != "1" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyReplace(exe, os.Getenv("TITAO_UPDATE_NEW")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
}

func TestRealReplacementPreservesUserFiles(t *testing.T) {
	if os.Getenv("TITAO_TEST_UPDATER") != "1" {
		t.Skip("real updater integration not enabled")
	}
	dir := filepath.Join(t.TempDir(), "program 100% & 配置")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "workbench.exe")
	if err := os.WriteFile(old, data, 0700); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "fixture.go")
	src := `package main
import("os";"path/filepath")
func main(){p,_:=os.Executable();os.WriteFile(filepath.Join(filepath.Dir(p),"updated.marker"),[]byte("new"),0600)}
`
	if err := os.WriteFile(fixture, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(dir, "new.exe")
	build := exec.Command("go", "build", "-ldflags=-H=windowsgui", "-o", next, fixture)
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, b)
	}
	wantHash, err := FileSHA256(next)
	if err != nil {
		t.Fatal(err)
	}
	keep := []byte("{\"saved_setting\":\"keep\"}\n")
	for _, name := range []string{"config.json", "queue.json", "precheck.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), keep, 0600); err != nil {
			t.Fatal(err)
		}
	}
	child := exec.Command(old, "-test.run=^TestUpdateProcessHelper$", "-test.v")
	child.Env = append(os.Environ(), "TITAO_UPDATE_HELPER=1", "TITAO_UPDATE_NEW="+next)
	child.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if b, err := child.CombinedOutput(); err != nil {
		t.Fatalf("update helper: %v %s", err, b)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "updated.marker")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("updated program did not restart")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got, err := FileSHA256(old); err != nil || got != wantHash {
		t.Fatalf("replacement hash: %s %v", got, err)
	}
	for _, name := range []string{"config.json", "queue.json", "precheck.jsonl"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, keep) {
			t.Fatalf("user file changed: %s", name)
		}
	}
}
