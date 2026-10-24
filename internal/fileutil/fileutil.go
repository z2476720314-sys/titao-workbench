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
func Reveal(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("路径不存在: %s", path)
	}
	// explorer 的 /select, 语法要求逗号后不留空格，且整串作为一个参数传入
	return exec.Command("explorer", "/select,"+path).Start()
}
