// 浏览器审查专用 mock 后端。
//
// 目的：不启动 Wails 桌面窗口、不抢焦点，直接在浏览器端口上把前端每个页面、
// 每个状态（空/进行中/完成/失败/跳过/暂停/待恢复/有新版本）都点一遍。
// 生产构建不会包含本文件（vite 只以 index.html 为入口）。
import { mount } from 'svelte'
import './style.css'
import App from './App.svelte'

const FRESH = new URLSearchParams(location.search).get('fresh') === '1'
const PATTERN = 'demo/patterns/pattern-a.png'
const PATTERN2 = 'demo/patterns/pattern-b.png'
const TPL = 'demo/templates/template-a.tif'
const TPL2 = 'demo/templates/template-b.tif'
const OUT = 'demo/output'
const OUTDIR = `${OUT}/pattern-a`

// 路径只是审查数据标识；预览完全内联，不读取本机文件。
const SAMPLE_JPG = Array.from({ length: 6 }, (_, i) => `${OUTDIR}/sample-${i + 1}.jpg`)
const PREVIEW_URL = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(
  '<svg xmlns="http://www.w3.org/2000/svg" width="320" height="320" viewBox="0 0 320 320">' +
  '<rect width="320" height="320" fill="#f2eee8"/>' +
  '<rect x="86" y="34" width="148" height="252" rx="24" fill="#546d62"/>' +
  '<circle cx="168" cy="174" r="58" fill="#e8c77c"/>' +
  '<path d="M122 220l44-93 42 93z" fill="#efe7d5"/>' +
  '<rect x="98" y="46" width="50" height="58" rx="12" fill="#263d36"/>' +
  '<circle cx="112" cy="61" r="8" fill="#bcc9c2"/><circle cx="134" cy="87" r="8" fill="#bcc9c2"/>' +
  '</svg>',
)

const mockKey = FRESH ? '' : 'dummy'

const CONFIG = {
  base_url: 'https://example.invalid/v1',
  api_key: mockKey,
  model: 'demo-vision-model',
  max_tokens: 4000,
  timeout_seconds: 180,
  temperature: 0.1,
  image_max_side: 640,
  thinking: false,
  candidates: [0.85, 0.95, 1.05, 1.15, 1.3],
  output_dir: FRESH ? '' : OUT,
  template_dir: FRESH ? '' : 'demo/templates',
  pattern_dir: FRESH ? '' : 'demo/patterns',
  group: '插画',
  fit: 'auto',
  cell_size: 800,
  jpg_quality: 95,
  trim_alpha: true,
  no_crop: true,
  offset_x: 0,
  offset_y: 0,
  max_rounds: 2,
  auto_tune_on: true,
  precheck_on: true,
  precheck_skip: true,
  gen_base_url: 'https://example.invalid/v1',
  gen_api_key: mockKey,
  gen_model: 'demo-image-model',
  gen_size: '1024x1024',
  gen_thinking: false,
  gen_timeout: 300,
  gen_count: 2,
  gen_auto_run: false,
  ps_version: '27.7.0',
  update_check: true,
}

const mkTask = (o: any) => ({
  id: 1, pattern: PATTERN, patternName: 'pattern-a',
  template: TPL, templateName: 'template-a',
  phase: 'pending', progress: 0, message: '', outputDir: '', files: [],
  error: '', chosenScale: 0, judgeNote: '', tuneRounds: 0, elapsedMs: 0, ...o,
})

