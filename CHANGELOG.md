# 更新日志

本文件记录每次发布的变更。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

版本号的唯一来源是 `main.go` 里的 `var Version`；发布时改它、同步本文件、打同名 tag。

## v0.2.0 — 2026-10-24

从「只能套图」扩到「生图 → 预检 → 套图 → 评审迭代」的完整流水线，并把 PS 交互从
「按 27.x 硬编码」改成「按版本自适应」。

### 新增

- **PS 版本自适应**（`internal/psx/capability.go`）：新增 `Caps` 能力矩阵与 `For(version)`。
  按大版本号判定 `DoJavaScript` / `NoDialogs` / `EditSmartObject` / `ReplaceContents` 四项能力，
  27.x 标记为实测通过，更低版本给出明确升级提示，未知或更高版本保守放行并注明「未实测」。
  套图开始前先做一次能力检查，缺能力直接报错而不是跑到一半卡死。
- **自动更新后端**（`internal/updater`）：查询 GitHub Releases 最新版 → 语义化版本比较 →
  下载 → SHA-256 校验 → 生成延迟替换脚本。版本比较覆盖预发布标识（`1.0.0-beta < 1.0.0`）、
  构建元数据（`+build.5` 不参与比较）、以及脏字符串（按 0 处理，永不 panic）。
- **生图流水线**（`internal/genimg`、`internal/genjobs`）：接入 OpenAI 兼容的图生图接口，
  支持参考图 → 提示词 → 生成 → 一致性校验 → 落盘，可暂停/继续/停止。
- **套图前预检**（`internal/precheck`）：用视觉模型判断图案是否适合直接套图，
  识别水印图、镜头框/边框图、本身是手机壳实拍的图，避免白跑一轮。
- **成品二次评审**（`internal/tuner`）：对已套好的成品做视觉评审，决定要不要再迭代一轮、
  以及下一轮往哪个方向调参数。
- **多模板批量**：一次可选多个图案 × 多个模板，成品统一落在 `<输出目录>/<图案名>/` 下，
  靠文件名区分模板。

### 修复

- 智能对象替换不再走 `placedLayerReplaceContents`。实测该路径会把原有透视变换
  从 271×601 放大成 1331×3211，成品完全错位；改用 `placedLayerEditContents` 进内部文档替换。
- COM 会话强制 `app.DisplayDialogs = 3`（PsDisplayNoDialogs）。此前 PS 一旦弹出模态框，
  COM 调用会永久挂起（实测卡满 300 秒超时）。
- 读图层属性改用 `executeActionGet(ref)`。`executeAction(charIDToTypeID("getd"), d, ...)`
  稳定报错误 8800。
- 图案统一经 Go 重存 PNG。原始 PNG 带 300dpi 元数据时 PS 会按物理尺寸换算，图案缩小 4 倍。

### 工程

- 仓库初始化：`.gitignore`、`LICENSE`（MIT）、本更新日志、`.github/workflows/release.yml`。
- 文档：`docs/发布说明.md`（发布流程与验收清单）、`docs/多模板说明.md`（多模板与输出结构）。
- 单元测试：`internal/psx` 增加 8 个能力矩阵用例，`internal/updater` 覆盖版本比较、
  GitHub API 各种失败分支、SHA-256 校验、下载与替换脚本生成。

### 已知限制

- 需要 Photoshop 已安装且可被 COM 调用；PS 未安装时套图功能整体不可用。
- 视觉模型接口偶发超时，此时回退到候选组里中间偏小的一档（宁可略小也不裁坏），
  日志会标「视觉模型判断失败」。
- 目录选择只展开一层，不递归。
- 自动更新尚未接入前端 UI，当前只有后端能力（`updater` 包）。

## v0.1.0 — 2026-10-23

首个可用版本。

### 新增

- 把抠好的透明图案套进 PS 分层 TIF 模板的智能对象，按模板自带分割线（默认每 800px 一格）
  纵向切出每张手机壳 JPG。
- 视觉模型自动判断图案摆放大小：按候选倍数各渲染一遍，交模型挑最合适的一档。
- 模板与图案都只读，程序永远复制到临时目录再动。
- 单文件 exe，不依赖 Python 或环境变量。
- 侧栏五个页签：套图、任务队列、成品、设置、关于。
