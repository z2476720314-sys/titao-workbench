package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"titao/internal/cfg"
	"titao/internal/imgutil"
	"titao/internal/judge"
	"titao/internal/printcanvas"
	"titao/internal/psx"
	"titao/internal/tuner"
)

type frozenProjection struct {
	photoshop
	canvas                    *psx.PrintCanvas
	diagnostics               string
	patternName, templateName string
}

func (p frozenProjection) Render(req psx.RenderRequest) (string, error) {
	req.Canvas = p.canvas
	req.DiagnosticAlpha = p.diagnostics != ""
	log, err := p.photoshop.Render(req)
	if err != nil {
		return log, err
	}
	if p.diagnostics != "" {
		if _, statErr := os.Stat(req.DiagnosticFlatPath(0)); statErr == nil {
			if _, err := imgutil.SplitCells(req.DiagnosticFlatPath(0), p.diagnostics, p.patternName, p.templateName, req.CellSize, 95); err != nil {
				return log, err
			}
		} else if !os.IsNotExist(statErr) {
			return log, statErr
		}
	}
	return log, nil
}

type patternRound struct {
	canvas      *psx.PrintCanvas
	projections []*Task
	diagnostics []string
	advice      []tuner.Advice
	score       int
	clear, good bool
	// composition 报告本轮构图是否达标：被压到 minCompositionScale 以下的轮次
	// 即使评审给高分也不能胜出，否则又会退回「小贴纸」成品。
	composition bool
	// reviewErr 记录模型评审失败的真实原因，写进任务备注，
	// 避免用户只看到「评审未完成」却无从判断是超时、限流还是格式错误。
	reviewErr string
}

func samePattern(a, b *Task) bool {
	return a.Pattern == b.Pattern && a.PatternName == b.PatternName && a.PreparedSHA256 == b.PreparedSHA256 && a.KeepWhole == b.KeepWhole
}

func patternGroupKey(t *Task) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%t", t.Pattern, t.PatternName, t.PreparedSHA256, t.KeepWhole)
}

// minCompositionScale 是构图下限：缩放低于该值说明在用「整体缩小」换镜头避让，
// 成品会缩成壳面上的小贴纸，即使评审给高分也不得判合格。
const minCompositionScale = 0.55

// adjustmentOf 取建议里的联合调整参数，仅用于日志诊断。
func adjustmentOf(adv tuner.Advice) tuner.Adjustment {
	if adv.Transform == nil {
		return tuner.Adjustment{ScaleRatio: 1}
	}
	return *adv.Transform
}

// pickReviewView 从一轮的全部评审图里挑一张送给模型。
//
// 为什么只送一张：同一图案的全部格子共用同一套摆放参数（scale/offset），
// 图案压在镜头孔上这件事在每一格都会发生，只是露出角度不同。逐格都送审
// 相当于把同一个问题问 7 遍——实测每张 35-47s、prompt 5000+ tokens，
// 是套图耗时的绝对大头。挑一张最完整、最正面的图即可代表全组。
//
// 选法（优先级从高到低）：
//  1. 未被旋转校正的格子（ReviewQuarterTurns[k]==0）：正立才能看到完整壳面，
//     旋转格是侧面或倒置角度，看到的图案被透视裁掉一块，容易误判。
//  2. 不在静态格里的格子：静态格是模板自带的侧面展示图，图案根本没印上去。
//  3. cell 编号最小的：模板里格子通常按主视角在前排列。
//
// 返回的是 allViews/allCells 的下标，不是 cell 编号本身。
func pickReviewView(projection *Task, cells []int) (int, bool) {
	if len(cells) == 0 {
		return 0, false
	}
	static := map[int]bool{}
	for _, s := range projection.StaticCells {
		static[s] = true
	}
	best, bestRank := -1, 1<<30
	for idx, cell := range cells {
		if static[cell] {
			continue
		}
		rank := cell // 基底：编号越小越靠前
		if turns := projection.ReviewQuarterTurns[cell]; turns != 0 {
			// 旋转格排到所有正立格之后
			rank += 1000
		}
		if rank < bestRank {
			best, bestRank = idx, rank
		}
	}
	if best >= 0 {
		return best, true
	}
	// 全部落在静态格（或方向信息缺失）时退回第一个可用的，不要卡死整组。
	return 0, len(cells) > 0
}

