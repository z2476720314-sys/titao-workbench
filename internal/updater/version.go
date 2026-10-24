package updater

import (
	"strconv"
	"strings"
)

// Compare 比较两个版本号，返回 -1 / 0 / 1（语义与 strings.Compare 一致）：
//
//	-1  current < latest（有更新）
//	 0  current == latest（已是最新）
//	 1  current > latest（本地比远端新，通常是开发中）
//
// 规则按语义化版本（SemVer）实现，因为 GitHub tag 就是按它排的：
//   - 前导 v/V 忽略；段数不足按 0 补齐（"1.2" == "1.2.0"）
//   - 非数字段按 0 处理，不 panic 也不报错（"abc" == "0.0.0"）
//   - 有预发布标记的版本小于同号的正式版（"1.0.0-beta" < "1.0.0"）
//   - 预发布标记按 SemVer 规则逐段比较：数字段按数值、其余按字典序，
//     数字段小于非数字段（"1.0.0-1" < "1.0.0-alpha"）
//
// 构建元数据（"+" 之后）不参与比较。
func Compare(current, latest string) int {
	a := parseVersion(current)
	b := parseVersion(latest)
	return a.compare(b)
}

// version 是解析后的版本号。
type version struct {
	nums []int
	pre  []string // 预发布标识；空切片表示正式版
}

// parseVersion 把任意字符串解析成可比较的 version，永不失败。
func parseVersion(s string) version {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	// 构建元数据不参与比较
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	// 预发布标记
	var pre []string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = strings.Split(s[i+1:], ".")
		s = s[:i]
	}

	var nums []int
	for _, part := range strings.Split(s, ".") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 {
			// 非数字段一律当 0：版本号脏了也不该让更新检查崩掉
			n = 0
		}
		nums = append(nums, n)
	}
	if len(nums) == 0 {
		nums = []int{0}
	}
	return version{nums: nums, pre: pre}
}

func (a version) compare(b version) int {
	// 主版本段：短的补 0 后逐段比较
	n := len(a.nums)
	if len(b.nums) > n {
		n = len(b.nums)
	}
	for i := 0; i < n; i++ {
		if c := cmpInt(at(a.nums, i), at(b.nums, i)); c != 0 {
			return c
		}
	}
	// 主版本相同时，正式版 > 预发布版
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	// 都是预发布：逐段比较，先耗尽者更小
	m := len(a.pre)
	if len(b.pre) < m {
		m = len(b.pre)
	}
	for i := 0; i < m; i++ {
		if c := cmpPreIdent(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(a.pre), len(b.pre))
}

// cmpPreIdent 按 SemVer 比较单个预发布标识。
//
// 数字标识按数值比较且小于字母标识；非数字标识按 ASCII 字典序。
func cmpPreIdent(x, y string) int {
	xn, xErr := strconv.Atoi(x)
	yn, yErr := strconv.Atoi(y)
	switch {
	case xErr == nil && yErr == nil:
		return cmpInt(xn, yn)
	case xErr == nil:
		return -1 // 数字 < 字母
	case yErr == nil:
		return 1
	default:
		return strings.Compare(x, y)
	}
}

// at 取切片第 i 个元素，越界返回 0（用于段数不等的版本号）。
func at(s []int, i int) int {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// IsNewer 报告 latest 是否比 current 新。
func IsNewer(current, latest string) bool {
	return Compare(current, latest) < 0
}

// NormalizeTag 去掉 tag 的 v 前缀，用于显示与文件名。
func NormalizeTag(tag string) string {
	return strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(tag), "v"), "V")
}
