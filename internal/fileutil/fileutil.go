// Package fileutil 收拢文件与 shell 相关的小工具。
//
// 之前 app.go 与 jobs/engine.go 各有一份完全相同的 copyFile，改一处漏一处，
// 统一收到这里。
package fileutil

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Copy 把 src 复制到 dst（覆盖已存在的 dst）。
//
// 用 io.Copy 而不是 ReadFrom：前者走 32KB 缓冲，不会像 sendfile 那样在
// 网络盘上退化成整文件读入内存。
func Copy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开源文件 %s: %w", filepath.Base(src), err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("创建目标文件 %s: %w", filepath.Base(dst), err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("复制 %s: %w", filepath.Base(src), err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// OpenPath 用系统默认程序打开文件或目录。
//
// 不走 `cmd /c start`：cmd 会把 & ^ % 等字符当元字符解析，路径里带这些字符
// 时会被截断或注入。explorer 直接接收参数，不经过 shell。
func OpenPath(path string) error {
	if path == "" {
		return fmt.Errorf("路径为空")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("路径不存在: %s", path)
	}
	return exec.Command("explorer", path).Start()
}

// Reveal 在资源管理器里选中并定位到 path。
//
// 为什么不用 explorer /select,：
// 实测这台机器上，从非交互进程里直接启动 explorer.exe 一律返回退出码 1
// （连 `explorer <目录>` 都失败），而 /select, 还额外要求原生反斜杠路径、
// 逗号后不能有空格。调用方看到 Start() 返回 nil 以为成功，实际什么都没选中,
// 这就是「定位没反应」的成因。
//
// 改用 Shell.Application COM：Namespace 拿到文件夹、ParseName 拿到条目，
// 再用 InvokeVerbEx 让 shell 自己派发打开动作，最后对匹配到该目录的窗口
// 调 SelectItem 选中。shellexec 由 shell 派发，窗口归属正确，实测可选中。
func Reveal(path string) error {
	clean := filepath.Clean(path)
	abs, err := filepath.Abs(clean)
	if err != nil {
		abs = clean
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("文件不存在: %s", abs)
	}
	// PowerShell 从命令行接收中文路径会乱码，改为写文件再由脚本读。
	dir, err := os.MkdirTemp("", "titao-reveal")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	pathFile := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(pathFile, []byte(abs), 0600); err != nil {
		return err
	}
	script := "\uFEFF" + revealScript(filepath.ToSlash(pathFile))
	scriptFile := filepath.Join(dir, "reveal.ps1")
	if err := os.WriteFile(scriptFile, []byte(script), 0600); err != nil {
		return err
	}
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-File", scriptFile).CombinedOutput()
	if err != nil {
		return fmt.Errorf("在资源管理器定位失败: %v %s", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "selected=True") {
		return fmt.Errorf("资源管理器未选中该文件: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// revealScript 生成定位脚本。0x1C = SELECT|DESELECTOTHERS|ENSUREVISIBLE|FOCUS。
func revealScript(pathFile string) string {
	return `$p = [System.IO.File]::ReadAllText('` + pathFile + `', [System.Text.Encoding]::UTF8).Trim()
$dir = [System.IO.Path]::GetDirectoryName($p)
$leaf = [System.IO.Path]::GetFileName($p)
$sh = New-Object -ComObject Shell.Application
$folder = $sh.Namespace($dir)
$item = $folder.ParseName($leaf)
if ($null -eq $item) { Write-Output "selected=False"; exit 0 }
# 必须用 Explore 打开目标文件夹。InvokeVerbEx('', '') 会落到默认动作，
# 实测打开的是「文档」目录，目标窗口根本不出现，选中自然一直失败。
$sh.Explore($dir)
$done = $false
for ($i = 0; $i -lt 24 -and -not $done; $i++) {
  Start-Sleep -Milliseconds 250
  foreach ($w in $sh.Windows()) {
    try {
      $wp = $w.Document.Folder.Self.Path
      if ($wp -and ($wp.TrimEnd('\') -ieq $dir.TrimEnd('\'))) {
        $w.Document.SelectItem($item, 0x1C)
        $done = $true
        break
      }
    } catch {}
  }
}
Write-Output ("selected=" + $done)
`
}
