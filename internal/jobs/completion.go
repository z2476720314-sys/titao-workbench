package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"titao/internal/cfg"
)

// 与队列分开保存，清理队列、开始新批次都不丢失已导出的组合。
type completionFile struct {
	Version int                         `json:"version"`
	Records map[string]completionRecord `json:"records"`
}

type completionRecord struct {
	Files       []completionInput `json:"files"`
	Message     string            `json:"message,omitempty"`
	JudgeNote   string            `json:"judge_note,omitempty"`
	ChosenScale float64           `json:"chosen_scale,omitempty"`
}

func completionRecordOf(t *Task) (completionRecord, error) {
	rec := completionRecord{Message: t.Message, JudgeNote: t.JudgeNote, ChosenScale: t.ChosenScale}
	for _, path := range t.Files {
		file, err := completionInputOf(path)
		if err != nil {
			return completionRecord{}, err
		}
		rec.Files = append(rec.Files, file)
	}
	return rec, nil
}

func (rec completionRecord) valid() bool {
	for _, file := range rec.Files {
		current, err := completionInputOf(file.Path)
		if err != nil || current != file {
			return false
		}
	}
	return len(rec.Files) > 0
}

func (rec completionRecord) resolve(outputRoot string) (completionRecord, bool) {
	paths := make([]string, len(rec.Files))
	for i, file := range rec.Files {
		paths[i] = file.Path
	}
	paths, ok := resolveCompletedFiles(paths, outputRoot)
	if !ok {
		return rec, false
	}
	resolved := rec
	resolved.Files = make([]completionInput, len(rec.Files))
	for i, path := range paths {
		file, err := completionInputOf(path)
		if err != nil || file.Size != rec.Files[i].Size || file.MTime != rec.Files[i].MTime {
			return rec, false
		}
		resolved.Files[i] = file
	}
	return resolved, true
}