// 一轮完整任务的各阶段快照，用于「任务队列」页覆盖所有状态徽章
const SAMPLE_TASKS = [
  mkTask({ id: 1, phase: 'done', progress: 100, message: '生成 6 张', chosenScale: 1.38, tuneRounds: 2, judgeNote: '图案饱满且未压摄像头，未裁切', elapsedMs: 32860, outputDir: OUTDIR, files: Array.from({ length: 6 }, (_, i) => `${OUTDIR}\\pattern-a_template-a_0${i + 1}.jpg`) }),
  mkTask({ id: 2, template: TPL2, templateName: 'template-b', phase: 'done', progress: 100, message: '生成 7 张', chosenScale: 1.15, tuneRounds: 1, judgeNote: '饱满，未压摄像头', elapsedMs: 34700, outputDir: OUTDIR, files: Array.from({ length: 7 }, (_, i) => `${OUTDIR}\\x_0${i + 1}.jpg`) }),
  mkTask({ id: 3, pattern: PATTERN2, patternName: 'pattern-b', phase: 'tune', progress: 72, message: '第 1 轮评审微调', chosenScale: 1.05, tuneRounds: 1 }),
  mkTask({ id: 4, pattern: PATTERN2, patternName: 'pattern-b', template: TPL2, templateName: 'template-b', phase: 'judge', progress: 60, message: '视觉模型判断摆放' }),
  mkTask({ id: 5, phase: 'render', progress: 20, message: 'Photoshop 渲染候选' }),
  mkTask({ id: 6, phase: 'precheck', progress: 5, message: '视觉预检' }),
  mkTask({ id: 7, phase: 'pattern', progress: 5, message: '规范化图案' }),
  mkTask({ id: 8, phase: 'pending', progress: 0, message: '' }),
  mkTask({ id: 9, phase: 'skipped', progress: 100, message: '跳过：带 shuiyin 文字水印', error: '', chosenScale: 0 }),
  mkTask({ id: 10, phase: 'failed', progress: 100, message: '', error: 'Photoshop 渲染不完整：2 个智能对象未被替换' }),
  mkTask({ id: 11, phase: 'canceled', progress: 0, message: '已停止，未执行' }),
]

const GEN_TASKS = [
  {
    id: 1, refPath: OUTDIR + '\\pattern-a_template-a_01.jpg', refName: '…_01.jpg',
    mode: 'handheld', count: 2, phase: 'done', progress: 100, message: '生成 2 张',
    files: [
      { path: SAMPLE_JPG[2], mode: 'handheld', index: 1, same: true, diff: '', reason: '' },
      { path: SAMPLE_JPG[3], mode: 'handheld', index: 2, same: false, diff: '壳面图案位置偏移约 6px', reason: '图案位置偏移' },
    ],
    error: '', consistent: false, elapsedMs: 41200,
  },
  { id: 2, refPath: OUTDIR + '\\pattern-a_template-a_02.jpg', refName: '…_02.jpg', mode: 'background', count: 2, phase: 'generate', progress: 50, message: '生成第 1/2 张', files: [], error: '', consistent: false, elapsedMs: 0 },
]

let genTasks: any[] = FRESH ? [] : GEN_TASKS

// 按参考图 × 模式 造生图任务，并逐阶段推进（模拟真实生成节奏）。
function buildGenTasks(opt: any) {
  const out: any[] = []
  let id = 0
  for (const r of opt.refs || []) {
    for (const m of opt.modes || []) {
      out.push({
        id: ++id, refPath: r, refName: String(r).split(/[\\/]/).pop(),
        mode: m, count: Number(opt.count) || 2, phase: 'pending', progress: 0,
        message: '等待', files: [], error: '', consistent: false, elapsedMs: 0,
      })
    }
  }
  return out
}
let genTimer: any = null
const GEN_PHASES = ['pending', 'prompt', 'generate', 'check', 'done']
function playGen() {
  if (genTimer) return
  genTimer = setInterval(() => {
    if (genState !== 'running') return
    const t = genTasks.find((x: any) => x.phase !== 'done' && x.phase !== 'failed')
    if (!t) {
      clearInterval(genTimer); genTimer = null
      genState = 'idle'
      emit('titao:gen', { type: 'done', message: `完成：${genTasks.length} 组生成，0 组失败`, state: 'idle' })
      return
    }
    const i = GEN_PHASES.indexOf(t.phase)
    const next = GEN_PHASES[Math.min(i + 1, GEN_PHASES.length - 1)]
    t.phase = next
    t.progress = Math.round(((GEN_PHASES.indexOf(next) + 1) / GEN_PHASES.length) * 100)
    t.message = next === 'gen' ? `生成第 1/${t.count} 张` : next === 'verify' ? '视觉校验一致性' : next === 'done' ? `生成 ${t.count} 张` : '等待'
    if (next === 'done') {
      t.files = SAMPLE_JPG.slice(0, t.count).map((path, index) => ({
        path, mode: t.mode, index: index + 1, same: true, reason: '', diff: '', retried: false,
      }))
      t.consistent = true
      t.elapsedMs = 41000
    }
    emit('titao:gen', {
      type: 'task', task: { ...t },
      done: genTasks.filter((x: any) => x.phase === 'done').length,
      failed: genTasks.filter((x: any) => x.phase === 'failed').length,
      state: 'running',
    })
    emit('titao:gen', { type: 'log', level: 'info', message: `[${t.id}] ${t.refName} × ${t.mode} → ${next}` })
  }, 900)
}

