// Package cfg 管理程序配置。配置写在程序目录的 config.json 里，
// 不依赖环境变量（用户明确要求）。
package cfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DefaultCandidates 是视觉模型挑选的候选填充倍数。
//
// 区间必须覆盖「明显偏小 → 明显超出」，模型才能做绝对判断；
// 区间太窄它只能相对比较，选出的倍数会随区间漂移（已实测）。
func DefaultCandidates() []float64 {
	return []float64{0.85, 0.95, 1.05, 1.15, 1.3}
}

// Config 是完整配置。
type Config struct {
	// 视觉模型
	BaseURL      string    `json:"base_url"`
	APIKey       string    `json:"api_key"`
	Model        string    `json:"model"`
	MaxTokens    int       `json:"max_tokens"`
	TimeoutSec   int       `json:"timeout_seconds"`
	Temperature  float64   `json:"temperature"`
	ImageMaxSide int       `json:"image_max_side"`
	Thinking     bool      `json:"thinking"`
	Candidates   []float64 `json:"candidates"`

	// 路径默认值
	TemplateDir string `json:"template_dir"`
	PatternDir  string `json:"pattern_dir"`
	OutputDir   string `json:"output_dir"`

	// 套图默认参数
	Group      string `json:"group"`
	Fit        string `json:"fit"`
	CellSize   int    `json:"cell_size"`
	JPGQuality int    `json:"jpg_quality"`
	TrimAlpha  bool   `json:"trim_alpha"`
	// NoCrop 禁止裁切：图案整体必须完整可见。
	// 用指针是为了区分「旧配置里根本没这个键」（nil → 取默认 true）
	// 和「用户明确关掉」（&false）。若用裸 bool，老配置升级后会被零值
	// 静默改成关闭，用户看不到任何提示就丢了这层保护。
	NoCrop  *bool   `json:"no_crop"` // 禁止裁切：图案整体必须完整可见（默认开）
	OffsetX float64 `json:"offset_x"`
	OffsetY float64 `json:"offset_y"`

	// 参数自动微调：多轮迭代收敛（模型自行决定候选区间与微调量）
	MaxRounds  int  `json:"max_rounds"`   // 最大迭代轮数，1~3，默认 2
	AutoTuneOn bool `json:"auto_tune_on"` // 是否启用多轮自动微调

	// 套图前预检：识别水印 / 镜头框 / 手机壳实拍图并跳过
	PrecheckOn   bool `json:"precheck_on"`   // 是否启用预检，默认开
	PrecheckSkip bool `json:"precheck_skip"` // 判定不合格时是否真的跳过（关=只记录不拦）

	// 生图模型（Junli Studio 等 OpenAI 兼容服务）
	GenBaseURL  string `json:"gen_base_url"`
	GenAPIKey   string `json:"gen_api_key"`
	GenModel    string `json:"gen_model"`
	GenSize     string `json:"gen_size"`     // 如 1024x1024（1:1，1K）
	GenThinking bool   `json:"gen_thinking"` // 是否开启生图思考模式（计费更高）
	GenTimeout  int    `json:"gen_timeout"`  // 单张生图超时秒数
	GenCount    int    `json:"gen_count"`    // 用户手动设定的生成张数，1~10
	GenAutoRun  bool   `json:"gen_auto_run"` // 套图完成后自动生图，默认关

	// Photoshop 版本自适应
	PSVersion string `json:"ps_version"` // 首次运行探测到的 PS 版本，如 "27.7.0"

	// 自动更新
	UpdateCheck bool `json:"update_check"` // 启动时静默检查新版本，默认开
}

// Default 返回一份可用的默认配置。
func Default() Config {
	return Config{
		BaseURL:      "https://www.acctoken.com/v1",
		Model:        "deepseek-v4.1-flash",
		MaxTokens:    4000,
		TimeoutSec:   180,
		Temperature:  0.1,
		ImageMaxSide: 640,
		Thinking:     false,
		Candidates:   DefaultCandidates(),
		Group:        "插画",
		Fit:          "auto",
		CellSize:     800,
		JPGQuality:   95,
		TrimAlpha:    true,
		NoCrop:       boolPtr(true),

		MaxRounds:  2,
		AutoTuneOn: true,

		PrecheckOn:   true,
		PrecheckSkip: true,

		GenBaseURL: "https://junli1.junlistudio.com/v1",
		GenModel:   "gpt-image-2.5",
		GenSize:    "1024x1024",
		GenTimeout: 300,
		GenCount:   2,
		GenAutoRun: false,

		UpdateCheck: true,
	}
}

// Store 提供并发安全的配置读写。
type Store struct {
	mu   sync.RWMutex
	path string
	cur  Config
}

