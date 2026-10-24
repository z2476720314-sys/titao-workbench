// Package main 提供 Wails 绑定层：把套图引擎的全部能力暴露给前端。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"titao/internal/cfg"
	"titao/internal/fileutil"
	"titao/internal/genimg"
	"titao/internal/genjobs"
	"titao/internal/imgutil"
	"titao/internal/jobs"
	"titao/internal/psx"
	"titao/internal/updater"
)

// App 是绑定到前端的门面。
type App struct {
	ctx   context.Context
	store *cfg.Store
	ps    *psx.Session
	eng   *jobs.Engine
	gen   *genjobs.Engine

	mu       sync.Mutex
	psStatus PSStatus
}

// PSStatus 是 Photoshop 连接状态。
type PSStatus struct {
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Error     string `json:"error"`
	Checked   bool   `json:"checked"`
}

// NewApp 构造应用。
func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	dir := appDir()
	store, err := cfg.NewStore(dir)
	if err != nil {
		runtime.LogErrorf(ctx, "加载配置失败: %v", err)
		store = nil
	}
	a.store = store

	a.ps = psx.Open()
	a.eng = jobs.New(a.ps, store, func(ev jobs.Event) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "titao:event", ev)
		}
	})
	a.gen = genjobs.New(store, func(ev genjobs.Event) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "titao:gen", ev)
		}
	})

	// 启动时后台探测 Photoshop，不阻塞界面
	go a.checkPS()
}

func (a *App) shutdown(ctx context.Context) {
	if a.ps != nil {
		a.ps.Close()
	}
}

// appDir 返回 exe 所在目录；开发模式下退回工作目录。
func appDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	dir := filepath.Dir(exe)
	// wails dev 时 exe 在临时构建目录里，配置应落在项目根
	if strings.Contains(strings.ToLower(dir), "go-build") {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
	}
	return dir
}

// ---------------------------------------------------------------------------
// 环境与配置
// ---------------------------------------------------------------------------

// CheckPhotoshop 探测 Photoshop 是否可用，返回版本号。
func (a *App) CheckPhotoshop() PSStatus {
	a.checkPS()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.psStatus
}

func (a *App) checkPS() {
	st := PSStatus{Checked: true}
	if a.ps == nil {
		st.Error = "Photoshop 会话未初始化"
	} else if ver, err := a.ps.Probe(); err != nil {
		st.Error = err.Error()
	} else {
		st.Available = true
		st.Version = ver
	}
	a.mu.Lock()
	a.psStatus = st
	a.mu.Unlock()
}

// GetConfig 返回当前配置与配置文件路径。
func (a *App) GetConfig() (map[string]any, error) {
	if a.store == nil {
		return nil, fmt.Errorf("配置未加载")
	}
	return map[string]any{
		"config": a.store.Get(),
		"path":   a.store.Path(),
	}, nil
}

// SaveConfig 保存配置（整份覆盖）。
func (a *App) SaveConfig(c cfg.Config) (cfg.Config, error) {
	if a.store == nil {
		return cfg.Config{}, fmt.Errorf("配置未加载")
	}
	return a.store.Update(func(cur *cfg.Config) {
		*cur = c
	})
}

// OpenConfigFile 用系统默认程序打开 config.json。
func (a *App) OpenConfigFile() error {
	if a.store == nil {
		return fmt.Errorf("配置未加载")
	}
	return fileutil.OpenPath(a.store.Path())
}

// ---------------------------------------------------------------------------
// 文件与目录选择
// ---------------------------------------------------------------------------

// PickPatterns 选择图案文件（可多选）。
func (a *App) PickPatterns() ([]string, error) {
	return runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择抠好的图案（可多选）",
		Filters: []runtime.FileFilter{
			{DisplayName: "图片 (*.png;*.jpg;*.jpeg;*.tif;*.tiff;*.webp)", Pattern: "*.png;*.jpg;*.jpeg;*.tif;*.tiff;*.webp"},
		},
	})
}

// PickPatternDir 选择图案目录。
func (a *App) PickPatternDir() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择图案目录"})
}

// PickTemplates 选择模板文件（可多选）。
func (a *App) PickTemplates() ([]string, error) {
	return runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择套图模板（可多选）",
		Filters: []runtime.FileFilter{
			{DisplayName: "TIF 模板 (*.tif;*.tiff)", Pattern: "*.tif;*.tiff"},
		},
	})
}

// PickTemplateDir 选择模板目录。
func (a *App) PickTemplateDir() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择模板目录"})
}

// PickOutputDir 选择输出目录。
func (a *App) PickOutputDir() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: "选择输出目录"})
}