func combinePhotoAdvice(replies []tuner.Advice, cells []int) tuner.Advice {
	worst := -1
	good, clear := true, true
	score := 100
	for i, reply := range replies {
		good = good && reply.Good && !reply.Fallback
		clear = clear && reply.CameraClear && !reply.Fallback
		if reply.Fallback {
			score = -1
			if worst < 0 || !replies[worst].Fallback {
				worst = i
			}
			continue
		}
		if reply.Score < score {
			score = reply.Score
		}
		if worst < 0 {
			worst = i
			continue
		}
		previous := replies[worst]
		if !previous.Fallback && ((!reply.Good && previous.Good) || (reply.Good == previous.Good && ((!reply.CameraClear && previous.CameraClear) || (reply.CameraClear == previous.CameraClear && reply.Score < previous.Score)))) {
			worst = i
		}
	}
	if worst < 0 {
		return tuner.Advice{Fallback: true, Err: fmt.Errorf("没有照片评审结果")}
	}
	result := replies[worst]
	result.Good, result.CameraClear, result.Score = good, clear, score
	if !good {
		result.Problem = fmt.Sprintf("照片格%02d：%s", cells[worst]+1, result.Problem)
	}
	return result
}

func validatePatternDestinations(tasks []*Task, outRoot string) error {
	groups := map[string]*Task{}
	templates := map[string]string{}
	for _, task := range tasks {
		directory, err := normalizedCompletionPath(filepath.Join(outRoot, task.PatternName))
		if err != nil {
			return err
		}
		if previous := groups[directory]; previous != nil && !samePattern(previous, task) {
			return fmt.Errorf("不同图案%s映射到同一输出目录，请先区分图案名称", task.PatternName)
		}
		groups[directory] = task
		name := directory + "\x00" + strings.ToLower(task.TemplateName)
		template, err := normalizedCompletionPath(task.Template)
		if err != nil {
			return err
		}
		if previous, ok := templates[name]; ok && previous != template {
			return fmt.Errorf("同名模板%s会相互覆盖，请先区分模板名称", task.TemplateName)
		}
		templates[name] = template
	}
	return nil
}

func (e *Engine) canvasArtifactDir(outRoot, patternName string) (string, error) {
	directory, err := normalizedCompletionPath(filepath.Join(outRoot, patternName))
	if err != nil {
		return "", err
	}
	identity := sha256.Sum256([]byte(directory))
	base := filepath.Join(filepath.Dir(outRoot), ".titao-print-canvases")
	if e.store != nil {
		base = filepath.Join(filepath.Dir(e.store.Path()), "print-canvases")
	}
	return filepath.Join(base, hex.EncodeToString(identity[:])), nil
}

func inputSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func freezePatternInput(source, destination string) (string, error) {
	if err := copyFile(source, destination); err != nil {
		return "", err
	}
	snapshot, err := inputSHA(destination)
	if err != nil {
		return "", err
	}
	current, err := inputSHA(source)
	if err != nil {
		return "", err
	}
	if current != snapshot {
		return "", fmt.Errorf("输入在冻结时发生变化: %s", filepath.Base(source))
	}
	return snapshot, nil
}

