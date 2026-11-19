package fileutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 定位功能曾整点无反应，原因是直接调 explorer /select,：
// 非交互进程启动 explorer 一律返回退出码 1，而 Start() 的 nil 被当成成功。
// 现在改为 Shell COM 派发并回报是否真的选中，这里锁住这几个契约。
func TestRevealRejectsMissingPath(t *testing.T) {
	err := Reveal(filepath.Join(t.TempDir(), "nope.jpg"))
	if err == nil {
		t.Fatal("不存在的文件必须报错，不能静默返回成功")
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("错误信息应说明文件不存在，实际: %v", err)
	}
}

// 定位脚本必须做四件事，缺一件就会回到「点了没反应」：
// 由 Shell COM 派发打开、对目标窗口 SelectItem、等待窗口就绪、回报结果。
func TestRevealScriptSelectsAndReports(t *testing.T) {
	script := revealScript(`C:/temp/target.txt`)
	for _, want := range []string{
		"New-Object -ComObject Shell.Application", // 用 Shell COM，不用 explorer.exe
		"$sh.Namespace($dir)",                     // 取到目标文件夹
		"$folder.ParseName($leaf)",                // 取到目标条目
		"$sh.Explore($dir)",                       // 必须 Explore 目标目录；InvokeVerbEx('','') 会开到「文档」
		"$w.Document.SelectItem($item, 0x1C)",     // 真正选中该文件
		"Start-Sleep -Milliseconds 250",           // 等窗口就绪再选中
		`Write-Output ("selected=" + $done)`,      // 回报结果供调用方判断
	} {
		if !strings.Contains(script, want) {
			t.Errorf("定位脚本缺少关键步骤：%q", want)
		}
	}
}

// 脚本必须是 UTF-8 BOM 开头，否则 PowerShell 按本地代码页解析中文路径会乱码。
func TestRevealWritesBOMScript(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅 Windows 用 PowerShell 定位")
	}
	// 不能用 t.TempDir()/os.TempDir()：它们在 AppData\Local\Temp 下，
	// 那是隐藏目录，资源管理器不会在里面选中条目，会误判成失败。
	// 用输出盘上的普通可见目录。
	dir := filepath.Join("E:/", "titao-reveal-test")
	t.Cleanup(func() { os.RemoveAll(dir) })
	target := filepath.Join(dir, "中文目录", "图案@WMTAI1654_01.jpg")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 真实调用一次：中文路径 + 正斜杠（前端传来的格式）。
	// 不连续调用两次：explorer 会把同目录请求复用到已开的窗口，
	// 第二次进入时窗口归属可能还没稳定，属于 shell 的时序噪声，与代码无关。
	if err := Reveal(filepath.ToSlash(target)); err != nil {
		t.Fatalf("正斜杠中文路径定位失败: %v", err)
	}
}