// ListDir 列出目录下的图片文件，供界面预览选择。
func (a *App) ListDir(dir string, kind string) ([]FileEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	allow := map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".tif": true, ".tiff": true, ".webp": true}
	if kind == "template" {
		allow = map[string]bool{".tif": true, ".tiff": true}
	}
	var out []FileEntry
	for _, en := range entries {
		if en.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(en.Name()))
		if !allow[ext] {
			continue
		}
		info, _ := en.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		out = append(out, FileEntry{
			Name: en.Name(),
			Path: filepath.Join(dir, en.Name()),
			Size: size,
		})
	}
	return out, nil
}

// FileEntry 是目录里的一个文件。
type FileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// ProbeTemplate 读取模板结构（尺寸、参考线、图层、可套图的图层组）。
func (a *App) ProbeTemplate(templatePath string) (*psx.TemplateInfo, error) {
	if a.ps == nil {
		return nil, fmt.Errorf("Photoshop 会话未初始化")
	}
	tmp, err := os.MkdirTemp("", "probe_")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	copyPath := filepath.Join(tmp, "tpl.tif")
	if err := fileutil.Copy(templatePath, copyPath); err != nil {
		return nil, err
	}
	return a.ps.ProbeTemplate(copyPath)
}

// ---------------------------------------------------------------------------
// 任务
// ---------------------------------------------------------------------------

// PlanTasks 预览任务展开结果，不执行。
func (a *App) PlanTasks(opt jobs.Options) ([]*jobs.Task, error) {
	if a.store == nil {
		return nil, fmt.Errorf("配置未加载")
	}
	return jobs.Plan(opt, a.store.Get())
}

// StartRun 开始一次套图运行。
func (a *App) StartRun(opt jobs.Options) error {
	if a.eng == nil {
		return fmt.Errorf("引擎未初始化")
	}
	a.mu.Lock()
	st := a.psStatus
	a.mu.Unlock()
	if !st.Available {
		go a.checkPS()
		return fmt.Errorf("Photoshop 未就绪：%s", firstNonEmpty(st.Error, "请先启动 Photoshop 一次"))
	}
	return a.eng.Start(opt)
}

// CancelRun 停止当前运行（旧入口，等价于 StopRun）。
func (a *App) CancelRun() {
	if a.eng != nil {
		a.eng.Cancel()
	}
}

// PauseRun 暂停当前运行：当前这组做完就停在组边界。
func (a *App) PauseRun() error {
	if a.eng == nil {
		return fmt.Errorf("引擎未初始化")
	}
	return a.eng.Pause()
}

// ResumeRun 继续运行。
//
// 两种情形都走这里：暂停后解闸门；程序重启后发现 queue.json 还有没做完的组，
// 则从队列恢复任务列表，只补跑剩下的。
func (a *App) ResumeRun() error {
	if a.eng == nil {
		return fmt.Errorf("引擎未初始化")
	}
	a.mu.Lock()
	st := a.psStatus
	a.mu.Unlock()
	if !st.Available {
		go a.checkPS()
		return fmt.Errorf("Photoshop 未就绪：%s", firstNonEmpty(st.Error, "请先启动 Photoshop 一次"))
	}
	return a.eng.Resume()
}

// StopRun 停止当前运行：当前这组做完就收尾，未执行的组标 canceled。
func (a *App) StopRun() error {
	if a.eng == nil {
		return fmt.Errorf("引擎未初始化")
	}
	a.eng.Stop()
	return nil
}

// PendingQueue 返回上次没跑完的队列（没有则返回 nil）。
func (a *App) PendingQueue() (*jobs.Queue, error) {
	if a.eng == nil {
		return nil, fmt.Errorf("引擎未初始化")
	}
	return a.eng.PendingQueue()
}

// ClearQueue 丢弃上次没跑完的队列。
func (a *App) ClearQueue() error {
	if a.eng == nil {
		return fmt.Errorf("引擎未初始化")
	}
	return a.eng.ClearQueue()
}

