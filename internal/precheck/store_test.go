package precheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func rec(path string, size, mtime int64, ok bool, reason string) Record {
	return Record{Path: path, Size: size, MTime: mtime, OK: ok, Reason: reason,
		Tags: []string{TagWatermark}}
}

// TestStoreMissThenHit 守住「先未命中、写后命中」的基本往返。
func TestStoreMissThenHit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "precheck.jsonl")
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, ok := s.Lookup(`C:\a.png`, 100, 1000); ok {
		t.Fatal("空库不该命中")
	}
	if err := s.Append(rec(`C:\a.png`, 100, 1000, false, "有水印")); err != nil {
		t.Fatal(err)
	}
	r, ok := s.Lookup(`C:\a.png`, 100, 1000)
	if !ok {
		t.Fatal("写入后应命中")
	}
	if r.OK || r.Reason != "有水印" {
		t.Errorf("记录内容不符: %+v", r)
	}
}

// TestStoreSizeChangeMisses 守住改了 size 必须重判（用户替换了同名文件）。
func TestStoreSizeChangeMisses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "precheck.jsonl")
	s, _ := NewStore(p)
	defer s.Close()
	s.Append(rec(`C:\a.png`, 100, 1000, false, "有水印"))

	if _, ok := s.Lookup(`C:\a.png`, 101, 1000); ok {
		t.Error("size 变了必须未命中")
	}
}

// TestStoreMTimeChangeMisses 守住改了 mtime 必须重判。
func TestStoreMTimeChangeMisses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "precheck.jsonl")
	s, _ := NewStore(p)
	defer s.Close()
	s.Append(rec(`C:\a.png`, 100, 1000, false, "有水印"))

	if _, ok := s.Lookup(`C:\a.png`, 100, 1001); ok {
		t.Error("mtime 变了必须未命中")
	}
}

// TestStorePathChangeMisses 守住换了路径不算同一条。
func TestStorePathChangeMisses(t *testing.T) {
	p := filepath.Join(t.TempDir(), "precheck.jsonl")
	s, _ := NewStore(p)
	defer s.Close()
	s.Append(rec(`C:\a.png`, 100, 1000, false, "有水印"))

	if _, ok := s.Lookup(`C:\b.png`, 100, 1000); ok {
		t.Error("路径不同必须未命中")
	}
}

// TestStorePersistsAcrossReload 守住启动时从 jsonl 读回内存 map。
func TestStorePersistsAcrossReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "precheck.jsonl")
	s, _ := NewStore(p)
	s.Append(rec(`C:\a.png`, 100, 1000, false, "有镜头框"))
	s.Append(rec(`C:\b.png`, 200, 2000, true, "干净"))
	s.Close()

	s2, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Len() != 2 {
		t.Fatalf("Len = %d，期望 2", s2.Len())
	}
	if r, ok := s2.Lookup(`C:\a.png`, 100, 1000); !ok || r.OK {
		t.Errorf("a.png 记录不符: ok=%v %+v", ok, r)
	}
	if r, ok := s2.Lookup(`C:\b.png`, 200, 2000); !ok || !r.OK {
		t.Errorf("b.png 记录不符: ok=%v %+v", ok, r)
	}
}

// TestStoreAppendsWithoutRewrite 守住「只追加不重写」：老行必须原样保留。
func TestStoreAppendsWithoutRewrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "precheck.jsonl")
	s, _ := NewStore(p)
	s.Append(rec(`C:\a.png`, 100, 1000, false, "一"))
	s.Append(rec(`C:\b.png`, 200, 2000, true, "二"))
	s.Close()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON: %v", n+1, err)
		}
		n++
	}
	if n != 2 {
		t.Errorf("文件里有 %d 行，期望 2 行", n)
	}
}

// TestStoreSkipsCorruptLine 守住坏行不拖垮整个库。
func TestStoreSkipsCorruptLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "precheck.jsonl")
	good, _ := json.Marshal(rec(`C:\ok.png`, 1, 2, true, "干净"))
	content := string(good) + "\n{这不是 json\n\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := NewStore(p)
	if err != nil {
		t.Fatalf("坏行不该让 NewStore 失败: %v", err)
	}
	defer s.Close()
	if s.Len() != 1 {
		t.Errorf("Len = %d，期望 1（坏行被跳过）", s.Len())
	}
	if _, ok := s.Lookup(`C:\ok.png`, 1, 2); !ok {
		t.Error("好行应被读入")
	}
}

// TestStoreMissingFileIsOK 守住首次运行没有 jsonl 不是错误。
func TestStoreMissingFileIsOK(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("文件不存在不该报错: %v", err)
	}
	defer s.Close()
	if s.Len() != 0 {
		t.Errorf("Len = %d，期望 0", s.Len())
	}
}

// TestStoreCreatesParentDir 守住目录不存在时自动建。
func TestStoreCreatesParentDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "deeper", "precheck.jsonl")
	s, err := NewStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Append(rec(`C:\a.png`, 1, 2, true, "干净")); err != nil {
		t.Fatalf("应自动创建父目录: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("文件未创建: %v", err)
	}
}

// TestStoreAtTimestamp 守住 Append 自动补时间戳。
func TestStoreAtTimestamp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "precheck.jsonl")
	s, _ := NewStore(p)
	defer s.Close()
	s.Append(rec(`C:\a.png`, 1, 2, true, "干净"))

	r, ok := s.Lookup(`C:\a.png`, 1, 2)
	if !ok {
		t.Fatal("应命中")
	}
	if r.At == "" {
		t.Error("At 应被自动填充")
	}
}

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	out = append(out, cur)
	return out
}
