package psx

import (
	"strings"
	"testing"
)

// TestForVerified27 守住已实测版本 27.x 的能力矩阵。
//
// 27.7.0 是本项目实际跑通的版本：DoJavaScript 可用、能进智能对象、
// 必须禁掉对话框（否则模态框让 COM 永久挂起）。
func TestForVerified27(t *testing.T) {
	c := For("27.7.0")

	if c.Major != 27 {
		t.Fatalf("Major = %d，期望 27", c.Major)
	}
	if !c.Verified {
		t.Error("27.x 应标记为已实测")
	}
	if !c.DoJavaScript {
		t.Error("27.x 应支持 DoJavaScript")
	}
	if !c.NoDialogs {
		t.Error("27.x 必须支持 DisplayDialogs = 3")
	}
	if !c.EditSmartObject {
		t.Error("27.x 应支持 placedLayerEditContents")
	}
	if c.ReplaceContents {
		t.Error("ReplaceContents 必须恒为 false（会破坏透视变换）")
	}
	if err := c.Check(); err != nil {
		t.Errorf("27.x 的 Check 不应报错: %v", err)
	}
}

// TestForUnknownVersion 守住未知/异常版本号走「保守全开 + 未验证」分支。
//
// 读不到版本号时不该直接拒绝工作，但必须让调用方知道这是未验证状态。
func TestForUnknownVersion(t *testing.T) {
	for _, v := range []string{"abc", "Photoshop", "x.y.z", "v", "-1", "0"} {
		c := For(v)
		if c.Major != 0 {
			t.Errorf("For(%q).Major = %d，期望 0", v, c.Major)
		}
		if c.Verified {
			t.Errorf("For(%q) 不应标记为已实测", v)
		}
		if !c.DoJavaScript || !c.NoDialogs || !c.EditSmartObject {
			t.Errorf("For(%q) 应保守全开核心能力，实际 %+v", v, c)
		}
		if err := c.Check(); err != nil {
			t.Errorf("For(%q).Check 不应报错: %v", v, err)
		}
		if !hasNote(c.Notes, "未知") {
			t.Errorf("For(%q) 的 Notes 应说明版本未知，实际 %v", v, c.Notes)
		}
	}
}

// TestForEmptyVersion 守住空版本号：保守默认 + 明确标注。
func TestForEmptyVersion(t *testing.T) {
	for _, v := range []string{"", "   ", "\t"} {
		c := For(v)
		if c.Major != 0 {
			t.Errorf("For(%q).Major = %d，期望 0", v, c.Major)
		}
		if c.Verified {
			t.Errorf("For(%q) 不应标记为已实测", v)
		}
		if err := c.Check(); err != nil {
			t.Errorf("For(%q).Check 不应报错: %v", v, err)
		}
		if !hasNote(c.Notes, "未知") {
			t.Errorf("For(%q) 的 Notes 应说明版本未知，实际 %v", v, c.Notes)
		}
	}
}

// TestForNewerUnverified 守住「比实测版本更新」时保守全开但标注未验证。
func TestForNewerUnverified(t *testing.T) {
	c := For("28.0.1")
	if c.Major != 28 {
		t.Fatalf("Major = %d，期望 28", c.Major)
	}
	if c.Verified {
		t.Error("28.x 尚未实测，Verified 应为 false")
	}
	if !c.EditSmartObject {
		t.Error("28.x 应按保守策略启用智能对象编辑")
	}
	if err := c.Check(); err != nil {
		t.Errorf("28.x 的 Check 不应报错: %v", err)
	}
	if !hasNote(c.Notes, "未实测") {
		t.Errorf("28.x 的 Notes 应说明未实测，实际 %v", c.Notes)
	}
}

// TestForLegacyLacksSmartObject 守住真正的能力缺口：低于 CS6 没有智能对象编辑。
func TestForLegacyLacksSmartObject(t *testing.T) {
	c := For("12.0")
	if c.EditSmartObject {
		t.Error("PS 12.x 不支持 placedLayerEditContents")
	}
	err := c.Check()
	if err == nil {
		t.Fatal("PS 12.x 的 Check 应报错")
	}
	if !strings.Contains(err.Error(), "智能对象") {
		t.Errorf("错误信息应点明智能对象能力缺失，实际: %v", err)
	}
}

// TestParseMajor 守住版本号解析的容错：前后空白、v 前缀、括号后缀。
func TestParseMajor(t *testing.T) {
	cases := map[string]int{
		"27.7.0":                 27,
		"v27.7":                  27,
		"V27":                    27,
		" 27 ":                   27,
		"27.7.0 (20250610.m.2200)": 27,
		"13":                     13,
		"":                       0,
		"   ":                    0,
		"abc":                    0,
		"v":                      0,
	}
	for in, want := range cases {
		if got := parseMajor(in); got != want {
			t.Errorf("parseMajor(%q) = %d，期望 %d", in, got, want)
		}
	}
}

// TestCapsSummaryAndVersionText 守住摘要/错误文案不会 panic 且含关键信息。
func TestCapsSummaryAndVersionText(t *testing.T) {
	c := For("27.7.0")
	s := c.Summary()
	if !strings.Contains(s, "27.x") || !strings.Contains(s, "已实测") {
		t.Errorf("Summary 应含版本与验证状态，实际: %s", s)
	}
	if c.ReplaceContents {
		t.Error("Summary 场景下 ReplaceContents 仍应为 false")
	}

	if got := For("").versionText(); got != "版本未知" {
		t.Errorf("空版本 versionText = %q，期望「版本未知」", got)
	}
	if got := For("abc").versionText(); !strings.Contains(got, "abc") {
		t.Errorf("无法解析的版本应回显原文，实际: %q", got)
	}
}

// TestForIsPure 守住 For 不会共享 Notes 底层数组（调用方 append 不能污染下次结果）。
func TestForIsPure(t *testing.T) {
	a := For("27.7.0")
	a.Notes = append(a.Notes, "调用方自己加的")
	b := For("27.7.0")
	for _, n := range b.Notes {
		if n == "调用方自己加的" {
			t.Fatal("For 返回的 Notes 与其它调用共享底层数组，被调用方污染")
		}
	}
}

func hasNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}