let runState = 'idle'
let runTasks = FRESH ? [] : SAMPLE_TASKS
let genState = 'idle'
// 用 sessionStorage 记住「上次没跑完」，刷新页面后仍能审查补跑横幅。
let pendingQueue: any = (() => {
  if (FRESH) return null
  try { const v = sessionStorage.getItem('__mock_pending'); return v ? JSON.parse(v) : null } catch { return null }
})()
function setPending(v: any) {
  pendingQueue = v
  try { v ? sessionStorage.setItem('__mock_pending', JSON.stringify(v)) : sessionStorage.removeItem('__mock_pending') } catch {}
}
const listeners: Record<string, Array<(d: any) => void>> = {}

function emit(name: string, data: any) {
  for (const fn of listeners[name] || []) {
    try { fn(data) } catch {}
  }
}

// 逐阶段推进任务，模拟真实渲染节奏（暂停时停住）。
const PHASES = ['pattern', 'precheck', 'render', 'judge', 'tune', 'split', 'done']
let timer: any = null
function playRun() {
  if (timer) return
  timer = setInterval(() => {
    if (runState !== 'running') return
    const t = runTasks.find((x: any) => x.phase !== 'done' && x.phase !== 'skipped' && x.phase !== 'failed')
    if (!t) {
      clearInterval(timer); timer = null
      runState = 'idle'
      emit('titao:event', { type: 'done', message: '完成：4 组成功，0 组失败', done: 4, failed: 0, state: 'idle' })
      return
    }
    const i = PHASES.indexOf(t.phase)
    const next = PHASES[Math.min(i + 1, PHASES.length - 1)]
    t.phase = next
    t.progress = Math.round(((PHASES.indexOf(next) + 1) / PHASES.length) * 100)
    if (next === 'done') { t.files = SAMPLE_JPG.slice(0, 6); t.chosenScale = 1.15; t.tuneRounds = 2; t.judgeNote = '图案饱满且未压摄像头'; t.elapsedMs = 20100 }
    emit('titao:event', { type: 'task', task: { ...t }, done: runTasks.filter((x: any) => x.phase === 'done').length, failed: 0, state: 'running' })
    emit('titao:event', { type: 'log', level: 'info', message: `[${t.id}] ${t.patternName} × ${t.templateName} → ${next}` })
  }, 900)
}

