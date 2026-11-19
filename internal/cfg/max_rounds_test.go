package cfg

import "testing"

// 评审轮数默认 3，可上调到 10；超上限收回 10、0 视为未配置。
// 以前上限写死 3，用户设 5 会被静默压回 3，界面上看不出没生效。
func TestNormalizeMaxRoundsAllowsUpToTen(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 3}, {-5, 3}, {1, 1}, {3, 3}, {5, 5}, {7, 7}, {10, 10}, {11, 10}, {99, 10},
	} {
		c := Default()
		c.MaxRounds = tc.in
		normalize(&c)
		if c.MaxRounds != tc.want {
			t.Errorf("MaxRounds=%d 归一化后应为 %d，实际 %d", tc.in, tc.want, c.MaxRounds)
		}
	}
}
