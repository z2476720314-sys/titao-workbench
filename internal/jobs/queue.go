// 队列持久化：把「图案 × 模板」组合的进度落到 config.json 同目录的 queue.json。
//
// 目的只有一个：程序被关掉（或被强杀）后重开，还能知道上次跑到哪、哪些组
// 已经出图了，从而只补跑没做完的部分。所以这份文件是**运行状态的镜像**，
// 不是配置：删掉它只丢恢复能力，不影响任何参数。
//
// 写盘时机刻意克制：只在每组开始前、结束后各写一次。渲染一组的耗时在秒级，
// 每帧写盘只会白占 IO。
package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// QueueVersion 是 queue.json 的格式版本。读到不认识的版本按「损坏」处理，
// 宁可丢掉恢复能力，也不要拿旧结构去驱动新引擎。
const QueueVersion = 1

// QueueTask 是队列文件里的一行任务。
//
// 字段名用下划线风格，方便用户直接打开 queue.json 排查；
// 与内存里的 Task（驼峰）刻意不同：前者是落盘格式，后者是 Wails 传输格式，
// 两者的演进节奏不该互相绑架。
type QueueTask struct {
	ID           int      `json:"id"`
	Pattern      string   `json:"pattern"`
	Template     string   `json:"template"`
	PatternName  string   `json:"pattern_name"`
	TemplateName string   `json:"template_name"`
	Phase        Phase    `json:"phase"`
	Files        []string `json:"files,omitempty"`
	Error        string   `json:"error,omitempty"`
	ChosenScale  float64  `json:"chosen_scale,omitempty"`
	ElapsedMS    int64    `json:"elapsed_ms,omitempty"`
}

// Queue 是一次运行的完整落盘镜像。
type Queue struct {
	Version   int         `json:"version"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at,omitempty"`
	Finished  bool        `json:"finished"`
	Pending   int         `json:"pending"` // 派生字段：还没结论的组数
	Options   Options     `json:"options"`
	Tasks     []QueueTask `json:"tasks"`
}

// settledPhase 报告某个阶段是否已有最终结论（不需要再跑）。
//
// done / skipped / failed 都算「有结论」：
//   - done、skipped 是任务书明确要求继续时跳过的；
//   - failed 是一次运行给出的失败裁决，继续时不该无声重跑（那会白烧一遍
//     Photoshop 和模型额度），要重跑请点「开始」。
//
// canceled 与 pending、running、以及各种中间阶段一样算「没结论」：
// 强杀时留下的 running 组必须重跑，用户点「停止」后改主意点「继续」也应当
// 把没执行的组补上。
func settledPhase(p Phase) bool {
	switch p {
	case PhaseDone, PhaseSkipped, PhaseFailed:
		return true
	}
	return false
}

// CountPending 数出还没结论的组数。
func (q *Queue) CountPending() int {
	if q == nil {
		return 0
	}
	n := 0
	for _, t := range q.Tasks {
		if !settledPhase(t.Phase) {
			n++
		}
	}
	return n
}

// HasPending 报告队列里是否还有没做完的组。
func (q *Queue) HasPending() bool { return q.CountPending() > 0 }

// NewQueue 用一次运行的参数与任务快照构造落盘镜像。
func NewQueue(opt Options, tasks []*Task) *Queue {
	now := time.Now().Format(time.RFC3339)
	q := &Queue{
		Version:   QueueVersion,
		CreatedAt: now,
		Options:   opt,
		Tasks:     make([]QueueTask, len(tasks)),
	}
	for i, t := range tasks {
		q.Tasks[i] = QueueTask{
			ID:           t.ID,
			Pattern:      t.Pattern,
			Template:     t.Template,
			PatternName:  t.PatternName,
			TemplateName: t.TemplateName,
			Phase:        t.Phase,
			Files:        append([]string(nil), t.Files...),
			Error:        t.Error,
			ChosenScale:  t.ChosenScale,
			ElapsedMS:    t.ElapsedMS,
		}
	}
	q.Pending = q.CountPending()
	return q
}

// ToTasks 把落盘行还原成内存任务，供续跑使用。
func (q *Queue) ToTasks() []*Task {
	if q == nil {
		return nil
	}
	out := make([]*Task, len(q.Tasks))
	for i, qt := range q.Tasks {
		out[i] = &Task{
			ID:           qt.ID,
			Pattern:      qt.Pattern,
			PatternName:  qt.PatternName,
			Template:     qt.Template,
			TemplateName: qt.TemplateName,
			Phase:        qt.Phase,
			Files:        append([]string(nil), qt.Files...),
			Error:        qt.Error,
			ChosenScale:  qt.ChosenScale,
			ElapsedMS:    qt.ElapsedMS,
		}
		// 已完成的组进度条要显示满格，否则界面看着像卡在 0%
		if qt.Phase == PhaseDone || qt.Phase == PhaseSkipped {
			out[i].Progress = 100
		}
	}
	return out
}

// LoadQueue 读 queue.json。
//
// 三种情况分开表达，调用方好决策：
//   - 文件不存在 → (nil, nil)：从没跑过，不算错误；
//   - 内容损坏 / 版本不认识 → (nil, err)：错误照常返回，由上层决定忽略；
//   - 正常 → (*Queue, nil)。
//
// 无论哪种都不会 panic：损坏的文件只会被当成「没有队列」。
func LoadQueue(path string) (*Queue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var q Queue
	if err := json.Unmarshal(data, &q); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	if q.Version <= 0 || q.Version > QueueVersion {
		return nil, fmt.Errorf("%s 的版本 %d 不受支持", path, q.Version)
	}
	q.Pending = q.CountPending()
	return &q, nil
}

// SaveQueue 原子写 queue.json（先写 .tmp 再 rename），
// 避免写到一半断电留下半个 JSON 让下次启动读到损坏文件。
func SaveQueue(path string, q *Queue) error {
	if path == "" {
		return fmt.Errorf("队列路径为空")
	}
	if q == nil {
		return fmt.Errorf("队列为空")
	}
	q.UpdatedAt = time.Now().Format(time.RFC3339)
	q.Pending = q.CountPending()
	q.Finished = !q.HasPending()

	data, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RemoveQueue 删除 queue.json（用户的「清理」按钮）。
func RemoveQueue(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	// 顺手清掉上次异常退出可能残留的临时文件
	_ = os.Remove(path + ".tmp")
	return nil
}
