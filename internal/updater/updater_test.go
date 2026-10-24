package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompare 守住语义版本比较的全部分支。
//
// 这里每条都是真实会遇到的形态：GitHub tag 带 v 前缀、预发布版、
// 用户手改 config 写进脏字符串。
func TestCompare(t *testing.T) {
	cases := []struct {
		current, latest string
		want            int
		why             string
	}{
		// 常规
		{"v0.2.0", "0.1.0", 1, "本地比远端新"},
		{"0.1.0", "v0.2.0", -1, "远端比本地新（有更新）"},
		{"v0.2.0", "v0.2.0", 0, "同版本"},
		{"0.2", "0.2.0", 0, "段数不足按 0 补齐"},
		{"0.2.0", "0.2.1", -1, "补丁号更大"},
		{"1.10.0", "1.9.0", 1, "数值比较而非字典序"},
		// 预发布
		{"1.0.0-beta", "1.0.0", -1, "预发布小于正式版"},
		{"1.0.0", "1.0.0-beta", 1, "正式版大于预发布"},
		{"1.0.0-alpha", "1.0.0-beta", -1, "预发布标识按字典序"},
		{"1.0.0-1", "1.0.0-alpha", -1, "数字标识小于字母标识"},
		{"1.0.0-alpha.1", "1.0.0-alpha", 1, "标识更多者更大"},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1, "标识内数字按数值比较"},
		// 脏数据
		{"abc", "0.0.0", 0, "非数字段按 0"},
		{"abc", "1.0.0", -1, "脏数据小于任何正常版本"},
		{"", "0.0.0", 0, "空串按 0.0.0"},
		{"v", "0.0.0", 0, "只有 v 前缀"},
		{"1.0.0+build.5", "1.0.0", 0, "构建元数据不参与比较"},
		{"  0.2.0  ", "v0.2.0", 0, "忽略前后空白"},
	}
	for _, c := range cases {
		if got := Compare(c.current, c.latest); got != c.want {
			t.Errorf("Compare(%q, %q) = %d，期望 %d（%s）", c.current, c.latest, got, c.want, c.why)
		}
	}
}

// TestIsNewer 守住调用侧最常用的判断。
func TestIsNewer(t *testing.T) {
	if !IsNewer("v0.1.0", "v0.2.0") {
		t.Error("0.1.0 → 0.2.0 应判定有更新")
	}
	if IsNewer("v0.2.0", "v0.2.0") {
		t.Error("同版本不应判定有更新")
	}
	if IsNewer("v0.3.0", "v0.2.0") {
		t.Error("本地更新时不应判定有更新")
	}
}

