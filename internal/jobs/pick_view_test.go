package jobs

import "testing"

// 评审只送一张「能完整看到壳面正面」的图，不再逐格都发。
// 选错格子会让模型看到侧面或倒置画面，把正常排版误判成遮挡。

func TestPickReviewViewPrefersUprightCell(t *testing.T) {
	// 格 0/1 需要旋转校正（侧面或倒置），格 2 正立。
	projection := &Task{ReviewQuarterTurns: map[int]int{0: 1, 1: 3, 2: 0}}
	idx, ok := pickReviewView(projection, []int{0, 1, 2})
	if !ok {
		t.Fatal("应该有可选格")
	}
	if got := []int{0, 1, 2}[idx]; got != 2 {
		t.Errorf("应选正立的格 2，实际选了格 %d", got)
	}
}

func TestPickReviewViewSkipsStaticCells(t *testing.T) {
	// 格 0 是模板自带的侧面静态图（图案没印上去），必须跳过。
	projection := &Task{StaticCells: []int{0}, ReviewQuarterTurns: map[int]int{0: 0, 1: 1}}
	idx, ok := pickReviewView(projection, []int{1}) // 静态格已在构建时被排除
	if !ok {
		t.Fatal("应该有可选格")
	}
	if got := []int{1}[idx]; got != 1 {
		t.Errorf("应选格 1，实际 %d", got)
	}
}

func TestPickReviewViewFallsBackWhenAllRotated(t *testing.T) {
	// 全部旋转时选编号最小的，不能因此卡死整组。
	projection := &Task{ReviewQuarterTurns: map[int]int{3: 1, 5: 2}}
	idx, ok := pickReviewView(projection, []int{3, 5})
	if !ok {
		t.Fatal("全旋转时也该选出可用格")
	}
	if got := []int{3, 5}[idx]; got != 3 {
		t.Errorf("全旋转时应选编号最小的格 3，实际 %d", got)
	}
}

func TestPickReviewViewEmpty(t *testing.T) {
	if _, ok := pickReviewView(&Task{}, nil); ok {
		t.Error("没有候选格时应报不可用，而不是返回 0")
	}
}

func TestPickReviewViewPrefersLowestUprightCellNumber(t *testing.T) {
	// 多个正立格时选编号最小的：模板通常把主视角排在最前。
	projection := &Task{ReviewQuarterTurns: map[int]int{4: 0, 6: 0}}
	idx, ok := pickReviewView(projection, []int{4, 6})
	if !ok {
		t.Fatal("应该有可选格")
	}
	if got := []int{4, 6}[idx]; got != 4 {
		t.Errorf("应选编号最小的正立格 4，实际 %d", got)
	}
}