// RunStatus 返回运行状态与任务快照。
func (a *App) RunStatus() map[string]any {
	if a.eng == nil {
		return map[string]any{"running": false, "state": "idle", "tasks": []*jobs.Task{}}
	}
	return map[string]any{
		"running": a.eng.IsRunning(),
		"state":   string(a.eng.State()),
		"tasks":   a.eng.Tasks(),
	}
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

// OpenPath 用系统默认程序打开文件或目录。
func (a *App) OpenPath(path string) error {
	return fileutil.OpenPath(path)
}

// RevealInExplorer 在资源管理器里定位文件。
func (a *App) RevealInExplorer(path string) error {
	return fileutil.Reveal(path)
}

// OpenOutputDir 打开输出目录。
func (a *App) OpenOutputDir() error {
	if a.store == nil {
		return fmt.Errorf("配置未加载")
	}
	dir := a.store.Get().OutputDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return fileutil.OpenPath(dir)
}

// NormalizePreview 规范化单个图案并返回尺寸信息，供界面预览。
func (a *App) NormalizePreview(patternPath string, trim bool) (*imgutil.PatternResult, error) {
	tmp, err := os.MkdirTemp("", "prev_")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	res, err := imgutil.Normalize(patternPath, filepath.Join(tmp, "p.png"), trim)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// PreviewPattern 把图案缩成 data URL，供界面缩略图显示。
func (a *App) PreviewPattern(patternPath string, maxSide int) (string, error) {
	if maxSide <= 0 {
		maxSide = 240
	}
	return imgutil.ThumbDataURL(patternPath, maxSide)
}

// PreviewCell 从输出 JPG 生成缩略图 data URL。
func (a *App) PreviewCell(jpgPath string, maxSide int) (string, error) {
	if maxSide <= 0 {
		maxSide = 400
	}
	return imgutil.ThumbDataURL(jpgPath, maxSide)
}

// ListOutputs 列出输出目录下的成品，按图案分组。
func (a *App) ListOutputs() ([]OutputGroup, error) {
	if a.store == nil {
		return nil, fmt.Errorf("配置未加载")
	}
	root := a.store.Get().OutputDir
	dirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []OutputGroup{}, nil
		}
		return nil, err
	}
	var groups []OutputGroup
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		sub := filepath.Join(root, d.Name())
		files, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		var jpgs []string
		for _, f := range files {
			if !f.IsDir() && strings.EqualFold(filepath.Ext(f.Name()), ".jpg") {
				jpgs = append(jpgs, filepath.Join(sub, f.Name()))
			}
		}
		if len(jpgs) == 0 {
			continue
		}
		groups = append(groups, OutputGroup{Name: d.Name(), Dir: sub, Count: len(jpgs), Files: jpgs})
	}
	return groups, nil
}

// OutputGroup 是一组成品。
type OutputGroup struct {
	Name  string   `json:"name"`
	Dir   string   `json:"dir"`
	Count int      `json:"count"`
	Files []string `json:"files"`
}

// ---------------------------------------------------------------------------
// 生图
// ---------------------------------------------------------------------------

// PickGenRefs 选择生图参考图（可多选）。
func (a *App) PickGenRefs() ([]string, error) {
	if a.ctx == nil {
		return nil, fmt.Errorf("界面未就绪")
	}
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择生图参考图（套好的上架图）",
		Filters: []runtime.FileFilter{
			{DisplayName: "图片 (*.jpg;*.jpeg;*.png;*.webp)", Pattern: "*.jpg;*.jpeg;*.png;*.webp"},
		},
	})
	if err != nil {
		return nil, err
	}
	return paths, nil
}

// StartGen 开始一轮生图。
func (a *App) StartGen(opt genjobs.Options) error {
	if a.gen == nil {
		return fmt.Errorf("生图引擎未初始化")
	}
	if a.store == nil {
		return fmt.Errorf("配置未加载")
	}
	return a.gen.Start(opt)
}

// PauseGen 暂停生图（当前 API 调用结束后停住）。
func (a *App) PauseGen() error {
	if a.gen == nil {
		return fmt.Errorf("生图引擎未初始化")
	}
	return a.gen.Pause()
}

// ResumeGen 继续生图。
func (a *App) ResumeGen() error {
	if a.gen == nil {
		return fmt.Errorf("生图引擎未初始化")
	}
	return a.gen.Resume()
}

// StopGen 停止生图。
func (a *App) StopGen() {
	if a.gen != nil {
		a.gen.Stop()
	}
}

// GenStatus 返回生图状态、任务快照与配置就绪情况。
func (a *App) GenStatus() map[string]any {
	if a.gen == nil {
		return map[string]any{
			"state":   string(genjobs.StateIdle),
			"running": false,
			"tasks":   []genjobs.Task{},
			"hasKey":  false,
		}
	}
	done, failed := a.gen.Counters()
	out := map[string]any{
		"state":   string(a.gen.State()),
		"running": a.gen.IsRunning(),
		"tasks":   a.gen.Tasks(),
		"done":    done,
		"failed":  failed,
		"hasKey":  false,
		"count":   2,
	}
	if a.store != nil {
		c := a.store.Get()
		out["hasKey"] = strings.TrimSpace(c.GenAPIKey) != ""
		out["count"] = c.GenCount
		out["autoRun"] = c.GenAutoRun
		out["model"] = c.GenModel
		out["size"] = c.GenSize
	}
	return out
}

// ListGenerated 扫描输出目录里的「生成图」子目录。
func (a *App) ListGenerated() ([]genjobs.GeneratedGroup, error) {
	if a.store == nil {
		return []genjobs.GeneratedGroup{}, nil
	}
	groups, err := genjobs.ListGenerated(a.store.Get().OutputDir)
	if err != nil {
		return nil, err
	}
	if groups == nil {
		groups = []genjobs.GeneratedGroup{}
	}
	return groups, nil
}