// TestNormalizeTag 守住显示用的去 v 前缀。
func TestNormalizeTag(t *testing.T) {
	for in, want := range map[string]string{
		"v0.2.0": "0.2.0",
		"V0.2.0": "0.2.0",
		"0.2.0":  "0.2.0",
		"  v1.0 ": "1.0",
	} {
		if got := NormalizeTag(in); got != want {
			t.Errorf("NormalizeTag(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// fakeAPI 起一个假的 GitHub Releases 服务，返回指定状态码与响应体。
func fakeAPI(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GitHub 无 User-Agent 会 403，这里必须验证客户端确实带了
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("请求缺少 User-Agent: %q", r.Header.Get("User-Agent"))
		}
		if r.URL.Path != "/repos/z2476720314-sys/titao-workbench/releases/latest" {
			t.Errorf("请求路径不符: %s", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{APIBase: srv.URL, Repo: DefaultRepo, HTTP: srv.Client()}
}

// TestCheckLatestOK 守住正常发布信息的解析，含资产列表。
func TestCheckLatestOK(t *testing.T) {
	body, _ := json.Marshal(Release{
		TagName: "v0.2.0",
		Name:    "套图工作台 v0.2.0",
		Body:    "## v0.2.0\n- 新功能",
		HTMLURL: "https://github.com/z2476720314-sys/titao-workbench/releases/tag/v0.2.0",
		Assets: []Asset{
			{Name: "titao-workbench.exe", BrowserDownloadURL: "https://example.test/a.exe", Size: 123},
			{Name: "titao-workbench.exe.sha256", BrowserDownloadURL: "https://example.test/a.sha256", Size: 64},
		},
	})
	c := fakeAPI(t, http.StatusOK, string(body))

	rel, err := c.CheckLatest(context.Background())
	if err != nil {
		t.Fatalf("CheckLatest 报错: %v", err)
	}
	if rel.TagName != "v0.2.0" {
		t.Errorf("TagName = %q", rel.TagName)
	}
	if len(rel.Assets) != 2 {
		t.Fatalf("Assets 数量 = %d，期望 2", len(rel.Assets))
	}
	if a := rel.AssetByName("titao-workbench.exe"); a == nil || a.Size != 123 {
		t.Errorf("AssetByName 未找到 exe: %+v", a)
	}
	if a := rel.AssetSuffix(".sha256"); a == nil || a.Name != "titao-workbench.exe.sha256" {
		t.Errorf("AssetSuffix 未找到 sha256: %+v", a)
	}
	if a := rel.AssetByName("不存在"); a != nil {
		t.Errorf("不存在的资产应返回 nil，实际 %+v", a)
	}
	if a := rel.AssetSuffix(".nope"); a != nil {
		t.Errorf("不匹配的后缀应返回 nil，实际 %+v", a)
	}
	if !IsNewer("v0.1.0", rel.TagName) {
		t.Error("v0.1.0 相对 v0.2.0 应有更新")
	}
}

// TestCheckLatestErrors 守住各种失败都返回 error 且不 panic。
//
// 这些分支在生产里就是「静默跳过更新检查」，绝不能崩。
func TestCheckLatestErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"404 无发布", http.StatusNotFound, `{}`, "尚无发布"},
		{"403 限流", http.StatusForbidden, `{}`, "限流"},
		{"429 限流", http.StatusTooManyRequests, `{}`, "限流"},
		{"500 服务端错误", http.StatusInternalServerError, `{}`, "HTTP 500"},
		{"非法 JSON", http.StatusOK, `not json`, "解析发布信息"},
		{"缺 tag_name", http.StatusOK, `{"name":"x"}`, "tag_name"},
		{"空 tag_name", http.StatusOK, `{"tag_name":"   "}`, "tag_name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			client := fakeAPI(t, c.status, c.body)
			rel, err := client.CheckLatest(context.Background())
			if err == nil {
				t.Fatalf("期望报错，实际返回 %+v", rel)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误信息应含 %q，实际: %v", c.want, err)
			}
		})
	}
}

// TestCheckLatestSkipsPrerelease 守住预发布/草稿不当作更新目标。
func TestCheckLatestSkipsPrerelease(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v0.3.0","prerelease":true}`,
		`{"tag_name":"v0.3.0","draft":true}`,
	} {
		c := fakeAPI(t, http.StatusOK, body)
		if _, err := c.CheckLatest(context.Background()); err == nil {
			t.Errorf("预发布/草稿应被拒绝: %s", body)
		}
	}
}

// TestCheckLatestNetworkError 守住连不上时返回 error 而不是挂住。
func TestCheckLatestNetworkError(t *testing.T) {
	c := &Client{APIBase: "http://127.0.0.1:1", Repo: DefaultRepo, HTTP: &http.Client{}}
	if _, err := c.CheckLatest(context.Background()); err == nil {
		t.Error("连接失败应返回 error")
	}
}

// TestVerifySHA256Match 守住正常校验通过。
func TestVerifySHA256Match(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	content := []byte("titao update payload")
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	hexSum := hex.EncodeToString(sum[:])

	// 三种常见写法都要认
	for _, want := range []string{
		hexSum,
		strings.ToUpper(hexSum),
		hexSum + "  titao-workbench.exe",
		"sha256:" + hexSum,
	} {
		if err := VerifySHA256(p, want); err != nil {
			t.Errorf("VerifySHA256(%q) 应通过，实际: %v", want, err)
		}
	}
}

