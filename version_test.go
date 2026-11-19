package main

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 版本号规范由测试守着，不靠人记：
//  1. main.go 的 var Version 是唯一来源；
//  2. CHANGELOG.md 必须存在与它完全一致的 `## vX.Y.Z — YYYY-MM-DD` 小节；
//  3. 源码和前端里不得再出现别的生产版本字面量。
//
// 只改 Version 不写 CHANGELOG（或反之）都会让 `go test ./...` 失败。

var changelogHeading = regexp.MustCompile(`^## v(\d+\.\d+\.\d+) — (\d{4}-\d{2}-\d{2})\s*$`)

func changelogText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatalf("读取 CHANGELOG.md 失败：%v", err)
	}
	return string(data)
}

// TestVersionIsSemver 保证 Version 是干净的三段式版本号。
func TestVersionIsSemver(t *testing.T) {
	if _, err := strconv.Atoi(strings.TrimPrefix(Version, "v")); err == nil {
		t.Fatalf("Version 不能只是单个数字：%q", Version)
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Fatalf("Version 必须是 X.Y.Z 三段式，实际 %q", Version)
	}
}

// TestChangelogHasCurrentVersionSection 保证每次改版本号都同步了更新日志。
func TestChangelogHasCurrentVersionSection(t *testing.T) {
	text := changelogText(t)
	found := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		m := changelogHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] != Version {
			continue
		}
		found = true
		if strings.Count(text, "## v"+Version+" —") > 1 {
			t.Fatalf("CHANGELOG.md 里 v%s 出现了重复小节", Version)
		}
		break
	}
	if !found {
		t.Fatalf("CHANGELOG.md 缺少 `## v%s — YYYY-MM-DD` 小节；改 main.go 的 Version 必须同时新增更新日志", Version)
	}
}

// TestChangelogHeadingsAreOrdered 保证更新日志按版本从新到旧排列，最新在最上面。
func TestChangelogHeadingsAreOrdered(t *testing.T) {
	var versions []string
	for _, line := range strings.Split(changelogText(t), "\n") {
		line = strings.TrimRight(line, "\r")
		if m := changelogHeading.FindStringSubmatch(line); m != nil {
			versions = append(versions, m[1])
		}
	}
	if len(versions) == 0 {
		t.Fatal("CHANGELOG.md 里没有任何 `## vX.Y.Z — YYYY-MM-DD` 小节")
	}
	if versions[0] != Version {
		t.Fatalf("CHANGELOG.md 最新小节是 v%s，应等于当前 Version v%s", versions[0], Version)
	}
	for i := 1; i < len(versions); i++ {
		if compareSemver(versions[i-1], versions[i]) <= 0 {
			t.Fatalf("CHANGELOG.md 顺序错误：v%s 必须新于 v%s", versions[i-1], versions[i])
		}
	}
}

func compareSemver(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}
