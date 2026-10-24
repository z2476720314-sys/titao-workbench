package cfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestUpdateDoesNotDeadlock 复现并守住「保存配置」死锁：
// 旧实现持写锁后调 Get()（内部 RLock），sync.RWMutex 不可重入 → 永久卡死。
func TestUpdateDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := s.Update(func(c *Config) {
			c.Model = "changed-model"
			c.Candidates = []float64{1.0, 1.1}
		})
		if err != nil {
			t.Errorf("Update: %v", err)
		}
		if got.Model != "changed-model" {
			t.Errorf("Update 返回值未反映修改: %q", got.Model)
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Update 超时未返回：写锁内重入读锁导致死锁")
	}

	// 落盘后重新加载，确认持久化生效
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if s2.Get().Model != "changed-model" {
		t.Errorf("重新加载后 model = %q", s2.Get().Model)
	}
}

// TestGetReturnsIndependentCopy 守住 Candidates 切片共享底层数组的隐患。
func TestGetReturnsIndependentCopy(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	c := s.Get()
	if len(c.Candidates) == 0 {
		t.Fatal("默认 candidates 为空")
	}
	c.Candidates[0] = 99
	if s.Get().Candidates[0] == 99 {
		t.Fatal("Get 返回的 Candidates 与内部共享底层数组，调用方改动污染了配置")
	}
}

// TestConcurrentGetUpdate 守住读写并发安全（配合 -race）。
func TestConcurrentGetUpdate(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.Get() }()
		go func() {
			defer wg.Done()
			_, _ = s.Update(func(c *Config) { c.Group = "插画" })
		}()
	}
	wg.Wait()
}

// TestNormalizeRepairsBrokenFile 守住手工改坏的 config.json 在加载时被纠正。
func TestNormalizeRepairsBrokenFile(t *testing.T) {
	dir := t.TempDir()
	broken := `{"cell_size":0,"jpg_quality":0,"fit":"tpyo","candidates":[1.0],"group":"","output_dir":""}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	c := s.Get()
	if c.CellSize <= 0 {
		t.Errorf("cell_size 未修正: %d", c.CellSize)
	}
	if c.JPGQuality <= 0 {
		t.Errorf("jpg_quality 未修正: %d", c.JPGQuality)
	}
	if c.Fit != "auto" && c.Fit != "contain" && c.Fit != "cover" && c.Fit != "stretch" && c.Fit != "none" {
		t.Errorf("fit 未修正: %q", c.Fit)
	}
	if len(c.Candidates) < 2 {
		t.Errorf("单档 candidates 未补回默认区间: %v", c.Candidates)
	}
	if c.Group == "" {
		t.Error("group 未补默认值")
	}
	if c.OutputDir == "" {
		t.Error("output_dir 未补默认值")
	}
}

// TestNoCropDefaultsOnForLegacyConfig 守住「老配置升级后不裁切保护不丢」。
//
// no_crop 是 v0.3.1 新增字段。若用裸 bool，旧 config.json 没有这个键时
// 反序列化得到零值 false —— 用户什么都没做，「图案必须完整可见」就被静默关掉了。
// 用指针 + normalize 补默认才能区分「没配置」与「明确关掉」。
func TestNoCropDefaultsOnForLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// 模拟 v0.3.0 的配置：有 trim_alpha，但没有 no_crop
	legacy := `{"output_dir":"E:\\套图输出","fit":"auto","trim_alpha":true,"cell_size":800}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if !st.Get().NoCropOn() {
		t.Error("旧配置缺少 no_crop 时应默认开启禁止裁切，实际为关闭")
	}

	// 用户明确关掉时必须保留
	off := `{"no_crop":false}`
	if err := os.WriteFile(path, []byte(off), 0o644); err != nil {
		t.Fatal(err)
	}
	st2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if st2.Get().NoCropOn() {
		t.Error("配置里明确写了 no_crop:false，不应被 normalize 改回开启")
	}
}

// TestLegacyConfigGetsNoCropWrittenBack 确认老配置会被补上 no_crop 并回写。
//
// 只补内存值不回写的话，用户打开 config.json 找不到这个键，
// 既不知道有「不裁切」这层保护，也不知道怎么关掉。
func TestLegacyConfigGetsNoCropWrittenBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"fit":"auto","trim_alpha":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStore(dir); err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("回写后的配置不是合法 JSON: %v", err)
	}
	if v, ok := got["no_crop"]; !ok {
		t.Fatal("老配置回写后应出现 no_crop 键")
	} else if v != true {
		t.Errorf("回写的 no_crop 应为 true，实际 %v", v)
	}
}
