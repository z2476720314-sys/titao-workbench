package fileutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRevealWindowsLive(t *testing.T) {
	if os.Getenv("TITAO_REVEAL_LIVE") != "1" {
		t.Skip("native Shell integration is opt-in")
	}
	dir := t.TempDir()
	t.Cleanup(func() {
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", `$sh=New-Object -ComObject Shell.Application;foreach($w in $sh.Windows()){try{if($w.Document.Folder.Self.Path -ieq $env:TITAO_TEST_FOLDER){$w.Quit()}}catch{}}`)
		cmd.Env = append(os.Environ(), "TITAO_TEST_FOLDER="+dir)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("close test Explorer window: %v %s", err, out)
		}
	})
	for _, name := range []string{"中文 Кот's [原图], & # 01.jpg", "第二张@WMTAI1654_02.jpg"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := Reveal(filepath.ToSlash(path)); err != nil {
			t.Fatal(err)
		}
		// Read-only verification; this script never opens windows or changes selection.
		script := `$ErrorActionPreference='Stop';$sh=New-Object -ComObject Shell.Application;for($i=0;$i -lt 30;$i++){foreach($w in $sh.Windows()){try{foreach($item in $w.Document.SelectedItems()){if($item.Path -ieq $env:TITAO_SELECTED_TARGET){Write-Output 'verified';exit 0}}}catch{}};Start-Sleep -Milliseconds 100};exit 2`
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
		cmd.Env = append(os.Environ(), "TITAO_SELECTED_TARGET="+path)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "verified" {
			t.Fatalf("Shell did not select requested file: %v %s", err, out)
		}
		t.Logf("Verified actual Shell selection: %s", path)
	}
}
