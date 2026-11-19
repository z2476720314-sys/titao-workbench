package jobs

import "testing"

// 止损判定的单元测试。
//
// 这套判定决定失败任务要不要把剩下几轮跑完。每轮要重渲染全组 7 格再逐格评审，
// 实测 44-52s，判错一次就是白烧一分钟和一轮模型额度。

func TestStallStopsWhenScoreDrops(t *testing.T) {
	s := newStallTracker()
	// 首轮建立基准 70
	if s.observe(70) {
		t.Fatal("第 1 轮不该止损")
	}
	// 第 2 轮 65 低于 70 → 阈值 1，应立即止损
	if !s.observe(65) {
		t.Fatalf("分数从 70 掉到 65 应止损，best=%d", s.Best())
	}
	if s.Best() != 70 {
		t.Errorf("最好分应保留 70，实际 %d", s.Best())
	}
}

func TestStallKeepsGoingWhileScoreRises(t *testing.T) {
	s := newStallTracker()
	for _, score := range []int{35, 55, 96} {
		if s.observe(score) {
			t.Fatalf("分数在上升（%d）不该止损", score)
		}
	}
	if s.Best() != 96 {
		t.Errorf("最好分应为 96，实际 %d", s.Best())
	}
}

// 小幅回升但始终不如历史最好分，也该停：70 → 65 → 67 是白跑一轮。
func TestStallStopsOnPartialRecoveryBelowBest(t *testing.T) {
	s := newStallTracker()
	s.observe(70)
	// 65 低于 70：阈值 1 已经触发
	if !s.observe(65) {
		t.Fatal("70→65 应止损")
	}
}

// 分数打平不算变差：模型换了个说法、画面质量和上一轮相当，
// 下一轮仍可能改对，不该据此提前结束。
func TestStallKeepsGoingOnEqualScore(t *testing.T) {
	s := newStallTracker()
	if s.observe(72) {
		t.Fatal("首轮不该止损")
	}
	if s.observe(72) {
		t.Fatal("72→72 同分不该止损，下一轮仍有机会")
	}
	if s.observe(72) {
		t.Fatal("连续同分仍不该止损")
	}
	if s.Best() != 72 {
		t.Errorf("最好分应保持 72，实际 %d", s.Best())
	}
}

// 评审失败（分数 < 0）不参与止损比较：
// 那条路径有自己的 break 和「评审未完成」文案，不能被止损抢先改写。
func TestStallIgnoresFailedReview(t *testing.T) {
	s := newStallTracker()
	s.observe(80)
	if s.observe(-1) {
		t.Fatal("评审失败不该触发止损判定")
	}
	if s.Best() != 80 {
		t.Errorf("评审失败不该污染最好分，实际 %d", s.Best())
	}
}

// 阈值必须是 1：3 轮上限下「连续 2 轮」最早只在第 3 轮判定，等于永不生效。
func TestStallThresholdIsOne(t *testing.T) {
	if maxStalledRounds != 1 {
		t.Fatalf("止损阈值应为 1，实际 %d", maxStalledRounds)
	}
}

// 真实轨迹回归：本批 3 轮任务在阈值 1 下应省下的轮数。
func TestStallSavesRoundsOnRealTrajectories(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scores   []int
		stopAt   int // 期望跑到第几轮（1 基）
		wantStop bool
	}{
		{"70→65→62", []int{70, 65, 62}, 2, true},
		// 第 2 轮掉到 45（低于最好 72）即止损，不会跑到第 3 轮。
		{"72→45→72", []int{72, 45, 72}, 2, true},
		{"35→55→96", []int{35, 55, 96}, 0, false},
		{"45→35", []int{45, 35}, 2, true},
		{"74→96", []int{74, 96}, 0, false},
		{"58→35", []int{58, 35}, 2, true},
		{"72→72→72", []int{72, 72, 72}, 0, false},
	} {
		s := newStallTracker()
		stoppedAt := 0
		for i, score := range tc.scores {
			if s.observe(score) {
				stoppedAt = i + 1
				break
			}
		}
		if tc.wantStop {
			if stoppedAt != tc.stopAt {
				t.Errorf("%s：应在第 %d 轮止损，实际 %d", tc.name, tc.stopAt, stoppedAt)
			}
		} else if stoppedAt != 0 {
			t.Errorf("%s：不该止损，却在第 %d 轮停了", tc.name, stoppedAt)
		}
	}
}
