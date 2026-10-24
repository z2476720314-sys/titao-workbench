// Package updater 提供「检查 GitHub Releases → 下载 → 校验 → 替换」的最小闭环。
//
// 设计原则：全程静默失败。自动更新是锦上添花，网络不通、被限流、仓库 404
// 都不该打断用户正在做的事，所以这里的函数只返回 error，不打印、不弹窗，
// 由上层决定要不要提示。
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultAPIBase 是 GitHub REST API 的根地址。
	DefaultAPIBase = "https://api.github.com"
	// DefaultRepo 是本项目的 GitHub 仓库（owner/name）。
	DefaultRepo = "z2476720314-sys/titao-workbench"
	// userAgent 是 GitHub API 强制要求的 User-Agent；缺失会直接 403。
	userAgent = "titao-workbench"
)

// Release 是 GitHub Releases API 返回的发布信息（只保留本项目用到的字段）。
type Release struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	Body        string  `json:"body"`
	Prerelease  bool    `json:"prerelease"`
	Draft       bool    `json:"draft"`
	PublishedAt string  `json:"published_at"`
	HTMLURL     string  `json:"html_url"`
	Assets      []Asset `json:"assets"`
}

// Asset 是一个发布附件。
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// Client 是 GitHub Releases 的只读客户端。
//
// APIBase / Repo 可改，测试用 httptest 指向本地假服务。
type Client struct {
	APIBase string
	Repo    string
	HTTP    *http.Client
}

// NewClient 返回指向官方仓库的默认客户端。
func NewClient() *Client {
	return &Client{
		APIBase: DefaultAPIBase,
		Repo:    DefaultRepo,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
	}
}

// CheckLatest 用默认客户端查询最新发布。
func CheckLatest(ctx context.Context) (*Release, error) {
	return NewClient().CheckLatest(ctx)
}

// CheckLatest 查询仓库的最新 release。
//
// 404（仓库或 release 不存在）、403（限流）、网络错误都原样返回 error，
// 由上层静默忽略。draft 与 prerelease 视为「没有可用更新」直接报错，
// 避免把测试版推给普通用户。
func (c *Client) CheckLatest(ctx context.Context) (*Release, error) {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	base := strings.TrimRight(c.APIBase, "/")
	if base == "" {
		base = DefaultAPIBase
	}
	repo := c.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	url := base + "/repos/" + repo + "/releases/latest"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造更新检查请求: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("查询最新版本: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("仓库 %s 尚无发布", repo)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub API 限流或拒绝访问（HTTP %d）", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("查询最新版本失败（HTTP %d）", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析发布信息: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("发布信息缺少 tag_name")
	}
	if rel.Draft || rel.Prerelease {
		return nil, fmt.Errorf("最新发布 %s 是预发布/草稿，不作为更新目标", rel.TagName)
	}
	return &rel, nil
}

// AssetByName 在发布附件里按文件名精确查找，找不到返回 nil。
func (r *Release) AssetByName(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// AssetSuffix 返回第一个以 suffix 结尾的附件，找不到返回 nil。
//
// 用于找 `<exe>.sha256` 这类带可变前缀的校验文件。
func (r *Release) AssetSuffix(suffix string) *Asset {
	for i := range r.Assets {
		if strings.HasSuffix(r.Assets[i].Name, suffix) {
			return &r.Assets[i]
		}
	}
	return nil
}