const handlers: Record<string, (...a: any[]) => any> = {
  GetConfig: () => ({ config: structuredClone(CONFIG), path: 'demo/config.json' }),
  SaveConfig: (c: any) => { Object.assign(CONFIG, c); return structuredClone(CONFIG) },
  OpenConfigFile: () => null,
  CheckPhotoshop: () => ({ available: true, version: '27.7.0', error: '', checked: true }),
  AppVersion: () => '0.3.2',
  PSCaps: () => ({
    version: '27.7.0', major: 27, verified: true,
    doJavaScript: true, noDialogs: true, editSmartObject: true, replaceContents: false,
    summary: 'Photoshop 27.x（已验证） · DoJavaScript 是 · 关闭模态框 是 · 编辑智能对象 是 · 替换智能对象 否',
    notes: ['Photoshop 27.x 为已实测版本，全部核心能力可用。', '不启用 placedLayerReplaceContents：它会破坏模板原有的透视变换。'],
  }),
  CheckUpdate: () => ({ checked: true, hasNew: true, current: '0.3.2', latest: 'v0.3.3', notes: '## v0.3.3\n- 下一版演示用', url: 'https://example.invalid/releases', asset: 'https://example.invalid/titao.exe', size: 12000000, error: '' }),
  InstallUpdate: () => '已模拟下载 v0.3.3，未修改任何文件。',

  PickPatterns: () => [PATTERN, PATTERN2],
  PickPatternDir: () => 'demo/patterns',
  PickTemplates: () => [TPL, TPL2],
  PickTemplateDir: () => 'demo/templates',
  PickOutputDir: () => OUT,
  PickGenRefs: () => [OUTDIR + '\\pattern-a_template-a_01.jpg'],
  ListDir: () => [],
  ProbeTemplate: () => ({
    name: 'template-a.tif', width: 800, height: 4800, cellSize: 800, rows: 6,
    guides: [0, 800, 1600, 2400, 3200, 4000, 4800],
    groups: ['插画', '底图'],
    layers: [
      { name: '手指', kind: 'ArtLayer', visible: true, opacity: 100, blend: 'NORMAL', children: 0 },
      { name: '图层 1', kind: 'ArtLayer', visible: true, opacity: 50.2, blend: 'MULTIPLY', children: 0 },
      { name: '插画', kind: 'LayerSet', visible: true, opacity: 100, blend: 'PASSTHROUGH', children: 6 },
      { name: '底图', kind: 'LayerSet', visible: true, opacity: 100, blend: 'PASSTHROUGH', children: 6 },
      { name: '背景', kind: 'ArtLayer', visible: true, opacity: 100, blend: 'NORMAL', children: 0 },
    ],
    artGroups: [{ group: '插画', psbs: ['pattern.psb'], count: 6 }],
  }),
  PlanTasks: () => SAMPLE_TASKS.slice(0, 2),
  // 开始套图：把 PlanTasks 的结果逐条推进，模拟真实事件流，便于审查任务表与日志。
  StartRun: () => {
    runState = 'running'
    runTasks = SAMPLE_TASKS.slice(0, 4).map((t: any) => ({ ...t, phase: 'pending', progress: 0, files: [], elapsedMs: 0 }))
    for (const t of runTasks) emit('titao:event', { type: 'task', task: { ...t }, total: runTasks.length, done: 0, failed: 0, state: 'running' })
    playRun()
    return null
  },
  PauseRun: () => { runState = 'paused'; emit('titao:event', { type: 'state', state: 'paused' }); return null },
  ResumeRun: () => { runState = 'running'; emit('titao:event', { type: 'state', state: 'running' }); playRun(); return null },
  StopRun: () => { runState = 'idle'; emit('titao:event', { type: 'done', message: '已停止：2 组成功，0 组失败，2 组未执行', done: 2, failed: 0, state: 'idle' }); return null },
  CancelRun: () => { runState = 'idle'; emit('titao:event', { type: 'done', message: '已取消', done: 0, failed: 0, state: 'idle' }) },
  PendingQueue: () => pendingQueue,
  ClearQueue: () => { setPending(null); return null },
  RunStatus: () => ({ running: runState === 'running', state: runState, tasks: runTasks }),

  OpenPath: () => null,
  RevealInExplorer: () => null,
  OpenOutputDir: () => null,
  NormalizePreview: () => ({ path: PATTERN, width: 944, height: 1802, trimmed: true, bbox: [1, 73, 945, 1875] }),
  PreviewPattern: () => PREVIEW_URL,
  PreviewCell: () => PREVIEW_URL,
  PreviewGenerated: () => PREVIEW_URL,

  ListOutputs: () => (CONFIG.output_dir ? [
    { name: 'pattern-a', dir: OUTDIR, count: SAMPLE_JPG.length, files: SAMPLE_JPG },
  ] : []),
  ListGenerated: () => (CONFIG.output_dir ? [
    { name: 'pattern-a', dir: OUTDIR + '\\生成图', count: 3, files: [SAMPLE_JPG[2], SAMPLE_JPG[4], SAMPLE_JPG[5]] },
  ] : []),

  StartGen: (opt: any) => {
    genTasks = buildGenTasks(opt)
    genState = 'running'
    // 一次性把全部任务推给前端，任务表的「总数」才对得上。
    for (const t of genTasks) emit('titao:gen', { type: 'task', task: { ...t }, done: 0, failed: 0, state: 'running' })
    playGen()
    return null
  },
  PauseGen: () => { genState = 'paused'; emit('titao:gen', { type: 'state', state: 'paused' }); return null },
  ResumeGen: () => { genState = 'running'; emit('titao:gen', { type: 'state', state: 'running' }); playGen(); return null },
  StopGen: () => { genState = 'idle'; emit('titao:gen', { type: 'done', message: '已停止生图', state: 'idle' }); return null },
  GenStatus: () => ({
    state: genState, running: genState === 'running', tasks: genTasks, done: genTasks.filter((t: any) => t.phase === 'done').length,
    failed: genTasks.filter((t: any) => t.phase === 'failed').length,
    hasKey: !!CONFIG.gen_api_key, count: CONFIG.gen_count, autoRun: !!CONFIG.gen_auto_run,
    model: CONFIG.gen_model, size: CONFIG.gen_size,
  }),
  GenModes: () => ([
    { id: 'handheld', label: '新手持图' },
    { id: 'background', label: '换背景图' },
  ]),
}

