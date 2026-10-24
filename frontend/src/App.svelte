<script lang="ts">
  import { onMount } from 'svelte'
  import { EventsOn } from '../wailsjs/runtime/runtime.js'
  import * as api from '../wailsjs/go/main/App.js'
  import type { cfg, jobs } from '../wailsjs/go/models.js'

  // ---------------- 视图 ----------------
  type View = 'run' | 'tasks' | 'gen' | 'outputs' | 'settings' | 'about'
  let view: View = 'run'
  const NAV: { id: View; label: string; icon: string }[] = [
    { id: 'run', label: '套图', icon: 'M4 6h16M4 12h16M4 18h10' },
    { id: 'tasks', label: '任务队列', icon: 'M4 7h16M4 12h16M4 17h7' },
    { id: 'gen', label: '生图', icon: 'M4 5h16v14H4z M9 9a1.5 1.5 0 103 0 1.5 1.5 0 00-3 0 M4 16l4-4 3 3 3-3 6 6' },
    { id: 'outputs', label: '成品', icon: 'M4 5h16v14H4z M4 15l5-5 4 4 3-3 4 4' },
    { id: 'settings', label: '设置', icon: 'M12 15a3 3 0 100-6 3 3 0 000 6z M3 12h2M19 12h2M12 3v2M12 19v2' },
    { id: 'about', label: '关于', icon: 'M12 20a8 8 0 100-16 8 8 0 000 16z M12 11v5M12 8h.01' },
  ]

  // ---------------- 状态 ----------------
  let psStatus: any = { available: false, version: '', error: '', checked: false }
  let psChecking = true

  // 套图参数
  let patternPaths: string[] = []
  let templatePaths: string[] = []
  let outputDir = ''
  let group = '插画'
  let fit = 'auto'
  let auto = true
  let trimAlpha = true
  let offsetX = 0
  let offsetY = 0
  let manualScale = ''
  let jpgQuality = 95
  let cellSize = 800
  let patternName = ''

  // 任务
  let tasks: any[] = []
  let running = false
  let doneCount = 0
  let failedCount = 0
  let logs: { t: string; lvl: string; m: string }[] = []
  let logBox: HTMLElement

  // 配置
  let conf: any = null
  let confPath = ''
  let savingConf = false
  let confSaved = ''

  // 成品
  let outputs: any[] = []
  let outputLoading = false
  let zoom: string | null = null
  let thumbs: Record<string, string> = {}
  let thumbLoading = 0

  // 模板探查
  let probeInfo: any = null
  let probeLoading = false
  let probeErr = ''

  // 通知
  let notice = ''
  let noticeKind = 'info'

  // ---------------- 生图 ----------------
  let genRefs: string[] = []
  let genModes: string[] = ['handheld']
  let genCount = 2
  let genState = 'idle'
  let genHasKey = false
  let genTasks: any[] = []
  let genRunning = false
  let genDone = 0
  let genFailed = 0
  let genLogs: { t: string; lvl: string; m: string }[] = []
  let genLogBox: HTMLElement
  let genGroups: any[] = []
  let genThumbs: Record<string, string> = {}
  let genLoading = false

  const GEN_MODE_OPTS = [
    { id: 'handheld', label: '新手持图', hint: '换手持姿态与场景，壳不变' },
    { id: 'background', label: '换背景图', hint: '换背景环境，不出现手' },
  ]

  function genStamp() {
    const d = new Date()
    const p = (n: number) => String(n).padStart(2, '0')
    return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
  }

  function genLog(lvl: string, m: string) {
    genLogs = [...genLogs.slice(-499), { t: genStamp(), lvl, m }]
    queueMicrotask(() => {
      if (genLogBox) genLogBox.scrollTop = genLogBox.scrollHeight
    })
  }

  function onGenEvent(ev: any) {
    if (ev.type === 'log') {
      genLog(ev.level || 'info', ev.message)
      return
    }
    if (ev.type === 'task' && ev.task) {
      const idx = genTasks.findIndex((t) => t.id === ev.task.id)
      if (idx >= 0) {
        const next = [...genTasks]
        next[idx] = ev.task
        genTasks = next
      } else {
        genTasks = [...genTasks, ev.task]
      }
      genDone = ev.done ?? genDone
      genFailed = ev.failed ?? genFailed
      genRunning = true
      if (ev.state) genState = ev.state
      return
    }
    if (ev.type === 'done') {
      genRunning = false
      genState = 'idle'
      genDone = ev.done ?? genDone
      genFailed = ev.failed ?? genFailed
      notify(ev.failed > 0 ? 'warn' : 'info', ev.message)
      loadGenerated()
    }
  }

  async function refreshGen() {
    try {
      const r: any = await api.GenStatus()
      genState = r.state || 'idle'
      genRunning = !!r.running
      genHasKey = !!r.hasKey
      genTasks = r.tasks || []
      genDone = r.done || 0
      genFailed = r.failed || 0
      if (r.count) genCount = r.count
    } catch {}
  }

  async function addGenRefs() {
    try {
      const fs = await api.PickGenRefs()
      if (fs?.length) genRefs = uniq([...genRefs, ...fs])
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  function toggleGenMode(id: string) {
    if (genModes.includes(id)) {
      const next = genModes.filter((m) => m !== id)
      genModes = next.length ? next : [id]
    } else {
      genModes = [...genModes, id]
    }
  }

  async function startGen() {
    if (!genRefs.length) return notify('warn', '请先选择参考图')
    if (!genModes.length) return notify('warn', '请至少选一种生图模式')
    if (!genHasKey) return notify('err', '未配置生图 Key：设置 → 生图模型 → API Key')

    genLogs = []
    genTasks = []
    genDone = 0
    genFailed = 0
    try {
      await api.StartGen({
        refs: genRefs,
        modes: genModes,
        count: Number(genCount) || 2,
        outputRoot: outputDir,
      } as any)
      genRunning = true
      genState = 'running'
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  async function pauseGen() {
    try {
      await api.PauseGen()
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  async function resumeGen() {
    try {
      await api.ResumeGen()
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  async function stopGen() {
    await api.StopGen()
    notify('info', '已请求停止，当前这张处理完就停')
  }

  async function loadGenerated() {
    genLoading = true
    genThumbs = {}
    try {
      genGroups = await api.ListGenerated()
    } catch (e: any) {
      notify('err', String(e))
    }
    genLoading = false
    await fillGenThumbs()
  }

  async function fillGenThumbs() {
    const all: string[] = []
    for (const g of genGroups) for (const f of g.files || []) all.push(f)
    let cursor = 0
    const worker = async () => {
      while (cursor < all.length) {
        const p = all[cursor++]
        try {
          const url = await api.PreviewGenerated(p, 320)
          if (url) genThumbs = { ...genThumbs, [p]: url }
        } catch {}
      }
    }
    await Promise.all(Array.from({ length: Math.min(4, all.length) }, worker))
  }

  const GEN_PHASE_LABEL: Record<string, string> = {
    pending: '等待',
    prompt: '写提示词',
    generate: '生成中',
    check: '一致性校验',
    retry: '重试',
    done: '完成',
    failed: '失败',
    canceled: '已取消',
  }

  function notify(kind: string, msg: string) {
    noticeKind = kind
    notice = msg
    setTimeout(() => {
      if (notice === msg) notice = ''
    }, 6000)
  }

  // ---------------- 初始化 ----------------
  onMount(() => {
    loadConfig()
    refreshPS()
    refreshStatus()
    refreshGen()
    const off = EventsOn('titao:event', (ev: any) => onEvent(ev))
    const offGen = EventsOn('titao:gen', (ev: any) => onGenEvent(ev))
    return () => {
      if (typeof off === 'function') off()
      if (typeof offGen === 'function') offGen()
    }
  })

  async function loadConfig() {
    try {
      const r: any = await api.GetConfig()
      conf = r.config
      confPath = r.path
      outputDir = conf.output_dir || ''
      group = conf.group || '插画'
      fit = conf.fit || 'auto'
      trimAlpha = conf.trim_alpha !== false
      jpgQuality = conf.jpg_quality || 95
      cellSize = conf.cell_size || 800
      offsetX = conf.offset_x || 0
      offsetY = conf.offset_y || 0
      genCount = conf.gen_count || 2
      genHasKey = !!conf.gen_api_key
    } catch (e: any) {
      notify('err', '加载配置失败：' + e)
    }
  }

  async function refreshPS() {
    psChecking = true
    try {
      psStatus = await api.CheckPhotoshop()
    } catch (e: any) {
      psStatus = { available: false, version: '', error: String(e), checked: true }
    }
    psChecking = false
  }

  async function refreshStatus() {
    try {
      const r: any = await api.RunStatus()
      tasks = r.tasks || []
      running = r.running
      doneCount = tasks.filter((t) => t.phase === 'done').length
      failedCount = tasks.filter((t) => t.phase === 'failed').length
    } catch {}
  }

  // ---------------- 事件 ----------------
  function onEvent(ev: any) {
    if (ev.type === 'log') {
      const now = new Date()
      const t = `${String(now.getHours()).padStart(2, '0')}:${String(now.getMinutes()).padStart(2, '0')}:${String(now.getSeconds()).padStart(2, '0')}`
      logs = [...logs.slice(-499), { t, lvl: ev.level || 'info', m: ev.message }]
      queueMicrotask(() => {
        if (logBox) logBox.scrollTop = logBox.scrollHeight
      })
      return
    }
    if (ev.type === 'task' && ev.task) {
      const idx = tasks.findIndex((t) => t.id === ev.task.id)
      if (idx >= 0) {
        const next = [...tasks]
        next[idx] = ev.task
        tasks = next
      } else {
        tasks = [...tasks, ev.task]
      }
      doneCount = ev.done ?? doneCount
      failedCount = ev.failed ?? failedCount
      running = true
      return
    }
    if (ev.type === 'done') {
      running = false
      doneCount = ev.done ?? doneCount
      failedCount = ev.failed ?? failedCount
      notify(ev.failed > 0 ? 'warn' : 'info', ev.message)
      if (view === 'outputs') loadOutputs()
    }
  }

  // ---------------- 路径操作 ----------------
  async function addPatterns() {
    try {
      const fs = await api.PickPatterns()
      if (fs?.length) {
        patternPaths = uniq([...patternPaths, ...fs])
      }
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  async function addPatternDir() {
    try {
      const d = await api.PickPatternDir()
      if (d) patternPaths = uniq([...patternPaths, d])
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  async function addTemplates() {
    try {
      const fs = await api.PickTemplates()
      if (fs?.length) templatePaths = uniq([...templatePaths, ...fs])
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  async function addTemplateDir() {
    try {
      const d = await api.PickTemplateDir()
      if (d) templatePaths = uniq([...templatePaths, d])
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  async function pickOut() {
    try {
      const d = await api.PickOutputDir()
      if (d) outputDir = d
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  function uniq(a: string[]) {
    return [...new Set(a.filter(Boolean))]
  }

  function removeAt(list: string[], i: number) {
    return list.filter((_, k) => k !== i)
  }

  function baseName(p: string) {
    const parts = p.split(/[\\/]/)
    return parts[parts.length - 1] || p
  }

  // ---------------- 模板探查 ----------------
  async function probeTemplate() {
    const tpl = templatePaths.find((p) => /\.tiff?$/i.test(p))
    if (!tpl) {
      notify('warn', '先添加一个 .tif 模板再探查')
      return
    }
    probeLoading = true
    probeErr = ''
    probeInfo = null
    try {
      probeInfo = await api.ProbeTemplate(tpl)
      if (probeInfo?.artGroups?.length) {
        const names = probeInfo.artGroups.map((g: any) => g.group)
        if (!names.includes(group)) group = names[0]
      }
    } catch (e: any) {
      probeErr = String(e)
    }
    probeLoading = false
  }

  // ---------------- 运行 ----------------
  async function startRun() {
    if (!patternPaths.length) return notify('warn', '请先添加图案')
    if (!templatePaths.length) return notify('warn', '请先添加模板')
    if (!psStatus.available) return notify('err', 'Photoshop 未就绪：' + (psStatus.error || '未知'))

    const scales = manualScale
      .split(/[,，\s]+/)
      .map((s) => parseFloat(s))
      .filter((n) => !isNaN(n) && n > 0)

    const opt = {
      patterns: patternPaths,
      templates: templatePaths,
      outputDir,
      group,
      fit,
      scales: scales.length ? scales : [],
      auto: scales.length ? false : auto,
      trimAlpha,
      offsetX: Number(offsetX) || 0,
      offsetY: Number(offsetY) || 0,
      cellSize: Number(cellSize) || 800,
      jpgQuality: Number(jpgQuality) || 95,
      patternName,
    }

    logs = []
    tasks = []
    doneCount = 0
    failedCount = 0
    try {
      await api.StartRun(opt as any)
      running = true
      view = 'tasks'
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  async function cancelRun() {
    await api.CancelRun()
    notify('info', '已请求停止，当前这张处理完就停')
  }

  // ---------------- 成品 ----------------
  async function loadOutputs() {
    outputLoading = true
    thumbs = {}
    try {
      outputs = await api.ListOutputs()
    } catch (e: any) {
      notify('err', String(e))
    }
    outputLoading = false
    fillThumbs()
  }

  /** 批量把成品 JPG 转成缩略图 data URL（限流，避免一次并发太多） */
  async function fillThumbs() {
    const all: string[] = []
    for (const g of outputs) for (const f of g.files || []) all.push(f)
    thumbLoading = all.length
    const LIMIT = 4
    let cursor = 0
    const worker = async () => {
      while (cursor < all.length) {
        const p = all[cursor++]
        try {
          const url = await api.PreviewCell(p, 320)
          if (url) thumbs = { ...thumbs, [p]: url }
        } catch {}
        thumbLoading--
      }
    }
    await Promise.all(Array.from({ length: Math.min(LIMIT, all.length) }, worker))
  }

  async function showThumb(path: string) {
    try {
      zoom = await api.PreviewCell(path, 900)
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  async function showGenThumb(path: string) {
    try {
      zoom = await api.PreviewGenerated(path, 900)
    } catch (e: any) {
      notify('err', String(e))
    }
  }

  // ---------------- 配置 ----------------
  async function saveConf() {
    if (!conf) return
    savingConf = true
    try {
      conf.output_dir = outputDir
      conf.group = group
      conf.fit = fit
      conf.trim_alpha = trimAlpha
      conf.jpg_quality = Number(jpgQuality) || 95
      conf.cell_size = Number(cellSize) || 800
      conf.offset_x = Number(offsetX) || 0
      conf.offset_y = Number(offsetY) || 0
      await api.SaveConfig(conf)
      confSaved = '已保存'
      setTimeout(() => (confSaved = ''), 2200)
    } catch (e: any) {
      notify('err', String(e))
    }
    savingConf = false
  }

  const FIT_OPTS = [
    { v: 'auto', label: 'auto · 画布 1:1（推荐）', hint: '实测每格刚好铺满壳面' },
    { v: 'contain', label: 'contain · 完整放下', hint: '以不裁切为基准' },
    { v: 'cover', label: 'cover · 铺满', hint: '以铺满为基准' },
    { v: 'stretch', label: 'stretch · 拉伸变形', hint: '不保持宽高比' },
    { v: 'none', label: 'none · 原始像素', hint: '不做任何缩放' },
  ]

  const phaseLabel: Record<string, string> = {
    pending: '等待',
    pattern: '规范化',
    render: 'PS 渲染',
    judge: '模型判断',
    export: '导出',
    done: '完成',
    failed: '失败',
    skipped: '跳过',
    canceled: '已取消',
  }

  function phaseClass(p: string) {
    if (p === 'done') return 'pill ok'
    if (p === 'failed') return 'pill err'
    if (p === 'canceled' || p === 'skipped') return 'pill warn'
    if (p === 'pending') return 'pill'
    return 'pill run'
  }
</script>

<div class="shell">
  <!-- ================= 侧栏 ================= -->
  <aside class="sidebar">
    <div class="brand">
      <div class="brand-title">套图工作台</div>
      <div class="brand-sub">Photoshop 自动套壳</div>
    </div>

    <nav class="nav">
      {#each NAV as n}
        <button class="nav-item" class:active={view === n.id} on:click={() => { view = n.id; if (n.id === 'outputs') loadOutputs() }}>
          <svg class="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
            <path d={n.icon} />
          </svg>
          <span>{n.label}</span>
          {#if n.id === 'tasks' && tasks.length}
            <span class="nav-badge">{tasks.length}</span>
          {/if}
          {#if n.id === 'outputs' && outputs.length}
            <span class="nav-badge">{outputs.reduce((a, g) => a + g.count, 0)}</span>
          {/if}
        </button>
      {/each}
    </nav>

    <div class="sidebar-foot">
      <span class="dot" class:ok={psStatus.available} class:bad={psStatus.checked && !psStatus.available} class:idle={!psStatus.checked}></span>
      <span>
        {#if psChecking}检测中…
        {:else if psStatus.available}PS {psStatus.version}
        {:else}未连接{/if}
      </span>
    </div>
  </aside>

  <!-- ================= 主区 ================= -->
  <main class="main">
    {#if notice}
      <div class="notice {noticeKind}">{notice}</div>
    {/if}

    <!-- ---------- 套图 ---------- -->
    {#if view === 'run'}
      <div class="page-head">
        <div>
          <h1 class="page-title">套图</h1>
          <div class="page-desc">
            把抠好的透明图案套进 TIF 模板的智能对象，按模板分割线（每 {cellSize}px 一格）切格导出 JPG。
            模板与图案都只读，成品写到输出目录。
          </div>
        </div>
        <button on:click={refreshPS} disabled={psChecking}>重连 Photoshop</button>
      </div>

      {#if psStatus.checked && !psStatus.available}
        <div class="notice err">
          Photoshop 未就绪：{psStatus.error || '请确认已安装并至少启动过一次'}
        </div>
      {/if}

      <!-- 图案 -->
      <section class="card">
        <div class="card-head">
          <div>
            <div class="card-title">图案</div>
            <div class="card-hint">抠好的透明 PNG / JPG / TIF，可多选文件或直接选目录</div>
          </div>
          <div class="row">
            <button on:click={addPatterns}>选文件</button>
            <button on:click={addPatternDir}>选目录</button>
            {#if patternPaths.length}
              <button class="ghost" on:click={() => (patternPaths = [])}>清空</button>
            {/if}
          </div>
        </div>
        <div class="pathlist">
          {#each patternPaths as p, i}
            <div class="path-item">
              <span class="name">{p}</span>
              <button class="ghost del" on:click={() => (patternPaths = removeAt(patternPaths, i))}>移除</button>
            </div>
          {/each}
        </div>
      </section>

      <!-- 模板 -->
      <section class="card">
        <div class="card-head">
          <div>
            <div class="card-title">套图模板</div>
            <div class="card-hint">PS 分层 TIF，内部「插画」组里是智能对象</div>
          </div>
          <div class="row">
            <button on:click={addTemplates}>选文件</button>
            <button on:click={addTemplateDir}>选目录</button>
            <button on:click={probeTemplate} disabled={probeLoading || !templatePaths.length}>
              {probeLoading ? '探查中…' : '探查结构'}
            </button>
            {#if templatePaths.length}
              <button class="ghost" on:click={() => (templatePaths = [])}>清空</button>
            {/if}
          </div>
        </div>
        <div class="pathlist">
          {#each templatePaths as p, i}
            <div class="path-item">
              <span class="name">{p}</span>
              <button class="ghost del" on:click={() => (templatePaths = removeAt(templatePaths, i))}>移除</button>
            </div>
          {/each}
        </div>

        {#if probeErr}
          <div class="notice err" style="margin-top:12px;margin-bottom:0">{probeErr}</div>
        {/if}

        {#if probeInfo}
          <div style="margin-top:12px;display:grid;grid-template-columns:1fr 1fr;gap:16px">
            <dl class="kv">
              <dt>模板</dt><dd>{probeInfo.name}</dd>
              <dt>尺寸</dt><dd>{probeInfo.width} × {probeInfo.height}</dd>
              <dt>可切格</dt><dd>{probeInfo.width / probeInfo.cellSize} × {probeInfo.rows} = {probeInfo.rows} 张</dd>
              <dt>分割线</dt><dd>{probeInfo.guides?.length || 0} 条水平参考线</dd>
            </dl>
            <div>
              <span class="lbl">可套图的图层组</span>
              <div style="margin-top:6px;display:flex;flex-wrap:wrap;gap:6px">
                {#each probeInfo.artGroups as g}
                  <span class="pill" style="font-family:var(--mono)">
                    {g.group} · {g.psbs.length} 源 / {g.count} 格
                  </span>
                {/each}
              </div>
              <div style="margin-top:10px;font-size:10.5px;color:var(--muted)">
                多个格子共享同一个 PSB 源时只替换一次，关联格一起更新。
              </div>
            </div>
          </div>
        {/if}
      </section>

      <!-- 参数 -->
      <section class="card">
        <div class="card-head">
          <div class="card-title">摆放参数</div>
        </div>

        <div class="grid-2">
          <div class="field">
            <span class="lbl">图层组</span>
            <input type="text" bind:value={group} placeholder="插画" />
          </div>
          <div class="field">
            <span class="lbl">摆放方式</span>
            <select bind:value={fit}>
              {#each FIT_OPTS as o}
                <option value={o.v}>{o.label}</option>
              {/each}
            </select>
            <div style="font-size:10.5px;color:var(--muted);margin-top:2px">
              {FIT_OPTS.find((o) => o.v === fit)?.hint || ''}
            </div>
          </div>
        </div>

        <div class="field">
          <label class="check-row">
            <input type="checkbox" bind:checked={auto} />
            <span style="color:var(--text);font-size:12px">视觉模型自动判断图案大小</span>
          </label>
          <div style="font-size:10.5px;color:var(--muted);margin-left:20px">
            按饱满度/遮挡/重心打分，从候选倍数里挑一张。关掉则用固定档位。
          </div>
        </div>

        <div class="grid-2">
          <div class="field">
            <span class="lbl">手动档位（留空=用模型或配置里的候选）</span>
            <input type="text" bind:value={manualScale} placeholder="例如 0.95,1.05,1.15" />
            <div style="font-size:10.5px;color:var(--muted);margin-top:2px">
              1.0 = 刚好铺满壳面，&lt;1 留白，&gt;1 超出被裁切。填了就不调模型。
            </div>
          </div>
          <div class="field">
            <span class="lbl">图案名（仅单图案时覆盖）</span>
            <input type="text" bind:value={patternName} placeholder="留空则用文件名" />
          </div>
        </div>

        <div class="grid-4">
          <div class="field">
            <span class="lbl">水平微调</span>
            <input type="number" step="0.01" bind:value={offsetX} />
            <div style="font-size:10px;color:var(--muted)">0.1 = 右移 10%</div>
          </div>
          <div class="field">
            <span class="lbl">垂直微调</span>
            <input type="number" step="0.01" bind:value={offsetY} />
            <div style="font-size:10px;color:var(--muted)">-0.1 = 上移 10%</div>
          </div>
          <div class="field">
            <span class="lbl">格边长</span>
            <input type="number" bind:value={cellSize} />
          </div>
          <div class="field">
            <span class="lbl">JPG 质量</span>
            <input type="number" min="1" max="100" bind:value={jpgQuality} />
          </div>
        </div>

        <div class="field" style="margin-bottom:0">
          <label class="check-row">
            <input type="checkbox" bind:checked={trimAlpha} />
            <span style="color:var(--text);font-size:12px">裁掉图案四周透明留白</span>
          </label>
          <div style="font-size:10.5px;color:var(--muted);margin-left:20px">
            开启后图案真正居中且尺寸准确；关闭则保留原始画布位置。
          </div>
        </div>
      </section>

      <!-- 输出 -->
      <section class="card">
        <div class="card-head">
          <div class="card-title">输出</div>
          <div class="row">
            <button on:click={pickOut}>选择目录</button>
            <button class="ghost" on:click={() => api.OpenOutputDir()}>打开</button>
          </div>
        </div>
        <div class="field" style="margin-bottom:0">
          <input type="text" bind:value={outputDir} />
          <div style="font-size:10.5px;color:var(--muted);margin-top:3px">
            结构：<code style="font-family:var(--mono)">&lt;输出目录&gt;/&lt;图案名&gt;/&lt;图案名&gt;_&lt;模板名&gt;_NN.jpg</code>
          </div>
        </div>
      </section>

      <div class="actionbar">
        <button class="primary" on:click={startRun} disabled={running || !patternPaths.length || !templatePaths.length || !psStatus.available}>
          {running ? '运行中…' : '开始套图'}
        </button>
        {#if running}
          <button class="danger" on:click={cancelRun}>停止</button>
        {/if}
        <span class="spacer"></span>
        <span style="font-size:11px;color:var(--muted)">
          {patternPaths.length} 图案 × {templatePaths.length} 模板
        </span>
      </div>
    {/if}

    <!-- ---------- 任务队列 ---------- -->
    {#if view === 'tasks'}
      <div class="page-head">
        <div>
          <h1 class="page-title">任务队列</h1>
          <div class="page-desc">每个「图案 × 模板」是一个组合，串行执行（Photoshop 不能并发驱动）。</div>
        </div>
        <div class="row">
          {#if running}
            <button class="danger" on:click={cancelRun}>停止</button>
          {/if}
          <button on:click={refreshStatus}>刷新</button>
        </div>
      </div>

      <div class="card">
        <div class="row" style="gap:28px;margin-bottom:12px">
          <div class="stat"><span class="v">{tasks.length}</span><span class="k">总数</span></div>
          <div class="stat"><span class="v" style="color:var(--success)">{doneCount}</span><span class="k">完成</span></div>
          <div class="stat"><span class="v" style="color:var(--error)">{failedCount}</span><span class="k">失败</span></div>
          <div class="stat"><span class="v">{tasks.reduce((a, t) => a + (t.files?.length || 0), 0)}</span><span class="k">产出 JPG</span></div>
          <span class="spacer"></span>
          {#if running}<span class="pill run">运行中</span>{/if}
        </div>

        {#if tasks.length === 0}
          <div class="empty">
            <div class="big">还没有任务</div>
            <div class="small">去「套图」页添加图案和模板，点开始后这里会显示每个组合的进度。</div>
          </div>
        {:else}
          <table>
            <thead>
              <tr>
                <th style="width:44px">#</th>
                <th>图案</th>
                <th>模板</th>
                <th style="width:96px">状态</th>
                <th style="width:150px">进度</th>
                <th style="width:70px">倍数</th>
                <th style="width:64px">产出</th>
                <th style="width:70px">耗时</th>
                <th style="width:72px"></th>
              </tr>
            </thead>
            <tbody>
              {#each tasks as t}
                <tr>
                  <td class="mono" style="color:var(--muted)">{t.id}</td>
                  <td style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title={t.patternName}>{t.patternName}</td>
                  <td style="max-width:170px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title={t.templateName}>{t.templateName}</td>
                  <td>
                    <span class={phaseClass(t.phase)}>{phaseLabel[t.phase] || t.phase}</span>
                  </td>
                  <td>
                    <div class="bar" class:ok={t.phase === 'done'} class:err={t.phase === 'failed'}>
                      <i style="width:{t.progress || 0}%"></i>
                    </div>
                    <div style="font-size:10px;color:var(--muted);margin-top:2px" title={t.error || t.message}>
                      {(t.error || t.message || '').slice(0, 40)}
                    </div>
                  </td>
                  <td class="mono" style="color:var(--muted)">{t.chosenScale ? '×' + t.chosenScale : '—'}</td>
                  <td class="mono">{t.files?.length || 0}</td>
                  <td class="mono" style="color:var(--muted)">{t.elapsedMs ? (t.elapsedMs / 1000).toFixed(1) + 's' : '—'}</td>
                  <td>
                    {#if t.outputDir}
                      <button class="ghost" style="font-size:10px;padding:2px 6px" on:click={() => api.OpenPath(t.outputDir)}>打开</button>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>

      <section class="card">
        <div class="card-head">
          <div class="card-title">运行日志</div>
          <div class="row">
            <button class="ghost" on:click={() => (logs = [])}>清空</button>
          </div>
        </div>
        <div class="log" bind:this={logBox}>
          {#if logs.length === 0}
            <span style="color:#5a5e66">等待开始…</span>
          {:else}
            {#each logs as l}
              <div class="log-line {l.lvl}"><span class="t">{l.t}</span><span class="m">{l.m}</span></div>
            {/each}
          {/if}
        </div>
      </section>
    {/if}

    <!-- ---------- 生图 ---------- -->
    {#if view === 'gen'}
      <div class="page-head">
        <div>
          <h1 class="page-title">生图</h1>
          <div class="page-desc">
            拿套好的上架图当参考图，让生图模型重做一张「新手持图」或「换背景图」。
            手机壳本身（图案、颜色、比例、摄像头开孔、材质）必须与参考图一致，每张生成图都会过一遍视觉模型校验，不一致自动重试一次。
          </div>
        </div>
        <div class="row">
          <button on:click={refreshGen}>刷新</button>
          <button on:click={loadGenerated} disabled={genLoading}>{genLoading ? '读取中…' : '刷新产物'}</button>
        </div>
      </div>

      {#if !genHasKey}
        <div class="notice warn">
          未配置生图 Key —— 去「设置 → 生图模型」填 API Key 后才能生成。
        </div>
      {/if}

      <!-- 参考图 -->
      <section class="card">
        <div class="card-head">
          <div>
            <div class="card-title">参考图</div>
            <div class="card-hint">每张参考图代表一款手机壳，会各自生成一组图</div>
          </div>
          <div class="row">
            <button on:click={addGenRefs}>选图片</button>
            {#if genRefs.length}
              <button class="ghost" on:click={() => (genRefs = [])}>清空</button>
            {/if}
          </div>
        </div>
        <div class="pathlist">
          {#each genRefs as p, i}
            <div class="path-item">
              <span class="name">{p}</span>
              <button class="ghost del" on:click={() => (genRefs = removeAt(genRefs, i))}>移除</button>
            </div>
          {/each}
          {#if !genRefs.length}
            <div class="empty" style="padding:16px 0">
              <div class="small">还没有参考图。也可以直接把「成品」页里的上架图拖进来用。</div>
            </div>
          {/if}
        </div>
      </section>

      <!-- 模式与数量 -->
      <section class="card">
        <div class="card-head">
          <div class="card-title">生成方式</div>
        </div>

        <div class="grid-2">
          {#each GEN_MODE_OPTS as m}
            <div class="field">
              <label class="check-row">
                <input type="checkbox" checked={genModes.includes(m.id)} on:change={() => toggleGenMode(m.id)} />
                <span style="color:var(--text);font-size:12px">{m.label}</span>
              </label>
              <div style="font-size:10.5px;color:var(--muted);margin-left:20px">{m.hint}</div>
            </div>
          {/each}
        </div>

        <div class="grid-2">
          <div class="field">
            <span class="lbl">每种模式生成张数</span>
            <input type="number" min="1" max="10" bind:value={genCount} />
            <div style="font-size:10.5px;color:var(--muted)">
              1~10 张。接口每次只出 1 张，要 N 张就调用 N 次（会消耗 N 次额度）。
            </div>
          </div>
          <div class="field">
            <span class="lbl">输出目录</span>
            <input type="text" value={outputDir} readonly />
            <div style="font-size:10.5px;color:var(--muted)">
              结构：<code style="font-family:var(--mono)">&lt;输出目录&gt;/&lt;图案名&gt;/生成图/&lt;图案名&gt;_&lt;模式&gt;_genNN.jpg</code>
            </div>
          </div>
        </div>

        <div class="actionbar" style="margin-top:4px">
          {#if !genRunning}
            <button class="primary" on:click={startGen} disabled={!genRefs.length || !genModes.length || !genHasKey}>
              开始生图
            </button>
          {:else}
            {#if genState === 'paused'}
              <button class="primary" on:click={resumeGen}>继续</button>
            {:else}
              <button on:click={pauseGen} disabled={genState === 'stopping'}>暂停</button>
            {/if}
            <button class="danger" on:click={stopGen} disabled={genState === 'stopping'}>停止</button>
          {/if}
          <span class="spacer"></span>
          <span style="font-size:11px;color:var(--muted)">
            {genRefs.length} 参考图 × {genModes.length} 模式 × {genCount || 2} 张 =
            {genRefs.length * genModes.length * (Number(genCount) || 2)} 张
          </span>
          {#if genRunning}
            <span class={genState === 'paused' ? 'pill warn' : 'pill run'}>
              {genState === 'paused' ? '已暂停' : genState === 'stopping' ? '停止中' : '生成中'}
            </span>
          {/if}
        </div>
      </section>

      <!-- 任务进度 -->
      {#if genTasks.length}
        <section class="card">
          <div class="card-head">
            <div class="card-title">任务</div>
            <div class="row" style="gap:24px">
              <div class="stat"><span class="v">{genTasks.length}</span><span class="k">总数</span></div>
              <div class="stat"><span class="v" style="color:var(--success)">{genDone}</span><span class="k">完成</span></div>
              <div class="stat"><span class="v" style="color:var(--error)">{genFailed}</span><span class="k">失败</span></div>
              <div class="stat"><span class="v">{genTasks.reduce((a, t) => a + (t.files?.length || 0), 0)}</span><span class="k">产出图</span></div>
            </div>
          </div>
          <table>
            <thead>
              <tr>
                <th style="width:44px">#</th>
                <th>参考图</th>
                <th style="width:96px">状态</th>
                <th style="width:200px">进度</th>
                <th style="width:64px">产出</th>
                <th style="width:70px">耗时</th>
                <th style="width:72px"></th>
              </tr>
            </thead>
            <tbody>
              {#each genTasks as t}
                <tr>
                  <td class="mono" style="color:var(--muted)">{t.id}</td>
                  <td style="max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title={t.refPath}>{t.refName}</td>
                  <td><span class={phaseClass(t.phase)}>{GEN_PHASE_LABEL[t.phase] || t.phase}</span></td>
                  <td>
                    <div class="bar" class:ok={t.phase === 'done'} class:err={t.phase === 'failed'}>
                      <i style="width:{t.progress || 0}%"></i>
                    </div>
                    <div style="font-size:10px;color:var(--muted);margin-top:2px" title={t.error || t.message}>
                      {(t.error || t.message || '').slice(0, 40)}
                    </div>
                  </td>
                  <td class="mono">{t.files?.length || 0}</td>
                  <td class="mono" style="color:var(--muted)">{t.elapsedMs ? (t.elapsedMs / 1000).toFixed(1) + 's' : '—'}</td>
                  <td>
                    {#if t.outputDir}
                      <button class="ghost" style="font-size:10px;padding:2px 6px" on:click={() => api.OpenPath(t.outputDir)}>打开</button>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>

          <!-- 一致性明细 -->
          {#each genTasks as t}
            {#if t.files?.length}
              <div style="margin-top:14px">
                <div class="lbl">{t.refName} · 生成结果</div>
                <div class="thumbs">
                  {#each t.files as f}
                    <div class="thumb" role="button" tabindex="0"
                         on:click={() => showGenThumb(f.path)}
                         on:keydown={(e) => e.key === 'Enter' && showGenThumb(f.path)}>
                      {#if genThumbs[f.path]}
                        <img src={genThumbs[f.path]} alt={baseName(f.path)} loading="lazy" />
                      {:else}
                        <div class="thumb-ph"></div>
                      {/if}
                      <div class="cap">
                        <span class={f.same ? 'pill ok' : 'pill err'} style="font-size:9px;padding:1px 4px">
                          {f.same ? '一致' : '不一致'}
                        </span>
                        {baseName(f.path)}
                      </div>
                      {#if !f.same && f.diff}
                        <div class="cap" style="color:var(--error)" title={f.diff}>{(f.reason || f.diff).slice(0, 24)}</div>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            {/if}
          {/each}
        </section>
      {/if}

      <!-- 运行日志 -->
      {#if genTasks.length || genLogs.length}
        <section class="card">
          <div class="card-head">
            <div class="card-title">生图日志</div>
            <div class="row">
              <button class="ghost" on:click={() => (genLogs = [])}>清空</button>
            </div>
          </div>
          <div class="log" bind:this={genLogBox}>
            {#if genLogs.length === 0}
              <span style="color:#5a5e66">等待开始…</span>
            {:else}
              {#each genLogs as l}
                <div class="log-line {l.lvl}"><span class="t">{l.t}</span><span class="m">{l.m}</span></div>
              {/each}
            {/if}
          </div>
        </section>
      {/if}

      <!-- 历史产物 -->
      <section class="card">
        <div class="card-head">
          <div>
            <div class="card-title">已生成的图</div>
            <div class="card-hint">扫描输出目录下每个图案的「生成图」子目录</div>
          </div>
          <button class="ghost" on:click={() => api.OpenOutputDir()}>打开输出目录</button>
        </div>
        {#if genGroups.length === 0}
          <div class="empty">
            <div class="big">{genLoading ? '读取中…' : '还没有生成图'}</div>
            <div class="small">生成完成后，JPG 会出现在 <code style="font-family:var(--mono)">&lt;输出目录&gt;/&lt;图案名&gt;/生成图/</code></div>
          </div>
        {:else}
          {#each genGroups as g}
            <div style="margin-bottom:16px">
              <div class="row" style="margin-bottom:8px">
                <span class="card-title" style="font-size:12px">{g.name}</span>
                <span class="card-hint">{g.count} 张</span>
                <span class="spacer"></span>
                <button class="ghost" style="font-size:10px;padding:2px 6px" on:click={() => api.OpenPath(g.dir)}>打开</button>
              </div>
              <div class="thumbs">
                {#each g.files as f}
                  <div class="thumb" role="button" tabindex="0"
                       on:click={() => showGenThumb(f)}
                       on:keydown={(e) => e.key === 'Enter' && showGenThumb(f)}>
                    {#if genThumbs[f]}
                      <img src={genThumbs[f]} alt={baseName(f)} loading="lazy" />
                    {:else}
                      <div class="thumb-ph"></div>
                    {/if}
                    <div class="cap">{baseName(f)}</div>
                  </div>
                {/each}
              </div>
            </div>
          {/each}
        {/if}
      </section>
    {/if}

    <!-- ---------- 成品 ---------- -->
    {#if view === 'outputs'}
      <div class="page-head">
        <div>
          <h1 class="page-title">成品</h1>
          <div class="page-desc">按图案分组的套图结果。点击缩略图看大图。</div>
        </div>
        <div class="row">
          <button on:click={loadOutputs} disabled={outputLoading}>{outputLoading ? '读取中…' : '刷新'}</button>
          <button on:click={() => api.OpenOutputDir()}>打开目录</button>
        </div>
      </div>

      {#if outputs.length === 0}
        <div class="card">
          <div class="empty">
            <div class="big">{outputLoading ? '读取中…' : '还没有成品'}</div>
            <div class="small">套图完成后，JPG 会出现在这里。当前输出目录：{outputDir || '（未设置）'}</div>
          </div>
        </div>
      {:else}
        {#each outputs as g}
          <section class="card">
            <div class="card-head">
              <div>
                <div class="card-title">{g.name}</div>
                <div class="card-hint">{g.count} 张 · {g.dir}</div>
              </div>
              <button class="ghost" on:click={() => api.OpenPath(g.dir)}>打开</button>
            </div>
            <div class="thumbs">
              {#each g.files as f}
                <div class="thumb" on:click={() => showThumb(f)} role="button" tabindex="0"
                     on:keydown={(e) => e.key === 'Enter' && showThumb(f)}>
                  {#if thumbs[f]}
                    <img src={thumbs[f]} alt={baseName(f)} loading="lazy" />
                  {:else}
                    <div class="thumb-ph"></div>
                  {/if}
                  <div class="cap">{baseName(f)}</div>
                </div>
              {/each}
            </div>
          </section>
        {/each}
      {/if}
    {/if}

    <!-- ---------- 设置 ---------- -->
    {#if view === 'settings'}
      <div class="page-head">
        <div>
          <h1 class="page-title">设置</h1>
          <div class="page-desc">
            配置存在程序目录的 <code style="font-family:var(--mono)">config.json</code>，改这里或直接编辑文件都行，不依赖环境变量。
          </div>
        </div>
        <div class="row">
          <button on:click={() => api.OpenConfigFile()}>打开配置文件</button>
          <button class="primary" on:click={saveConf} disabled={savingConf}>
            {savingConf ? '保存中…' : confSaved || '保存'}
          </button>
        </div>
      </div>

      {#if conf}
        <section class="card">
          <div class="card-head">
            <div>
              <div class="card-title">视觉模型</div>
              <div class="card-hint">用于自动判断图案摆放大小</div>
            </div>
            <span class={conf.api_key ? 'pill ok' : 'pill warn'}>{conf.api_key ? '已配置 Key' : '缺少 API Key'}</span>
          </div>
          <div class="grid-2">
            <div class="field">
              <span class="lbl">接口地址</span>
              <input type="text" bind:value={conf.base_url} placeholder="https://.../v1" />
            </div>
            <div class="field">
              <span class="lbl">模型</span>
              <input type="text" bind:value={conf.model} placeholder="deepseek-v4.1-flash" />
            </div>
          </div>
          <div class="field">
            <span class="lbl">API Key</span>
            <input type="text" bind:value={conf.api_key} placeholder="sk-..." />
          </div>
          <div class="grid-4">
            <div class="field">
              <span class="lbl">max_tokens</span>
              <input type="number" bind:value={conf.max_tokens} />
            </div>
            <div class="field">
              <span class="lbl">超时（秒）</span>
              <input type="number" bind:value={conf.timeout_seconds} />
            </div>
            <div class="field">
              <span class="lbl">temperature</span>
              <input type="number" step="0.05" bind:value={conf.temperature} />
            </div>
            <div class="field">
              <span class="lbl">送图最长边</span>
              <input type="number" bind:value={conf.image_max_side} />
            </div>
          </div>
          <div class="field">
            <span class="lbl">候选倍数（逗号分隔）</span>
            <input type="text" value={(conf.candidates || []).join(', ')}
                   on:change={(e) => {
                     const v = (e.currentTarget as HTMLInputElement).value
                     conf.candidates = v.split(/[,，\s]+/).map((s) => parseFloat(s)).filter((n) => !isNaN(n) && n > 0)
                   }} />
            <div style="font-size:10.5px;color:var(--muted);margin-top:2px">
              区间要覆盖「明显偏小 → 明显超出」，模型才能做绝对判断；区间太窄它只能相对比较，选出的倍数会漂移。
            </div>
          </div>
          <div class="field" style="margin-bottom:0">
            <label class="check-row">
              <input type="checkbox" bind:checked={conf.thinking} />
              <span style="color:var(--text);font-size:12px">启用思考链</span>
            </label>
            <div style="font-size:10.5px;color:var(--muted);margin-left:20px">
              关闭更快更省；开启可能让 content 被思考内容挤空。
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-head">
            <div>
              <div class="card-title">生图模型</div>
              <div class="card-hint">用于「生图」页：图生图重做手持图 / 换背景图</div>
            </div>
            <span class={conf.gen_api_key ? 'pill ok' : 'pill warn'}>{conf.gen_api_key ? '已配置 Key' : '缺少 API Key'}</span>
          </div>
          <div class="grid-2">
            <div class="field">
              <span class="lbl">接口地址</span>
              <input type="text" bind:value={conf.gen_base_url} placeholder="https://.../v1" />
            </div>
            <div class="field">
              <span class="lbl">模型</span>
              <input type="text" bind:value={conf.gen_model} placeholder="gpt-image-2.5" />
            </div>
          </div>
          <div class="field">
            <span class="lbl">API Key</span>
            <input type="text" bind:value={conf.gen_api_key} placeholder="留空则不启用生图" />
            <div style="font-size:10.5px;color:var(--muted);margin-top:2px">
              生图的 Key 与上面视觉模型的 Key 分开存，互不影响。
            </div>
          </div>
          <div class="grid-4">
            <div class="field">
              <span class="lbl">尺寸</span>
              <input type="text" bind:value={conf.gen_size} placeholder="1024x1024" />
            </div>
            <div class="field">
              <span class="lbl">超时（秒）</span>
              <input type="number" bind:value={conf.gen_timeout} />
            </div>
            <div class="field">
              <span class="lbl">默认张数</span>
              <input type="number" min="1" max="10" bind:value={conf.gen_count} />
            </div>
            <div class="field">
              <span class="lbl">输出根目录</span>
              <input type="text" bind:value={conf.output_dir} />
            </div>
          </div>
          <div class="field">
            <label class="check-row">
              <input type="checkbox" bind:checked={conf.gen_thinking} />
              <span style="color:var(--text);font-size:12px">生图启用思考链</span>
            </label>
            <div style="font-size:10.5px;color:var(--muted);margin-left:20px">
              默认关闭。开启后服务端会先思考再出图，更慢，但复杂构图可能更稳。
            </div>
          </div>
          <div class="field" style="margin-bottom:0">
            <label class="check-row">
              <input type="checkbox" bind:checked={conf.gen_auto_run} />
              <span style="color:var(--text);font-size:12px">套图完成后自动生图</span>
            </label>
            <div style="font-size:10.5px;color:var(--muted);margin-left:20px">
              打开后，每次套图跑完会把产出的上架图当参考图，按「生图」页选中的模式与张数自动接着生成。默认关闭。
            </div>
          </div>
        </section>

        <section class="card">
          <div class="card-head"><div class="card-title">默认路径</div></div>
          <div class="field">
            <span class="lbl">模板目录</span>
            <input type="text" bind:value={conf.template_dir} />
          </div>
          <div class="field">
            <span class="lbl">图案目录</span>
            <input type="text" bind:value={conf.pattern_dir} />
          </div>
          <div class="field" style="margin-bottom:0">
            <span class="lbl">输出目录</span>
            <input type="text" bind:value={conf.output_dir} />
          </div>
        </section>

        <section class="card">
          <div class="card-head"><div class="card-title">默认套图参数</div></div>
          <div class="grid-3">
            <div class="field">
              <span class="lbl">图层组</span>
              <input type="text" bind:value={conf.group} />
            </div>
            <div class="field">
              <span class="lbl">摆放方式</span>
              <select bind:value={conf.fit}>
                {#each FIT_OPTS as o}
                  <option value={o.v}>{o.v}</option>
                {/each}
              </select>
            </div>
            <div class="field">
              <span class="lbl">格边长</span>
              <input type="number" bind:value={conf.cell_size} />
            </div>
          </div>
          <div class="grid-3">
            <div class="field">
              <span class="lbl">JPG 质量</span>
              <input type="number" min="1" max="100" bind:value={conf.jpg_quality} />
            </div>
            <div class="field">
              <span class="lbl">水平微调</span>
              <input type="number" step="0.01" bind:value={conf.offset_x} />
            </div>
            <div class="field">
              <span class="lbl">垂直微调</span>
              <input type="number" step="0.01" bind:value={conf.offset_y} />
            </div>
          </div>
          <div class="field" style="margin-bottom:0">
            <label class="check-row">
              <input type="checkbox" bind:checked={conf.trim_alpha} />
              <span style="color:var(--text);font-size:12px">默认裁掉图案透明留白</span>
            </label>
          </div>
        </section>

        <div class="actionbar">
          <button class="primary" on:click={saveConf} disabled={savingConf}>
            {savingConf ? '保存中…' : confSaved || '保存设置'}
          </button>
          <span class="spacer"></span>
          <span style="font-size:10.5px;color:var(--muted);font-family:var(--mono)">{confPath}</span>
        </div>
      {/if}
    {/if}

    <!-- ---------- 关于 ---------- -->
    {#if view === 'about'}
      <div class="page-head">
        <div>
          <h1 class="page-title">关于</h1>
          <div class="page-desc">套图工作台 — 把抠好的图案自动套到手机壳模板上并按分割线切格导出。</div>
        </div>
      </div>

      <section class="card">
        <div class="card-head"><div class="card-title">工作原理</div></div>
        <ol style="margin:0;padding-left:18px;font-size:12px;line-height:1.85;color:#c8cad0">
          <li>模板 TIF 复制到临时目录，<b>原文件永不打开或修改</b>。</li>
          <li>进入「插画」组里每个智能对象内部的 PSB，隐藏原有图层后置入图案并居中。</li>
          <li>多个格子共享同一个 PSB 源时只替换一次，关联格一起更新（同款图案的不同手持角度）。</li>
          <li>按候选倍数各渲染一张整幅拼合图，交视觉模型挑最饱满且不裁坏的一张。</li>
          <li>按每 <span class="mono">{cellSize}px</span> 一格切格，导出高质量 JPG。</li>
        </ol>
      </section>

      <section class="card">
        <div class="card-head"><div class="card-title">倍数 t 的含义</div></div>
        <table>
          <thead><tr><th style="width:100px">t</th><th>效果</th></tr></thead>
          <tbody>
            <tr><td class="mono">1.0</td><td>图案刚好铺满壳面（至少一个方向顶到壳边）</td></tr>
            <tr><td class="mono">&lt; 1.0</td><td>留白，图案变小</td></tr>
            <tr><td class="mono">&gt; 1.0</td><td>超出壳面，被裁切</td></tr>
          </tbody>
        </table>
        <div style="margin-top:10px;font-size:11px;color:var(--muted);line-height:1.7">
          模板的智能对象变换已经把图案画布映射到壳面，所以按画布 1:1 置入在每一格都正好铺满，
          包括横向倾斜的手持格。这也是 <b>auto</b> 成为推荐值的原因。
        </div>
      </section>

      <section class="card">
        <div class="card-head"><div class="card-title">环境</div></div>
        <dl class="kv">
          <dt>Photoshop</dt><dd>{psStatus.available ? 'v' + psStatus.version : '未连接'}</dd>
          <dt>配置文件</dt><dd>{confPath}</dd>
          <dt>输出目录</dt><dd>{outputDir}</dd>
        </dl>
      </section>
    {/if}
  </main>
</div>

{#if zoom}
  <div class="modal-mask" on:click={() => (zoom = null)} role="presentation">
    <div class="modal" on:click|stopPropagation role="presentation">
      <img src={zoom} alt="成品预览" />
    </div>
  </div>
{/if}
