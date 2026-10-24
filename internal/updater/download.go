package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 下载相关上限，防止被超大响应打爆磁盘。
const (
	maxDownloadBytes = 256 << 20 // 256 MiB，足够容纳带 WebView2 的安装包
	downloadTimeout  = 10 * time.Minute
)

// Download 把 url 下载到 dst（自动创建父目录）。
//
// 先写 dst+".part" 再改名，中途失败不会留下一个「看起来完整」的文件。
// 目标已存在且内容非空时直接覆盖（更新场景下旧包没有保留价值）。
func Download(ctx context.Context, url, dst string) error {
	return DownloadWithClient(ctx, &http.Client{Timeout: downloadTimeout}, url, dst)
}

// DownloadWithClient 同 Download，但可注入 HTTP 客户端（测试用）。
func DownloadWithClient(ctx context.Context, client *http.Client, url, dst string) error {
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("创建下载目录: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载更新包: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载更新包失败（HTTP %d）", resp.StatusCode)
	}

	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("创建临时文件: %w", err)
	}

	// 多留 1 字节用于探测超限
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, maxDownloadBytes+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		os.Remove(tmp)
		return fmt.Errorf("写入更新包: %w", copyErr)
	case closeErr != nil:
		os.Remove(tmp)
		return fmt.Errorf("关闭更新包: %w", closeErr)
	case n > maxDownloadBytes:
		os.Remove(tmp)
		return fmt.Errorf("更新包超过 %d MiB 上限", maxDownloadBytes>>20)
	case n == 0:
		os.Remove(tmp)
		return fmt.Errorf("更新包为空（0 字节）")
	}

	// Windows 上 os.Rename 不能覆盖已存在文件，先删旧的
	if _, err := os.Stat(dst); err == nil {
		if err := os.Remove(dst); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("替换旧文件: %w", err)
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("落盘更新包: %w", err)
	}
	return nil
}

// VerifySHA256 校验 path 的 SHA-256 是否等于 want。
//
// want 接受这些写法（GitHub 的 .sha256 资产常见前两种）：
//
//	"<64位hex>"
//	"<64位hex>  titao-workbench.exe"
//	"sha256:<64位hex>"
//
// 大小写不敏感。不匹配返回 error，绝不静默通过——更新包被中间人替换
// 或下载截断，都会在这里暴露。
func VerifySHA256(path, want string) error {
	want = strings.TrimSpace(want)
	if i := strings.IndexByte(want, ':'); i >= 0 && strings.EqualFold(want[:i], "sha256") {
		want = want[i+1:]
	}
	// 取第一个空白分隔的字段
	if f := strings.Fields(want); len(f) > 0 {
		want = f[0]
	}
	want = strings.ToLower(want)
	if len(want) != 64 {
		return fmt.Errorf("校验值格式不对：期望 64 位十六进制，实际 %d 位", len(want))
	}
	if _, err := hex.DecodeString(want); err != nil {
		return fmt.Errorf("校验值不是合法十六进制: %w", err)
	}

	got, err := FileSHA256(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("更新包校验失败：期望 %s，实际 %s", want, got)
	}
	return nil
}

// FileSHA256 计算文件的 SHA-256（小写十六进制）。
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开文件计算校验值: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("读取文件计算校验值: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ApplyReplace 安排「等当前进程退出后，用 newExe 覆盖 exePath 并重新启动」。
//
// 为什么必须绕这一圈：Windows 不允许覆盖正在运行的可执行文件，
// 所以真正的替换只能由一个外部进程在退出之后完成。
// 这里生成一个临时 .bat，用 ping 做延迟（比 timeout 稳，不受重定向输入影响），
// 循环重试直到能写成功，然后启动新版本并自删。
//
// 调用方拿到 error 为 nil 后应尽快退出程序。
func ApplyReplace(exePath, newExe string) error {
	exePath, err := filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("解析当前程序路径: %w", err)
	}
	newExe, err = filepath.Abs(newExe)
	if err != nil {
		return fmt.Errorf("解析更新包路径: %w", err)
	}
	if _, err := os.Stat(newExe); err != nil {
		return fmt.Errorf("更新包不存在: %w", err)
	}

	script := BuildReplaceScript(exePath, newExe)
	batPath := filepath.Join(filepath.Dir(newExe), "titao-update.bat")
	if err := os.WriteFile(batPath, []byte(script), 0o644); err != nil {
		return fmt.Errorf("写入更新脚本: %w", err)
	}

	// 隐藏窗口启动，不抢用户焦点
	cmd := exec.Command("cmd.exe", "/c", "start", "", "/min", batPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动更新脚本: %w", err)
	}
	return nil
}

// BuildReplaceScript 生成延迟替换用的批处理内容。
//
// 单独抽出来是为了能单测：批处理的引号/转义极易写错，
// 一旦写错就是「更新后程序打不开」这种灾难性后果。
func BuildReplaceScript(exePath, newExe string) string {
	exe := exePath
	newFile := newExe
	dir := filepath.Dir(exePath)

	var b strings.Builder
	b.WriteString("@echo off\r\n")
	b.WriteString("setlocal\r\n")
	b.WriteString("chcp 65001 >nul 2>&1\r\n")
	// 等主进程真正退出：最多重试 60 次 × 1 秒
	b.WriteString("set /a tries=0\r\n")
	b.WriteString(":wait\r\n")
	fmt.Fprintf(&b, "copy /y \"%s\" \"%s\" >nul 2>&1\r\n", newFile, exe)
	b.WriteString("if not errorlevel 1 goto replaced\r\n")
	b.WriteString("set /a tries+=1\r\n")
	b.WriteString("if %tries% geq 60 goto failed\r\n")
	b.WriteString("ping -n 2 127.0.0.1 >nul\r\n")
	b.WriteString("goto wait\r\n")
	b.WriteString(":replaced\r\n")
	fmt.Fprintf(&b, "start \"\" /d \"%s\" \"%s\"\r\n", dir, exe)
	fmt.Fprintf(&b, "del /f /q \"%s\" >nul 2>&1\r\n", newFile)
	b.WriteString("goto cleanup\r\n")
	b.WriteString(":failed\r\n")
	// 替换失败不要静默：留下旧版本可用，并提示用户
	fmt.Fprintf(&b, "echo 更新失败：无法覆盖 %s，请手动替换。\r\n", exe)
	b.WriteString("pause\r\n")
	b.WriteString(":cleanup\r\n")
	// 批处理删除自身：需要借一个中转
	b.WriteString("(goto) 2>nul & del \"%~f0\"\r\n")
	return b.String()
}
