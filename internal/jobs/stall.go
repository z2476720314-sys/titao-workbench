package jobs

// maxStalledRounds 是一轮分数低于历史最好分就停止微调的容忍轮数。
//
// 只在「分数真的掉下去」时止损，掉平不算：
// 同分说明这一轮调整后画面质量和上一轮相当、模型只是换了个说法，
// 下一轮仍可能改对；直接停会把一个还在探索的方向误判为走不通。
//
// 为什么容忍轮数是 1：3 轮上限下「连续 2 轮」最早只能在第 3 轮判定，
// 而第 3 轮本身就是最后一轮，等于永远不生效（实测本批 6 个多轮任务省下 0 秒）。
// 设成 1 才能在中间轮次真正止损：实测 70→65 在第 2 轮停省 44s，72→45 省 51s。
//
// 掉分才停的代价可控：最终仍输出历史最高分那一轮，只是不再尝试下一轮。
const maxStalledRounds = 1

// stallTracker 跟踪「评审分数连续几轮低于历史最好分」。
//
// 提取成独立结构是为了能单测：这套判定嵌在共享印刷画布的轮次循环里，
// 而那层循环要真渲染 + 真调模型，很难为一句 if 搭一整套夹具。
//
// 为什么值得止损：每轮都要重渲染全组 7 格（约 7s）再交给视觉模型评审，
// 实测每轮 20-50s。模型连续给不出更好的解时，剩下的轮数只是把同一套动作
// 重做一遍，成品不会变好（实测 70→65→62、72→45→72 都是这种）。
type stallTracker struct {
	best  int // 历史最好分，-1 表示还没有有效分数
	count int // 连续低于最好分的轮数
}

func newStallTracker() *stallTracker { return &stallTracker{best: -1} }

// observe 记录一轮分数，返回是否应当停止继续微调。
//
// 判定规则：
//   - 首次有效分数 → 建立基准，不停。
//   - 高于基准 → 刷新基准，不停。
//   - 等于基准 → 视为持平，不计入停滞（同分不算变差，下一轮仍有机会）。
//   - 低于基准 → 计入停滞，达到阈值即停。
//
// 分数低于 0 表示评审本身失败（Fallback），不参与比较，
// 那条路径有单独的 break，不该被止损逻辑抢先判定。
func (s *stallTracker) observe(score int) bool {
	if score < 0 {
		return false
	}
	switch {
	case s.best < 0 || score > s.best:
		s.best = score
		s.count = 0
	case score == s.best:
		// 持平：不推进也不清零。推进会把持平误判成变差；
		// 清零会让「掉一次、平一次、再掉」的拖沓轨迹无限延长。
		return false
	default:
		s.count++
	}
	return s.count >= maxStalledRounds
}

// Best 返回历史最好分，用于停止时的提示文案。
func (s *stallTracker) Best() int { return s.best }