// TestVerifySHA256Mismatch 守住不匹配必须报错，绝不静默通过。
func TestVerifySHA256Mismatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(p, []byte("real content"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrong := strings.Repeat("0", 64)
	err := VerifySHA256(p, wrong)
	if err == nil {
		t.Fatal("校验值不匹配时应报错")
	}
	if !strings.Contains(err.Error(), "校验失败") {
		t.Errorf("错误信息应点明校验失败，实际: %v", err)
	}
}

// TestVerifySHA256BadInput 守住格式错误的校验值被拒绝。
func TestVerifySHA256BadInput(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"", "  ", "abc", strings.Repeat("z", 64), strings.Repeat("0", 63)} {
		if err := VerifySHA256(p, want); err == nil {
			t.Errorf("VerifySHA256(%q) 应报错", want)
		}
	}
}

// TestVerifySHA256MissingFile 守住文件不存在时的错误。
func TestVerifySHA256MissingFile(t *testing.T) {
	if err := VerifySHA256(filepath.Join(t.TempDir(), "nope"), strings.Repeat("0", 64)); err == nil {
		t.Error("文件不存在时应报错")
	}
}

// TestDownload 守住下载落盘：内容正确、无 .part 残留。
func TestDownload(t *testing.T) {
	payload := []byte("binary payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("下载请求缺少 User-Agent")
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "sub", "update.exe")
	if err := Download(context.Background(), srv.URL, dst); err != nil {
		t.Fatalf("Download 报错: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("下载内容不符: %q", got)
	}
	if _, err := os.Stat(dst + ".part"); err == nil {
		t.Error("不应残留 .part 临时文件")
	}
}

// TestDownloadErrors 守住下载失败分支。
func TestDownloadErrors(t *testing.T) {
	// 非 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "a.exe")
	if err := Download(context.Background(), srv.URL, dst); err == nil {
		t.Error("HTTP 404 应报错")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("失败时不应留下目标文件")
	}

	// 空响应体
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer empty.Close()
	if err := Download(context.Background(), empty.URL, dst); err == nil {
		t.Error("空响应体应报错")
	}
}

// TestDownloadOverwrites 守住重复下载能覆盖旧文件（Windows 上 Rename 不覆盖）。
func TestDownloadOverwrites(t *testing.T) {
	body := "v1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dst := filepath.Join(t.TempDir(), "a.exe")
	if err := os.WriteFile(dst, []byte("old longer content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Download(context.Background(), srv.URL, dst); err != nil {
		t.Fatalf("覆盖下载报错: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != body {
		t.Errorf("内容未更新: %q", got)
	}
}

// TestBuildReplaceScript 守住延迟替换脚本的关键要素。
//
// 批处理一旦写错就是「更新后程序起不来」，所以逐项断言：
// 必须有重试循环、必须启动新版本、必须自删、必须处理失败提示。
func TestBuildReplaceScript(t *testing.T) {
	s := BuildReplaceScript(`C:\apps\titao\titao-workbench.exe`, `C:\apps\titao\update.exe`)

	for _, want := range []string{
		"@echo off",
		`copy /y "C:\apps\titao\update.exe" "C:\apps\titao\titao-workbench.exe"`,
		":wait",
		":replaced",
		`:failed`,
		`start "" /d "C:\apps\titao" "C:\apps\titao\titao-workbench.exe"`,
		"更新失败",
		"del \"%~f0\"",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("替换脚本缺少关键片段 %q\n实际内容:\n%s", want, s)
		}
	}
	// 换行必须是 CRLF，cmd.exe 对 LF 的批处理容错差
	if !strings.Contains(s, "\r\n") {
		t.Error("批处理应使用 CRLF 换行")
	}
	// 不能出现自动强制结束进程的危险命令
	for _, bad := range []string{"taskkill", "format ", "del /s", "rmdir /s"} {
		if strings.Contains(strings.ToLower(s), bad) {
			t.Errorf("替换脚本不应包含危险命令 %q", bad)
		}
	}
}

// TestApplyReplaceMissingNewExe 守住更新包不存在时提前报错，不生成脚本。
func TestApplyReplaceMissingNewExe(t *testing.T) {
	dir := t.TempDir()
	err := ApplyReplace(filepath.Join(dir, "app.exe"), filepath.Join(dir, "nope.exe"))
	if err == nil {
		t.Fatal("更新包不存在时应报错")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "titao-update.bat")); statErr == nil {
		t.Error("失败时不应留下更新脚本")
	}
}