// NewStore 打开（或创建）配置目录下的 config.json。
func NewStore(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, "config.json"), cur: Default()}
	if err := s.load(); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		// 首次运行：落盘默认配置，方便用户直接编辑
		if err := s.saveLocked(); err != nil {
			return nil, fmt.Errorf("写入默认配置: %w", err)
		}
	}
	return s, nil
}

// Path 返回配置文件路径。
func (s *Store) Path() string { return s.path }

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	// 先铺默认值，再用文件内容覆盖，缺字段不会变零值
	c := Default()
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("解析 %s: %w", s.path, err)
	}
	// 老配置缺 no_crop 时补上默认值后回写一次，让 config.json 自解释——
	// 否则用户打开文件找不到这个键，既不知道有这层保护、也不知道怎么关。
	//
	// 注意不能用 c.NoCrop == nil 判断：上面先铺了 Default()，NoCrop 已经是
	// 非 nil 的 true，永远判不出来。要看原始文件里到到底有没有这个键。
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return fmt.Errorf("解析 %s: %w", s.path, err)
	}
	_, hasNoCrop := keys["no_crop"]
	needRewrite := !hasNoCrop

	// 手工改坏的值（cell_size=0、fit 拼错）在这里就纠回来，别留到运行期
	normalize(&c)
	s.mu.Lock()
	s.cur = c
	var werr error
	if needRewrite {
		werr = s.saveLocked()
	}
	s.mu.Unlock()
	if werr != nil {
		return fmt.Errorf("回写默认配置: %w", werr)
	}
	return nil
}

// Get 返回当前配置的副本。
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cloneLocked()
}

// Update 应用 patch 并落盘。patch 返回修改后的配置。
//
// 注意：不能在这里调用 s.Get()——sync.RWMutex 不可重入，持写锁再 RLock
// 会永久死锁（界面点「保存配置」就卡死）。
func (s *Store) Update(patch func(*Config)) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	patch(&s.cur)
	normalize(&s.cur)
	if err := s.saveLocked(); err != nil {
		return s.cloneLocked(), err
	}
	return s.cloneLocked(), nil
}

// cloneLocked 在已持锁的前提下复制一份配置。调用方必须持有 s.mu。
func (s *Store) cloneLocked() Config {
	c := s.cur
	c.Candidates = append([]float64(nil), s.cur.Candidates...)
	return c
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.cur, "", "  ")
	if err != nil {
		return err
	}
	// 原子替换，避免写一半断电把配置写坏
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// normalize 兜住明显非法的值，避免下游除零或越界。
func normalize(c *Config) {
	if c.CellSize <= 0 {
		c.CellSize = 800
	}
	if c.JPGQuality < 1 || c.JPGQuality > 100 {
		c.JPGQuality = 95
	}
	if c.ImageMaxSide < 128 {
		c.ImageMaxSide = 640
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = 4000
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = 180
	}
	if len(c.Candidates) == 0 {
		c.Candidates = DefaultCandidates()
	}
	for i, v := range c.Candidates {
		if v <= 0 {
			c.Candidates[i] = 1.0
		}
	}
	// 单档时模型无从比较，自动判断会直接跳过；给回默认区间
	if len(c.Candidates) == 1 {
		c.Candidates = DefaultCandidates()
	}
	if c.Group == "" {
		c.Group = "插画"
	}
	switch c.Fit {
	case "auto", "contain", "cover", "stretch", "none":
	default:
		c.Fit = "auto"
	}

	// 禁止裁切默认开：旧配置没有这个键时反序列化得到 nil，这里补成 true
	if c.NoCrop == nil {
		c.NoCrop = boolPtr(true)
	}

	// 多轮迭代：1~3 轮，超范围收回，0 视为未配置用默认 2
	if c.MaxRounds <= 0 {
		c.MaxRounds = 2
	}
	if c.MaxRounds > 3 {
		c.MaxRounds = 3
	}

	// 生图
	if c.GenBaseURL == "" {
		c.GenBaseURL = "https://junli1.junlistudio.com/v1"
	}
	if c.GenModel == "" {
		c.GenModel = "gpt-image-2.5"
	}
	if c.GenSize == "" {
		c.GenSize = "1024x1024"
	}
	if c.GenTimeout <= 0 {
		c.GenTimeout = 300
	}
	if c.GenCount <= 0 {
		c.GenCount = 2
	}
	if c.GenCount > 10 {
		c.GenCount = 10
	}
}

// boolPtr 返回 v 的指针。配置里的可选布尔值用指针表达，
// 才能区分「没配置」与「明确关掉」。
func boolPtr(v bool) *bool { return &v }

// NoCropOn 报告禁止裁切是否开启。未配置视为开启。
func (c Config) NoCropOn() bool {
	return c.NoCrop == nil || *c.NoCrop
}
