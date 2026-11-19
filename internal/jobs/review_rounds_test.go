package jobs

import (
	"testing"

	"titao/internal/cfg"
)

// 评审轮数是用户可调参数：默认 3，可上调到 10。
// 以前硬编码上限 3，用户把配置改成 5 也会被静默压回 3，界面看着调了但实际没生效。
func TestReviewRoundsHonorUserSettingUpToTen(t *testing.T) {
	def := cfg.Default()
	def.AutoTuneOn = true

	for _, want := range []int{1, 2, 3, 5, 8, 10} {
		opt := Options{Auto: true, MaxRounds: want}
		on, got := effectiveReviewSettings(opt, def)
		if !on || got != want {
			t.Errorf("MaxRounds=%d 时应评审 %d 轮，实际 reviewOn=%t rounds=%d", want, want, on, got)
		}
	}
}

// 超过硬上限才收回，且必须是 10 而不是 3。
func TestReviewRoundsClampAtHardLimit(t *testing.T) {
	def := cfg.Default()
	def.AutoTuneOn = true
	for _, in := range []int{11, 50, 999} {
		on, got := effectiveReviewSettings(Options{Auto: true, MaxRounds: in}, def)
		if !on || got != maxReviewRounds {
			t.Errorf("MaxRounds=%d 应收回到 %d，实际 rounds=%d", in, maxReviewRounds, got)
		}
	}
	if maxReviewRounds != 10 {
		t.Errorf("硬上限应为 10，实际 %d", maxReviewRounds)
	}
}

// 请求里没给轮数时取配置值，配置值也支持到 10。
func TestReviewRoundsFallBackToConfig(t *testing.T) {
	for _, want := range []int{1, 3, 7, 10} {
		def := cfg.Default()
		def.AutoTuneOn = true
		def.MaxRounds = want
		on, got := effectiveReviewSettings(Options{Auto: true}, def)
		if !on || got != want {
			t.Errorf("配置 MaxRounds=%d 应生效，实际 reviewOn=%t rounds=%d", want, on, got)
		}
	}
}

// 关闭自动微调时无论写几轮都只跑一轮：没有评审就没有第二轮的意义。
func TestReviewDisabledForcesSingleRound(t *testing.T) {
	def := cfg.Default()
	def.AutoTuneOn = false
	on, got := effectiveReviewSettings(Options{Auto: true, MaxRounds: 10}, def)
	if on || got != 1 {
		t.Errorf("关闭自动微调应 reviewOn=false rounds=1，实际 reviewOn=%t rounds=%d", on, got)
	}
	// 关掉 auto 也一样
	def.AutoTuneOn = true
	on, got = effectiveReviewSettings(Options{Auto: false, MaxRounds: 10}, def)
	if on || got != 1 {
		t.Errorf("auto=false 应 reviewOn=false rounds=1，实际 reviewOn=%t rounds=%d", on, got)
	}
}