func (e *Engine) processPattern(ctx context.Context, opt Options, def cfg.Config, jc judge.Config, tasks []*Task, outRoot string, seq, total int) {
	if len(tasks) == 0 {
		return
	}
	started := time.Now()
	fail := func(err error) {
		for _, task := range tasks {
			e.failTask(task, err)
		}
	}
	wait := func() bool {
		if err := e.gate(ctx); err != nil {
			e.cancelRest(tasks)
			return false
		}
		return true
	}
	jc.BeforeRequest = func() error { return e.gate(ctx) }
	names := map[string]bool{}
	for _, task := range tasks {
		if !samePattern(tasks[0], task) {
			fail(fmt.Errorf("不同图案不能共用一次画布微调"))
			return
		}
		name := strings.ToLower(task.TemplateName)
		if names[name] {
			fail(fmt.Errorf("同一图案存在同名模板%s，输出文件会相互覆盖", task.TemplateName))
			return
		}
		names[name] = true
	}
	if err := os.MkdirAll(filepath.Dir(outRoot), 0700); err != nil {
		fail(err)
		return
	}
	work, err := os.MkdirTemp(filepath.Dir(outRoot), "titao-pattern-")
	if err != nil {
		fail(err)
		return
	}
	defer os.RemoveAll(work)
	inputs := map[string]string{}
	protected := map[string]bool{}
	allTasks := append(e.Tasks(), tasks...)
	for _, task := range allTasks {
		for _, path := range []string{task.Pattern, task.OriginalPattern, task.Template} {
			if path != "" {
				protected[path] = true
			}
		}
	}
	sourceSnapshot := filepath.Join(work, "original"+filepath.Ext(tasks[0].Pattern))
	sourceSHA, err := freezePatternInput(tasks[0].Pattern, sourceSnapshot)
	if err != nil {
		fail(err)
		return
	}
	inputs[tasks[0].Pattern] = sourceSHA
	templates := make([]string, len(tasks))
	for i, task := range tasks {
		if err := e.validatePreparedTask(task); err != nil {
			fail(err)
			return
		}
		if task.OriginalPattern != "" {
			hash, err := inputSHA(task.OriginalPattern)
			if err != nil {
				fail(err)
				return
			}
			inputs[task.OriginalPattern] = hash
		}
		templates[i] = filepath.Join(work, fmt.Sprintf("template-%d%s", i, filepath.Ext(task.Template)))
		hash, err := freezePatternInput(task.Template, templates[i])
		if err != nil {
			fail(err)
			return
		}
		inputs[task.Template] = hash
	}
	if tasks[0].KeepWhole {
		opt.NoCrop = true
	}
	pattern := sourceSnapshot
	autoLayout := opt.Auto && fitIsContainLike(opt.Fit)
	// Detect original cut edges before Plan removes only the outer zero-alpha pixels.
	pr, err := imgutil.Normalize(sourceSnapshot, filepath.Join(work, "analysis.png"), false)
	if err != nil {
		fail(err)
		return
	}
	if tasks[0].KeepWhole {
		pr.AnchorX, pr.AnchorY = 0, 0
		pr.BottomContent = false
		pr.TopCut = false
	}
	fit := opt.Fit
	if opt.NoCrop && fit != "" && fit != psx.FitAuto && fit != psx.FitNone {
		fit = psx.FitContain
	} else if autoLayout && fit != "" && fit != psx.FitAuto && fit != psx.FitNone {
		fit = psx.FitCover
	}
	// 贴底入口：`AlignBottom` 只在非 NoCrop 的手动摆位里被读取
	// （printcanvas.manualPlacement）。NoCrop 走 FitArtwork，那是独立的几何
	// 求解器，根本不看 AlignBottom，所以从 v0.3.11 起 NoCrop 模式贴底一直失效。
	// NoCrop 下的贴底只能由 AnchorY=1 表达：让下边贴住印刷域底边。
	alignBottom := pr.BottomContent && fitIsContainLike(fit)
	if alignBottom && opt.NoCrop {
		// 已有更精确的锚点（2=对边铺满）时不要降级成单边贴底。
		if pr.AnchorY != 2 {
			pr.AnchorY = 1
		}
	}
	request := psx.RenderRequest{Pattern: pattern, Fit: fit, NoCrop: opt.NoCrop, OffsetX: opt.OffsetX, OffsetY: opt.OffsetY, ContentBounds: pr.ContentBounds, AnchorX: pr.AnchorX, AnchorY: pr.AnchorY, IgnoreCutAnchors: tasks[0].KeepWhole, AlignBottom: alignBottom && !opt.NoCrop}
	scale := 1.0
	if !opt.Auto && len(opt.Scales) > 0 {
		scale = opt.Scales[0]
	}
	placement := judge.Placement{Fit: fit, NoCrop: opt.NoCrop, KeepWhole: tasks[0].KeepWhole, AnchorX: pr.AnchorX, AnchorY: pr.AnchorY}
	if opt.Auto {
		placement.SourceDataURL, placement.SourceAlphaGuide, err = imgutil.ReviewReferenceDataURL(pattern, def.ImageMaxSide)
		if err != nil {
			fail(err)
			return
		}
	}
	reviewOn, rounds := effectiveReviewSettings(opt, def)
	var best, current *patternRound
	var history []tuner.Round
	stopNote := ""
	feedbackUsed := false
	var bottomPending *psx.PrintCanvas
	bottomTried := false
	// 分数掉头就止损：模型每轮都在猜，追不上历史最好分说明方向走不通，
	// 继续跑只会把渲染和评审再重复一遍（每轮约 40-50s）。
	stalled := newStallTracker()
	for round := 1; round <= rounds; round++ {
		if !wait() {
			return
		}
		roundDir := filepath.Join(work, fmt.Sprintf("round-%d", round))
		if err := os.MkdirAll(roundDir, 0700); err != nil {
			fail(err)
			return
		}
		request.PreferOffsets = round > 1
		var canvas *psx.PrintCanvas
		bottomRound := bottomPending != nil
		if bottomRound {
			canvas = bottomPending
			bottomPending, bottomTried = nil, true
		} else {
			canvas, err = printcanvas.Plan(pattern, filepath.Join(roundDir, "artwork_plan.json"), request, scale)
		}
		if err != nil {
			if best != nil && errors.Is(err, printcanvas.ErrEdgeStretchConflict) {
				stopNote = fmt.Sprintf("本轮微调与直边贴合冲突：%v。已保留此前套图，请人工复核", err)
				e.logf("warn", "  第%d轮 %s", round, stopNote)
				break
			}
			fail(fmt.Errorf("生成统一原图版位: %w", err))
			return
		}
		reused := current != nil && canvas.SHA256 == current.canvas.SHA256
		if reused && (feedbackUsed || round == rounds) {
			stopNote = "统一版位已到约束，调整未生效，请人工复核"
			break
		}
		candidate := &patternRound{canvas: canvas, score: 100, clear: true, good: true, composition: true}
		if reused {
			feedbackUsed = true
			candidate.projections = current.projections
			candidate.diagnostics = current.diagnostics
			e.logf("warn", "  公共版位未改变，反馈实际参数后仅复核一次")
		} else {
			for i, task := range tasks {
				e.updateTask(task, func(t *Task) {
					t.Phase = PhaseRender
					t.Progress = 20
					t.Message = fmt.Sprintf("公共版位第%d轮投影", round)
				})
				local := *task
				local.Pattern, local.Template = pattern, templates[i]
				local.OriginalPattern, local.OriginalSHA256, local.PreparedSHA256 = "", "", ""
				local.Files = nil
				local.TuneHistory = nil
				local.CanvasPath = ""
				local.CanvasSHA256 = ""
				local.Error = ""
				local.JudgeNote = ""
				local.Phase = PhasePending
				projectionOpt := opt
				projectionOpt.Auto = false
				projectionOpt.TrimAlpha = false
				projectionOpt.Scales = []float64{canvas.Placement.Scale}
				projectionOpt.OffsetX, projectionOpt.OffsetY = canvas.Placement.OffsetX, canvas.Placement.OffsetY
				projectionOut := filepath.Join(roundDir, fmt.Sprintf("projection-%d", i), "exports")
				if err := os.MkdirAll(projectionOut, 0700); err != nil {
					fail(err)
					return
				}
				diagnosticDir := ""
				if reviewOn {
					diagnosticDir = filepath.Join(roundDir, fmt.Sprintf("projection-%d", i), "diagnostics")
				}
				child := New(frozenProjection{photoshop: e.ps, canvas: canvas, diagnostics: diagnosticDir, patternName: task.PatternName, templateName: task.TemplateName}, e.store, func(ev Event) {
					if ev.Type == "log" && ev.Level != "ok" {
						e.emit(ev)
					}
				})
				if !wait() {
					return
				}
				child.processOne(ctx, projectionOpt, def, jc, &local, projectionOut, seq+i, total)
				if !wait() {
					return
				}
				if local.Phase != PhaseDone && len(local.Files) == 0 {
					fail(fmt.Errorf("%s 投影失败: %s", task.TemplateName, local.Error))
					return
				}
				if local.CanvasSHA256 != canvas.SHA256 {
					fail(fmt.Errorf("%s 未使用同一版原图版位", task.TemplateName))
					return
				}
				candidate.projections = append(candidate.projections, &local)
				candidate.diagnostics = append(candidate.diagnostics, diagnosticDir)
			}
		}
		candidate.advice = make([]tuner.Advice, len(tasks))
		if canvas.Placement.AnchorImpossible {
			candidate.good, candidate.clear, candidate.score = false, false, 0
			stopNote = "统一原图版位无法同时满足内容完整和直边贴合，请人工复核"
		} else if reviewOn {
			// 同一图案的多个模板共用一套原图版位与摆放参数，评审一次即可：
			// 微调值是全局的，第二个模板再问一遍只是把同一个问题再问一次，
			// 每多问一次就多一次视觉请求（单次 35-47s）。因此只用第一个模板送审，
			// 其余模板直接复用它的结论。
			const reviewTemplate = 0
			allViews := make([][][]byte, len(candidate.projections))
			allCells := make([][]int, len(candidate.projections))
			diagnosticViews := make([]bool, len(candidate.projections))
			for i, projection := range candidate.projections {
				if candidate.diagnostics[i] != "" {
					if _, err := os.Stat(filepath.Join(candidate.diagnostics[i], filepath.Base(projection.Files[0]))); err == nil {
						diagnosticViews[i] = true
					}
				}
				for cell, path := range projection.Files {
					static := false
					for _, s := range projection.StaticCells {
						if cell == s {
							static = true
						}
					}
					if static {
						continue
					}
					if diagnosticViews[i] {
						path = filepath.Join(candidate.diagnostics[i], filepath.Base(path))
					}
					b, err := os.ReadFile(path)
					if err != nil {
						fail(err)
						return
					}
					if turns := projection.ReviewQuarterTurns[cell]; turns != 0 {
						b, err = imgutil.ReviewUprightJPEG(b, turns)
						if err != nil {
							fail(err)
							return
						}
						e.logf("dim", "  %s照片格%02d评审方向校正%d度（正式图不变）", projection.TemplateName, cell+1, turns*90)
					}
					allViews[i] = append(allViews[i], b)
					allCells[i] = append(allCells[i], cell)
				}
				if len(allViews[i]) == 0 {
					fail(fmt.Errorf("模板没有可评审的印刷照片"))
					return
				}
			}
			var wg sync.WaitGroup
			sem := make(chan struct{}, 4)
			replies := make([][]tuner.Advice, len(tasks))
			for i, projection := range candidate.projections[:reviewTemplate+1] {
				// 只挑一张能完整看到壳面正面的图送评审，不再逐格都发。
				// 理由：7 格共用同一套摆放参数，任何一格的遮挡都等价于全组的问题，
				// 而每多发一张就是一次完整的视觉请求（实测单次 35-47s、prompt 5000+ tokens）。
				// 选法：优先正立（未旋转）且不含静态侧视图的格；都旋转时退回首格。
				pick, ok := pickReviewView(projection, allCells[i])
				if !ok {
					fail(fmt.Errorf("模板没有可评审的印刷照片"))
					return
				}
				views := [][]byte{allViews[i][pick]}
				cells := []int{allCells[i][pick]}
				if len(cells) > 0 && len(allViews[i]) > 1 {
					e.logf("dim", "  %s评审仅使用照片格%02d（全组共用同一摆放，其余 %d 格不再单独送审）",
						projection.TemplateName, cells[0]+1, len(allViews[i])-1)
				}
				allViews[i], allCells[i] = views, cells
				replies[i] = make([]tuner.Advice, 1)
				check := placement
				check.DiagnosticViews = diagnosticViews[i]
				check.AppliedSummary = "dx向公共原图版位右，dy向画布下，不是原照片的屏幕上下左右。"
				if projection.ReviewQuarterTurns != nil {
					check.AppliedSummary += "评审照片已按模板真实变换校正到原图版位上方向。"
				}
				for cell := range projection.Files {
					static := false
					for _, s := range projection.StaticCells {
						if cell == s {
							static = true
						}
					}
					if !static {
						p := canvas.Placement
						p.Cell = cell
						check.ViewPlacements = append(check.ViewPlacements, p)
					}
				}
				check.Scale, check.OffsetX, check.OffsetY = canvas.Placement.Scale, canvas.Placement.OffsetX, canvas.Placement.OffsetY
				check.AppliedSummary += fmt.Sprintf("同一图案全部模板使用公共原图版位，plan_sha=%s；当前模板=%s。程序验证公共版位直边已贴合=%t。手机壳边框、透明框、阴影不属于待评项，不得因这些给出不合格或放大建议；但图案自身有笔直切线（竖直或水平的直线切断）却没贴住对应壳边时，贴边属于待评问题，必须指出。当前纵横相对比例=%.6g；允许范围是%.2f至%.2f，不能靠明显压扁或拉长主体来避镜头。本轮已执行遮挡后下移贴底=%t；该动作只改变纵向位置，不改变大小或比例，仍遮挡时如实报告并停止调整。数值只针对公共印刷域，任何调整使所有模板同步重投影，禁止单独改变一张的位置。", canvas.SHA256, projection.TemplateName, !canvas.Placement.AnchorUnmet, canvas.Placement.VerticalStretch(), psx.MinVerticalStretch, psx.MaxVerticalStretch, bottomTried)
				if reused {
					check.AppliedSummary += "上次请求被印刷域或直边约束夹回，画布未改变；请联合调整缩小、位移或左右拉伸，不能重复无效建议。"
				}
				for j, view := range views {
					check.ViewPlacements = nil
					oneCheck := check
					oneCheck.AppliedSummary += fmt.Sprintf("\n本次评审代表全组摆放（全组共用同一套参数），图中照片格%02d只是取景角度不同。", allCells[i][j]+1)
					sem <- struct{}{}
					// 等到并发名额后再查暂停，避免等待期间暂停仍续发请求。
					if !wait() {
						<-sem
						wg.Wait()
						return
					}
					wg.Add(1)
					go func(i, j int, view []byte, check judge.Placement) {
						defer wg.Done()
						defer func() { <-sem }()
						replies[i][j] = tuner.Critique(tuner.Config(jc), view, check)
					}(i, j, view, oneCheck)
				}
			}
			wg.Wait()
			if !wait() {
				return
			}
			for i := range replies {
				candidate.advice[i] = combinePhotoAdvice(replies[i], allCells[i])
			}
			// 其余模板复用第一个模板的评审结论：画布与参数是共享的，
			// 分别评审只会得到互相冲突的建议，还多花数倍时间。
			for i := reviewTemplate + 1; i < len(candidate.advice); i++ {
				candidate.advice[i] = candidate.advice[reviewTemplate]
				e.logf("dim", "  %s 复用 %s 的评审结论（同一图案共用版位与参数）",
					tasks[i].TemplateName, tasks[reviewTemplate].TemplateName)
			}
			for i, adv := range candidate.advice {
				if adv.Fallback {
					candidate.good, candidate.clear, candidate.score = false, false, -1
					reason := "未知原因"
					if adv.Err != nil {
						reason = adv.Err.Error()
					}
					candidate.reviewErr = reason
					e.logf("warn", "  %s 评审未完成：%s", tasks[i].TemplateName, reason)
					continue
				}
				if !adv.Good || canvas.Placement.AnchorUnmet {
					candidate.good = false
				}
				if !adv.CameraClear {
					candidate.clear = false
				}
				if adv.Score < candidate.score {
					candidate.score = adv.Score
				}
				e.logf("info", "  第%d轮 %s：%d分，合格=%t，已避镜头=%t，%s", round, tasks[i].TemplateName, adv.Score, adv.Good, adv.CameraClear, adv.Problem)
				if adv.Usage != nil && adv.Usage.CompletionTokens != nil {
					e.logf("dim", "  %s最低分照片响应输出tokens %d", tasks[i].TemplateName, *adv.Usage.CompletionTokens)
				}
			}
		} else if canvas.Placement.AnchorUnmet {
			candidate.good, candidate.clear = false, false
			stopNote = "统一版位直边未贴合，请人工复核"
		}
		if reviewOn && canvas.Placement.Scale < minCompositionScale {
			candidate.composition = false
			if candidate.good {
				candidate.good = false
				stopNote = fmt.Sprintf("图案被压到%.0f%%，低于%.0f%%构图下限，不接受整体缩小避让", canvas.Placement.Scale*100, minCompositionScale*100)
				e.logf("warn", "  第%d轮构图过小：scale=%.3f < %.2f，判不合格", round, canvas.Placement.Scale, minCompositionScale)
			}
		}
		current = candidate
		if best == nil || (candidate.good && !best.good) || (candidate.good == best.good && ((candidate.composition && !best.composition) || (candidate.composition == best.composition && (candidate.score > best.score || (candidate.score == best.score && candidate.clear && !best.clear))))) {
			best = candidate
		}
		if bottomRound {
			// 用户选择贴底结果，即使分数变低也不退回悬空版位。
			best = candidate
			stalled = newStallTracker()
		}
		problem := stopNote
		chosen := -1
		for i, adv := range candidate.advice {
			if adv.Fallback {
				problem = "模型评审未完成，请人工复核"
				chosen = i
				break
			}
			if !adv.Good && (chosen < 0 || (!adv.CameraClear && candidate.advice[chosen].CameraClear) || (adv.CameraClear == candidate.advice[chosen].CameraClear && adv.Score < candidate.advice[chosen].Score)) {
				chosen = i
				problem = tasks[i].TemplateName + "：" + adv.Problem
				if stopNote != "" && !strings.Contains(problem, stopNote) {
					problem += "；" + stopNote
				}
			}
		}
		if candidate.good && reviewOn {
			problem = "所有模板共用版位并通过本轮检查"
		} else if candidate.good {
			problem = "统一版位投影完成，未调用自动评审"
		}
		recordedScore := candidate.score
		if !reviewOn || recordedScore < 0 {
			recordedScore = 0
		}
		history = append(history, tuner.Round{Index: round, Scale: canvas.Placement.Scale, OffsetX: canvas.Placement.OffsetX, OffsetY: canvas.Placement.OffsetY, Score: recordedScore, Problem: problem, Good: candidate.good && reviewOn, CameraClear: candidate.clear && reviewOn, Applied: []psx.AppliedPlacement{canvas.Placement}})
		for _, task := range tasks {
			e.updateTask(task, func(t *Task) {
				t.Phase = PhaseTune
				t.Progress = 72
				t.TuneHistory = append([]tuner.Round(nil), history...)
				t.Message = fmt.Sprintf("公共版位第%d轮检查", round)
			})
		}
		if reviewOn && !candidate.clear && candidate.score >= 0 && !canvas.Placement.AnchorImpossible {
			if bottomTried {
				best = candidate
				stopNote = "贴底后仍有遮挡，按用户规则停止继续微调；保留遮挡记录，请人工复核"
				break
			}
			if request.AnchorY == -1 || request.AnchorY == 2 || (pr.TopCut && canvas.Transform[5] <= .002) {
				stopNote = "下移贴底与顶部直切边约束冲突，保持图案大小与比例，请人工复核"
				break
			}
			if !canvas.Verified {
				stopNote = "当前版位超出印刷域，无法安全下移，请人工复核"
				break
			}
			bottom, err := printcanvas.MoveToBottom(canvas, filepath.Join(roundDir, "bottom", "artwork_plan.json"))
			if err != nil {
				fail(fmt.Errorf("下移贴底: %w", err))
				return
			}
			if bottom == canvas {
				best = candidate
				stopNote = "已无向下移动空间，仍有遮挡，请人工复核"
				break
			}
			if round < rounds {
				bottomPending = bottom
				e.logf("info", "  上方内容被镜头遮挡，下一轮仅整体向下平移贴底，大小、比例和横向位置不变")
				continue
			}
			stopNote = "评审轮数已用完，尚未尝试下移贴底，请人工复核"
		}
		if candidate.good || !reviewOn || canvas.Placement.AnchorImpossible || round == rounds {
			break
		}
		// 越调越差就止损。比较对象是「历史最好分」而不是上一轮：
		// 70→65→67 这种小幅回升但始终不如首轮的情况，也能及时停。
		if stalled.observe(candidate.score) {
			stopNote = fmt.Sprintf("本轮 %d 分未超过此前最好 %d 分，提前止损，请人工复核", candidate.score, stalled.Best())
			e.logf("warn", "  第%d轮 %d 分未超过此前最好 %d 分，停止继续微调（省下剩余 %d 轮）",
				round, candidate.score, stalled.Best(), rounds-round)
			break
		}
		if chosen < 0 {
			stopNote = "直边或内容约束未满足，请人工复核"
			break
		}
		adv := candidate.advice[chosen]
		if adv.Fallback {
			stopNote = "模型评审未完成，请人工复核"
			break
		}
		next, nx, ny, nsy, iterate := tuner.Plan(adv, canvas.Placement.Scale, canvas.Placement.OffsetX, canvas.Placement.OffsetY, canvas.Placement.VerticalStretch())
		if !iterate {
			stopNote = "复核后仍无可执行的全组调整，请人工复核"
			e.logf("warn", "  第%d轮模型建议无法执行：dx=%v dy=%v scale_ratio=%v stretch_y=%v（当前 scale=%.3f）",
				round, adjustmentOf(adv).DX, adjustmentOf(adv).DY, adjustmentOf(adv).ScaleRatio, adjustmentOf(adv).StretchY, canvas.Placement.Scale)
			break
		}
		e.logf("info", "  第%d轮调整为 scale=%.3f offset=(%.3f,%.3f) 纵向比例=%.4f（上轮 scale=%.3f offset=(%.3f,%.3f) 纵向比例=%.4f）",
			round+1, next, nx, ny, nsy, canvas.Placement.Scale, canvas.Placement.OffsetX, canvas.Placement.OffsetY, canvas.Placement.VerticalStretch())
		scale, request.OffsetX, request.OffsetY = next, nx, ny
		request.StretchY = nsy
	}
	if stopNote != "" && len(history) > 0 && !history[len(history)-1].Good && !strings.Contains(history[len(history)-1].Problem, stopNote) {
		history[len(history)-1].Problem += "；" + stopNote
	}
	if !wait() {
		return
	}
	if best == nil {
		fail(fmt.Errorf("没有可用的统一版位投影"))
		return
	}
	if err := printcanvas.Validate(best.canvas); err != nil {
		fail(err)
		return
	}
	for source, expected := range inputs {
		current, err := inputSHA(source)
		if err != nil || current != expected {
			fail(fmt.Errorf("套图期间输入发生变化，未发布: %s", filepath.Base(source)))
			return
		}
	}
	for _, task := range tasks {
		if err := e.validatePreparedTask(task); err != nil {
			fail(fmt.Errorf("发布前原图校验失败: %w", err))
			return
		}
		if task.CompletionKey != "" {
			key, err := completionKey(opt, def, task)
			if err != nil || key != task.CompletionKey {
				fail(fmt.Errorf("套图期间输入或摆放配置变化，未发布"))
				return
			}
		}
	}
	relativeDir := tasks[0].PatternName
	if !best.good {
		// 待复核产物单独成目录，避免和合格品混在同一个输出根下被误用。
		relativeDir = filepath.Join("_待复核", relativeDir)
	}
	finalDir := filepath.Join(outRoot, relativeDir)
	if err := os.MkdirAll(finalDir, 0755); err != nil {
		fail(err)
		return
	}
	artifactDir, err := e.canvasArtifactDir(outRoot, relativeDir)
	if err != nil {
		fail(err)
		return
	}
	canvasPath := filepath.Join(artifactDir, "artwork_plan.json")
	publication := map[string]string{canvasPath: best.canvas.Path, filepath.Join(artifactDir, best.canvas.SourceFile): best.canvas.SourcePath()}
	var finalFiles [][]string
	for _, projection := range best.projections {
		var files []string
		for _, path := range projection.Files {
			dst := filepath.Join(finalDir, filepath.Base(path))
			if _, duplicate := publication[dst]; duplicate {
				fail(fmt.Errorf("模板输出文件重名"))
				return
			}
			publication[dst] = path
			files = append(files, dst)
		}
		finalFiles = append(finalFiles, files)
	}
	for destination := range publication {
		dstAbs, _ := filepath.Abs(destination)
		for source := range protected {
			srcAbs, _ := filepath.Abs(source)
			same := strings.EqualFold(dstAbs, srcAbs)
			dstInfo, de := os.Stat(destination)
			srcInfo, se := os.Stat(source)
			if de == nil && se == nil && os.SameFile(dstInfo, srcInfo) {
				same = true
			}
			if same {
				fail(fmt.Errorf("输出不能覆盖原始输入: %s", filepath.Base(source)))
				return
			}
		}
	}
	if !wait() {
		return
	}
	if err := publishPatternFiles(publication); err != nil {
		fail(err)
		return
	}
	note := fmt.Sprintf("公共版位检查%d分", best.score)
	if best.score < 0 {
		note = "公共版位评审未完成"
		if best.reviewErr != "" {
			note += "（" + best.reviewErr + "）"
		}
	}
	if !reviewOn {
		note = "共用统一原图版位，未调用自动评审"
	}
	if !best.good {
		note += "；" + stopNote
		if stopNote == "" {
			note += "未通过全部模板评审，请人工复核"
		}
	}
	for i, task := range tasks {
		e.mu.Lock()
		if best.good {
			e.done++
		} else {
			e.failed++
		}
		e.mu.Unlock()
		e.updateTask(task, func(t *Task) {
			t.Phase = PhaseDone
			t.Progress = 100
			t.Files = finalFiles[i]
			t.OutputDir = finalDir
			t.CanvasPath = canvasPath
			t.CanvasSHA256 = best.canvas.SHA256
			t.StaticCells = append([]int(nil), best.projections[i].StaticCells...)
			t.ReviewQuarterTurns = best.projections[i].ReviewQuarterTurns
			t.ChosenScale = best.canvas.Placement.Scale
			t.TuneRounds = len(history)
			t.TuneHistory = append([]tuner.Round(nil), history...)
			t.ElapsedMS = time.Since(started).Milliseconds()
			t.JudgeNote = note
			t.Message = fmt.Sprintf("生成%d张，共用同一原图版位", len(t.Files))
			t.Error = ""
			if !best.good {
				t.Phase = PhaseFailed
				t.Error = note
				t.Message += "（请人工复核）"
			}
		})
	}
}