;(window as any).go = {
  main: {
    App: new Proxy({}, {
      get: (_t, prop: string) => (...args: any[]) => {
        const fn = handlers[prop]
        if (!fn) {
          console.warn('[mock] 未实现:', prop)
          return Promise.resolve(null)
        }
        try { return Promise.resolve(fn(...args)) } catch (e) { return Promise.reject(e) }
      },
    }),
  },
}

;(window as any).runtime = {
  EventsOnMultiple: (name: string, cb: (d: any) => void) => {
    ;(listeners[name] ||= []).push(cb)
    return () => { listeners[name] = (listeners[name] || []).filter((f) => f !== cb) }
  },
  EventsOn: (name: string, cb: (d: any) => void) => {
    ;(listeners[name] ||= []).push(cb)
    return () => { listeners[name] = (listeners[name] || []).filter((f) => f !== cb) }
  },
  EventsOff: () => {},
  EventsOffAll: () => {},
  EventsOnce: () => {},
  EventsEmit: (name: string, data: any) => emit(name, data),
  WindowSetTitle: () => {},
  LogInfo: (m: string) => console.log('[mock]', m),
  LogError: (m: string) => console.error('[mock]', m),
}

// 供人工审查时切换状态：控制台执行 __mock.pause() / __mock.resume() / __mock.pending() 等
;(window as any).__mock = {
  run: () => { runState = 'running'; emit('titao:event', { type: 'state', state: 'running' }) },
  pause: () => { runState = 'paused'; emit('titao:event', { type: 'state', state: 'paused' }) },
  resume: () => { runState = 'running'; emit('titao:event', { type: 'state', state: 'running' }) },
  idle: () => { runState = 'idle'; emit('titao:event', { type: 'state', state: 'idle' }) },
  pending: () => { setPending({ tasks: SAMPLE_TASKS, pending: 4, finished: false }) },
  noPending: () => { setPending(null) },
  emptyTasks: () => { runTasks = []; emit('titao:event', { type: 'reset' }) },
  fillTasks: () => { runTasks = SAMPLE_TASKS },
  genIdle: () => { genState = 'idle' },
  genRun: () => { genState = 'running' },
  // 切「已配 Key / 未配 Key」，用于审查生图按钮可用性与缺 Key 提示
  genKey: (on = true) => { CONFIG.gen_api_key = on ? 'demo-key-not-a-credential' : ''; emit('titao:event', { type: 'log', level: 'info', message: 'mock: gen key ' + (on ? 'on' : 'off') }) },
  log: (level: string, message: string) => emit('titao:event', { type: 'log', level, message }),
  event: (ev: any) => emit('titao:event', ev),
}

mount(App, { target: document.getElementById('app')! })