// PreviewGenerated 返回生成图的缩略图 data URL。
func (a *App) PreviewGenerated(path string, maxSide int) (string, error) {
	if maxSide <= 0 {
		maxSide = 320
	}
	return imgutil.ThumbDataURL(path, maxSide)
}

// GenModes 返回可用的生图模式，供前端渲染勾选项。
func (a *App) GenModes() []map[string]string {
	return []map[string]string{
		{"id": string(genimg.ModeHandheld), "label": genimg.ModeHandheld.Label()},
		{"id": string(genimg.ModeBackground), "label": genimg.ModeBackground.Label()},
	}
}

// ---------------------------------------------------------------------------
// 自动更新
// ---------------------------------------------------------------------------

// UpdateInfo 是一次更新检查的结果。
type UpdateInfo struct {
	Checked bool   `json:"checked"`
	HasNew  bool   `json:"hasNew"`
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Notes   string `json:"notes"`
	URL     string `json:"url"`   // release 页面
	Asset   string `json:"asset"` // 可执行文件下载地址（可能为空）
	Size    int64  `json:"size"`  // 资产字节数
	Error   string `json:"error"` // 检查失败原因（静默用，不弹窗）
}

// CheckUpdate 查一次 GitHub 最新 release。
//
// 全程静默失败：网络不通、被限流、仓库 404 都只把原因塞进 Error，
// 由前端决定要不要显示。启动时后台调用一次，不阻塞界面。
func (a *App) CheckUpdate() UpdateInfo {
	info := UpdateInfo{Checked: true, Current: Version}
	if a.store != nil && !a.store.Get().UpdateCheck {
		info.Error = "已在设置里关闭自动检查更新"
		return info
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	rel, err := updater.CheckLatest(ctx)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Latest = rel.TagName
	info.Notes = rel.Body
	info.URL = rel.HTMLURL
	info.HasNew = updater.IsNewer(Version, rel.TagName)

	// 优先找 .exe 资产；没有就退化成「只提示，让用户自己下」
	if as := rel.AssetSuffix(".exe"); as != nil {
		info.Asset = as.BrowserDownloadURL
		info.Size = as.Size
	}
	return info
}

// InstallUpdate 下载最新 exe 并安排替换当前程序。
//
// 流程：下载到临时目录 → 校验大小 → 写一个延迟替换脚本 → 提示用户重启。
// exe 运行中不能覆盖自身，所以真正的替换由脚本在进程退出后完成。
func (a *App) InstallUpdate() (string, error) {
	info := a.CheckUpdate()
	if !info.HasNew {
		if info.Error != "" {
			return "", fmt.Errorf("检查更新失败：%s", info.Error)
		}
		return "", fmt.Errorf("已是最新版本 %s", info.Current)
	}
	if info.Asset == "" {
		return "", fmt.Errorf("最新版本 %s 没有提供可下载的 exe，请到 %s 手动下载", info.Latest, info.URL)
	}

	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位当前程序: %w", err)
	}
	tmpDir := filepath.Join(os.TempDir(), "titao-update")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", fmt.Errorf("创建临时目录: %w", err)
	}
	newExe := filepath.Join(tmpDir, "titao-new.exe")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := updater.Download(ctx, info.Asset, newExe); err != nil {
		return "", fmt.Errorf("下载更新: %w", err)
	}
	if st, err := os.Stat(newExe); err != nil {
		return "", fmt.Errorf("下载后找不到文件: %w", err)
	} else if info.Size > 0 && st.Size() != info.Size {
		return "", fmt.Errorf("下载不完整：期望 %d 字节，实际 %d 字节", info.Size, st.Size())
	}
	if err := updater.ApplyReplace(exe, newExe); err != nil {
		return "", fmt.Errorf("安排替换: %w", err)
	}
	return fmt.Sprintf("已下载 %s，替换脚本已就绪。关闭程序后会自动替换，重新打开即为新版本。", info.Latest), nil
}

// AppVersion 返回当前程序版本号。
func (a *App) AppVersion() string { return Version }

// PSCaps 返回当前 Photoshop 的能力诊断（版本自适应结果）。
func (a *App) PSCaps() map[string]any {
	a.mu.Lock()
	ver := a.psStatus.Version
	a.mu.Unlock()
	c := psx.For(ver)
	return map[string]any{
		"version":         c.Version,
		"major":           c.Major,
		"verified":        c.Verified,
		"doJavaScript":    c.DoJavaScript,
		"noDialogs":       c.NoDialogs,
		"editSmartObject": c.EditSmartObject,
		"replaceContents": c.ReplaceContents,
		"summary":         c.Summary(),
		"notes":           c.Notes,
	}
}

// ---------------------------------------------------------------------------

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