// 仅兼容随程序提供的 @ 前缀清理：原目录消失时，核对同级代码目录的整组文件。
func resolveCompletedFiles(files []string, outputRoot string) ([]string, bool) {
	if _, valid := completionOutputs(files); valid {
		return append([]string(nil), files...), true
	}
	if len(files) == 0 || strings.TrimSpace(outputRoot) == "" {
		return files, false
	}
	parent := filepath.Dir(files[0])
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		return files, false
	}
	root, err := normalizedCompletionPath(outputRoot)
	if err != nil {
		return files, false
	}
	oldRoot, err := normalizedCompletionPath(filepath.Dir(parent))
	if err != nil || root != oldRoot {
		return files, false
	}
	name := filepath.Base(parent)
	at := strings.LastIndex(name, "@")
	if at < 0 {
		return files, false
	}
	code := name[at+1:]
	if code == "" || code == "." || code == ".." || strings.ContainsAny(code, `/\`) {
		return files, false
	}
	realRoot, err := filepath.EvalSymlinks(outputRoot)
	if err != nil {
		return files, false
	}
	oldParent, _ := normalizedCompletionPath(parent)
	resolved := make([]string, len(files))
	seen := map[string]bool{}
	for i, file := range files {
		p, err := normalizedCompletionPath(filepath.Dir(file))
		if err != nil || p != oldParent {
			return files, false
		}
		base := filepath.Base(file)
		if seen[base] {
			return files, false
		}
		seen[base] = true
		resolved[i] = filepath.Join(outputRoot, code, base)
		realFile, err := filepath.EvalSymlinks(resolved[i])
		if err != nil {
			return files, false
		}
		rel, err := filepath.Rel(realRoot, realFile)
		if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return files, false
		}
	}
	if _, valid := completionOutputs(resolved); !valid {
		return files, false
	}
	return resolved, true
}

// RestoreQueueTasks 只解析恢复展示的成品路径，不修改队列或成品。
func RestoreQueueTasks(q *Queue, outputRoot string) []*Task {
	if q == nil {
		return nil
	}
	if strings.TrimSpace(q.Options.OutputDir) != "" {
		outputRoot = q.Options.OutputDir
	}
	tasks := q.ToTasks()
	for _, task := range tasks {
		if task.Phase != PhaseDone {
			continue
		}
		if files, valid := resolveCompletedFiles(task.Files, outputRoot); valid {
			task.Files = files
			task.OutputDir = filepath.Dir(files[0])
		}
	}
	return tasks
}

type completionInput struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

func normalizedCompletionPath(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return filepath.Clean(p), nil
}

func completionInputOf(path string) (completionInput, error) {
	p, err := normalizedCompletionPath(path)
	if err != nil {
		return completionInput{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return completionInput{}, err
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return completionInput{}, fmt.Errorf("输入不是非空文件: %s", path)
	}
	return completionInput{Path: p, Size: st.Size(), MTime: st.ModTime().UnixNano()}, nil
}

func effectiveJobOptions(opt Options, def cfg.Config) Options {
	if strings.TrimSpace(opt.OutputDir) == "" {
		opt.OutputDir = def.OutputDir
	}
	if opt.Group == "" {
		opt.Group = def.Group
	}
	if opt.Fit == "" {
		opt.Fit = def.Fit
	}
	if opt.CellSize <= 0 {
		opt.CellSize = def.CellSize
	}
	if opt.JPGQuality <= 0 {
		opt.JPGQuality = def.JPGQuality
	}
	if len(opt.Scales) == 0 {
		opt.Scales = def.Candidates
	}
	if len(opt.Scales) == 0 {
		opt.Scales = cfg.DefaultCandidates()
	}
	if !opt.Auto && len(opt.Scales) > 1 {
		opt.Scales = opt.Scales[:1]
	}
	return opt
}

func effectiveReviewSettings(opt Options, def cfg.Config) (bool, int) {
	reviewOn := opt.Auto && def.AutoTuneOn
	rounds := opt.MaxRounds
	if rounds <= 0 {
		rounds = def.MaxRounds
	}
	if !reviewOn || rounds < 1 {
		rounds = 1
	}
	if rounds > 3 {
		rounds = 3
	}
	return reviewOn, rounds
}

// 只取会影响实际摆放和导出的字段；模型、连接、密钥和流式配置永不入键。
func completionKey(opt Options, def cfg.Config, t *Task) (string, error) {
	pattern, err := completionInputOf(t.Pattern)
	if err != nil {
		return "", err
	}
	template, err := completionInputOf(t.Template)
	if err != nil {
		return "", err
	}
	opt = effectiveJobOptions(opt, def)
	reviewOn, rounds := effectiveReviewSettings(opt, def)
	output, err := normalizedCompletionPath(filepath.Join(opt.OutputDir, t.PatternName))
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Pattern      completionInput
		Template     completionInput
		Output       string
		PatternName  string
		TemplateName string
		Group        string
		Fit          string
		Scales       []float64
		Auto         bool
		ReviewOn     bool
		MaxRounds    int
		TrimAlpha    bool
		NoCrop       bool
		OffsetX      float64
		OffsetY      float64
		CellSize     int
		JPGQuality   int
	}{pattern, template, output, t.PatternName, t.TemplateName, opt.Group, opt.Fit,
		opt.Scales, opt.Auto, reviewOn, rounds, opt.TrimAlpha, opt.NoCrop, opt.OffsetX, opt.OffsetY,
		opt.CellSize, opt.JPGQuality})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func loadCompletions(path string) (*completionFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &completionFile{Version: 1, Records: map[string]completionRecord{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取完成记录: %w", err)
	}
	var f completionFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("完成记录损坏，请保留 %s 并人工处理: %w", path, err)
	}
	if f.Version != 1 || f.Records == nil {
		return nil, fmt.Errorf("完成记录格式不受支持，请人工处理: %s", path)
	}
	for key, rec := range f.Records {
		if len(key) != sha256.Size*2 || len(rec.Files) == 0 {
			return nil, fmt.Errorf("完成记录内容损坏，请人工处理: %s", path)
		}
		if _, err := hex.DecodeString(key); err != nil {
			return nil, fmt.Errorf("完成记录指纹损坏，请人工处理: %s", path)
		}
		for _, p := range rec.Files {
			if strings.TrimSpace(p.Path) == "" || p.Size <= 0 || p.MTime == 0 {
				return nil, fmt.Errorf("完成记录成品路径为空，请人工处理: %s", path)
			}
		}
	}
	return &f, nil
}

func saveCompletions(path string, f *completionFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// 返回最早成品时间，供旧队列迁移核对输入是否在导出后被替换。
func completionOutputs(files []string) (time.Time, bool) {
	if len(files) == 0 {
		return time.Time{}, false
	}
	var earliest time.Time
	for _, path := range files {
		st, err := os.Stat(path)
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			return time.Time{}, false
		}
		if earliest.IsZero() || st.ModTime().Before(earliest) {
			earliest = st.ModTime()
		}
	}
	return earliest, true
}

func (e *Engine) completionPath() string {
	return filepath.Join(filepath.Dir(e.store.Path()), "completed.json")
}

func (f *completionFile) seedQueue(q *Queue, def cfg.Config) bool {
	if q == nil {
		return false
	}
	changed := false
	for _, t := range RestoreQueueTasks(q, effectiveJobOptions(q.Options, def).OutputDir) {
		if t.Phase != PhaseDone {
			continue
		}
		earliest, valid := completionOutputs(t.Files)
		if !valid {
			continue
		}
		key, err := completionKey(q.Options, def, t)
		if err != nil {
			continue
		}
		if t.CompletionKey != "" {
			if key != t.CompletionKey {
				continue
			}
		} else {
			// shortcut: 旧版没有开工指纹，按正常文件时间比较；需内容校验时升级为内容哈希。
			pat, _ := os.Stat(t.Pattern)
			tpl, _ := os.Stat(t.Template)
			if pat == nil || tpl == nil || pat.ModTime().After(earliest) || tpl.ModTime().After(earliest) {
				continue
			}
		}
		if _, exists := f.Records[key]; exists {
			continue
		}
		rec, err := completionRecordOf(t)
		if err != nil {
			continue
		}
		f.Records[key] = rec
		changed = true
	}
	return changed
}

// Plan 先排除有完整成品的历史组合，再按 expandPaths 的图案顺序取数量。
func (e *Engine) Plan(opt Options) ([]*Task, error) {
	if e.store == nil {
		return nil, fmt.Errorf("配置未加载，无法规划")
	}
	if opt.PatternLimit < 0 {
		return nil, fmt.Errorf("本次图案数量不能为负数")
	}
	def := e.store.Get()
	unlimited := opt
	unlimited.PatternLimit = 0
	tasks, err := Plan(unlimited, def)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(effectiveJobOptions(opt, def).OutputDir) == "" {
		return nil, fmt.Errorf("请先选择输出目录")
	}
	e.completionMu.Lock()
	defer e.completionMu.Unlock()
	f, err := e.loadCompletionHistory(def)
	if err != nil {
		return nil, err
	}
	var pending []*Task
	resolvedHistory := false
	selected := map[string]bool{}
	for _, task := range tasks {
		key, err := completionKey(opt, def, task)
		if err != nil {
			return nil, fmt.Errorf("计算完成指纹: %w", err)
		}
		if rec, ok := f.Records[key]; ok {
			if rec.valid() {
				continue
			}
			if resolved, valid := rec.resolve(effectiveJobOptions(opt, def).OutputDir); valid {
				f.Records[key] = resolved
				resolvedHistory = true
				continue
			}
		}
		pattern, _ := normalizedCompletionPath(task.Pattern)
		if !selected[pattern] {
			if opt.PatternLimit > 0 && len(selected) >= opt.PatternLimit {
				continue
			}
			selected[pattern] = true
		}
		task.ID, task.CompletionKey = len(pending)+1, key
		pending = append(pending, task)
	}
	if resolvedHistory {
		if err := saveCompletions(e.completionPath(), f); err != nil {
			return nil, fmt.Errorf("保存成品改名记录失败: %w", err)
		}
	}
	return pending, nil
}

func (e *Engine) recordCompletion(opt Options, def cfg.Config, t *Task) error {
	if t.Phase != PhaseDone {
		return nil
	}
	if _, valid := completionOutputs(t.Files); !valid {
		return nil
	}
	key, err := completionKey(opt, def, t)
	if err != nil {
		return err
	}
	if key != t.CompletionKey {
		return fmt.Errorf("套图期间输入或模板发生变化，未写入完成记录")
	}
	e.completionMu.Lock()
	defer e.completionMu.Unlock()
	f, err := loadCompletions(e.completionPath())
	if err != nil {
		return err
	}
	rec, err := completionRecordOf(t)
	if err != nil {
		return err
	}
	f.Records[key] = rec
	if err := saveCompletions(e.completionPath(), f); err != nil {
		return fmt.Errorf("保存完成记录: %w", err)
	}
	return nil
}

// 调用方持 completionMu，避免预览规划与执行完成时互相覆盖历史。
func (e *Engine) loadCompletionHistory(def cfg.Config) (*completionFile, error) {
	f, err := loadCompletions(e.completionPath())
	if err != nil {
		return nil, err
	}
	q, err := LoadQueue(e.queuePath())
	if err != nil {
		return nil, fmt.Errorf("读取旧队列失败，请保留文件并人工处理: %w", err)
	}
	if f.seedQueue(q, def) {
		if err := saveCompletions(e.completionPath(), f); err != nil {
			return nil, fmt.Errorf("保存历史完成记录失败: %w", err)
		}
	}
	return f, nil
}
