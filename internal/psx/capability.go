package psx

import (
	"fmt"
	"strconv"
	"strings"
)

// Caps 描述某个 Photoshop 版本上本项目所依赖的自动化能力。
//
// 为什么需要它：Photoshop 的 ExtendScript / COM 能力在不同大版本之间会变，
// 而且失败方式很难看——脚本调用不存在的 API 只会回报一个含糊的 8800，
// 或者干脆弹模态框把 COM 调用永久挂起。把「版本 → 可用能力」显式建模，
// 就能在真正开文档之前给出可读的中文错误，而不是等 300 秒超时。
type Caps struct {
	// Version 是传入的原始版本号字符串，便于日志与错误信息还原现场。
	Version string
	// Major 是解析出的主版本号；0 表示无法解析（空串、非数字前缀等）。
	Major int
	// Verified 表示该大版本是否已被本项目实测验证过。
	Verified bool

	// DoJavaScript 对应 app.DoJavaScript(js, [], 1)。
	DoJavaScript bool
	// NoDialogs 对应 app.DisplayDialogs = PsDisplayNoDialogs(3)。
	NoDialogs bool
	// EditSmartObject 对应 placedLayerEditContents（进入智能对象内部）。
	EditSmartObject bool
	// ReplaceContents 对应 placedLayerReplaceContents。
	//
	// 恒为 false：该 API 虽然可用，但会破坏智能对象原有的透视变换
	// （实测 bounds 从 271x601 变成 1331x3211），本项目一律走
	// placedLayerEditContents 进入内部再置入的路径。
	ReplaceContents bool

	// Notes 记录版本相关的约束与踩过的坑，供日志/界面展示。
	Notes []string
}

// verifiedMajor 是已实测过的 Photoshop 大版本。
//
// 27.x 的实测结论：DoJavaScript(js, [], 1) 可用；placedLayerEditContents 可用；
// 必须把 DisplayDialogs 设成 3，否则任何模态框都会让 COM 调用永久挂起。
const verifiedMajor = 27

// minSmartObjectMajor 是支持智能对象「编辑内容」的最低大版本（CS6 = 13）。
//
// 低于它的版本无法进入智能对象内部替换图案，本项目没有可用替代路径。
const minSmartObjectMajor = 13

// 所有版本都适用的实现约束，写进 Notes 让调用方一眼看到「为什么代码长这样」。
var universalNotes = []string{
	"placedLayerReplaceContents 会破坏智能对象原有透视变换（实测 bounds 271x601 变 1331x3211），本项目不使用该路径",
	"executeAction(charIDToTypeID(\"getd\"), d, ...) 会报错误 8800，读属性必须用 executeActionGet(ref)；ActionDescriptor 只能在 JSX 内 new，win32com 无法实例化",
}

// For 根据 Photoshop 版本号返回该版本的能力集合。
//
// 解析规则：去掉前导 v/V 与空白后取开头的连续数字作为主版本号。
// 版本号为空或无法解析时返回「保守全开」的默认值并在 Notes 里标注未验证——
// 宁可先按能用来跑，也不要因为读不到版本号就拒绝工作。
func For(version string) Caps {
	c := Caps{
		Version:         version,
		Major:           parseMajor(version),
		DoJavaScript:    true,
		NoDialogs:       true,
		EditSmartObject: true,
		// 见字段注释：恒 false，与版本无关。
		ReplaceContents: false,
		Notes:           append([]string(nil), universalNotes...),
	}

	switch {
	case c.Major == 0:
		// 空版本 / 无法解析：保守全开，但明确标注未验证。
		c.Notes = append(c.Notes,
			fmt.Sprintf("Photoshop 版本号未知（%q），按保守默认启用全部核心能力，请在首次运行时实测确认", version))
	case c.Major == verifiedMajor:
		c.Verified = true
	case c.Major < minSmartObjectMajor:
		// 真正的能力缺口：老版本没有智能对象编辑，且没有替代路径。
		c.EditSmartObject = false
		c.Notes = append(c.Notes,
			fmt.Sprintf("Photoshop %d.x 不支持智能对象编辑内容（placedLayerEditContents），需要 CS6（13）及以上", c.Major))
	case c.Major > verifiedMajor:
		c.Notes = append(c.Notes,
			fmt.Sprintf("Photoshop %d.x 未实测，按 %d.x 的已验证行为保守全开；若渲染异常请回报版本号", c.Major, verifiedMajor))
	default:
		c.Notes = append(c.Notes,
			fmt.Sprintf("Photoshop %d.x 未实测，按保守全开处理；若渲染异常请回报版本号", c.Major))
	}
	return c
}

// Check 校验必需能力是否齐备，缺失时返回中文错误。
//
// 只校验「没有它就跑不动」的三项；ReplaceContents 不在其中，
// 因为本项目根本不走那条路径。
func (c Caps) Check() error {
	var missing []string
	if !c.DoJavaScript {
		missing = append(missing, "app.DoJavaScript")
	}
	if !c.NoDialogs {
		missing = append(missing, "app.DisplayDialogs = PsDisplayNoDialogs")
	}
	if !c.EditSmartObject {
		missing = append(missing, "placedLayerEditContents（智能对象编辑内容）")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("Photoshop %s 缺少必需能力：%s；请升级 Photoshop（CS6/13 及以上）后重试",
		c.versionText(), strings.Join(missing, "、"))
}

// versionText 返回用于错误信息的版本描述。
func (c Caps) versionText() string {
	if c.Major > 0 {
		return fmt.Sprintf("%d.x", c.Major)
	}
	if strings.TrimSpace(c.Version) == "" {
		return "版本未知"
	}
	return fmt.Sprintf("版本 %q", c.Version)
}

// Summary 返回一行人类可读的能力摘要，供日志与「诊断」界面使用。
func (c Caps) Summary() string {
	on := func(b bool) string {
		if b {
			return "是"
		}
		return "否"
	}
	verified := "未验证"
	if c.Verified {
		verified = "已实测"
	}
	return fmt.Sprintf("PS %s（%s）DoJavaScript=%s 无对话框=%s 编辑智能对象=%s 替换内容=%s",
		c.versionText(), verified, on(c.DoJavaScript), on(c.NoDialogs), on(c.EditSmartObject), on(c.ReplaceContents))
}

// parseMajor 从版本号里取出主版本号，取不到时返回 0。
//
// 容忍这些写法："27.7.0"、"v27.7"、" 27 "、"27.7.0 (20250610.m.2200)"。
func parseMajor(version string) int {
	s := strings.TrimSpace(version)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")

	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// Caps 读取当前会话的 Photoshop 版本并返回对应能力集合。
//
// 版本读取失败不算致命：返回 For("") 的保守默认，让上层仍能继续尝试，
// 只是会在 Notes 里标出「版本未知」。
func (s *Session) Caps() Caps {
	ver, err := s.Probe()
	if err != nil {
		return For("")
	}
	return For(ver)
}

