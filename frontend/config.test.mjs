import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

// Exercise the component's configuration flow without a desktop window.
const source = readFileSync(new URL('./src/App.svelte', import.meta.url), 'utf8')
const script = source.match(/<script lang="ts">([\s\S]*?)<\/script>/)[1]
const code = ts.transpileModule(script.replace(/^\s*import .*$/gm, ''), {
  compilerOptions: { target: ts.ScriptTarget.ESNext },
}).outputText

test('settings save persists edits and applies them to the next run', async () => {
  const original = {
    output_dir: 'demo/old-output', group: 'old-group', fit: 'auto',
    trim_alpha: true, no_crop: true, jpg_quality: 95, cell_size: 800,
    offset_x: 0, offset_y: 0, auto_tune_on: true, max_rounds: 2,
    gen_count: 2, gen_api_key: '',
  }
  const edited = {
    output_dir: 'demo/new-output', group: 'new-group', fit: 'cover',
    trim_alpha: false, no_crop: false, jpg_quality: 90, cell_size: 1000,
    offset_x: 0.2, offset_y: -0.1, auto_tune_on: false, max_rounds: 1,
    gen_count: 3, gen_api_key: 'dummy',
  }
  let saved
  let run
  const api = {
    GetConfig: async () => ({ config: { ...original }, path: 'demo/config.json' }),
    SaveConfig: async (config) => { saved = { ...config }; return saved },
    StartRun: async (options) => { run = options },
  }
  const app = new Function('api', 'onMount', 'location', 'setTimeout', `${code}
    return {
      loadConfig, saveConf, startRun,
      edit: values => Object.assign(conf, values),
      ready: () => { psStatus.available = true; patternPaths = ['demo/pattern.png']; templatePaths = ['demo/template.tif'] },
      saved: () => confSaved,
    }
  `)(api, () => {}, { search: '?view=settings' }, () => {})

  await app.loadConfig()
  app.edit(edited)
  await app.saveConf()
  assert.deepEqual(saved, edited)
  assert.equal(app.saved(), '已保存')
  app.ready()
  await app.startRun()
  assert.deepEqual(run, {
    patterns: ['demo/pattern.png'], templates: ['demo/template.tif'],
    outputDir: 'demo/new-output', group: 'new-group', fit: 'cover', scales: [],
    auto: false, trimAlpha: false, noCrop: false, jpgQuality: 90, cellSize: 1000,
    offsetX: 0.2, offsetY: -0.1, patternName: '', maxRounds: 1,
  })
})
