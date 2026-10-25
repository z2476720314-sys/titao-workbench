import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'

const source = readFileSync(new URL('./src/mock.ts', import.meta.url), 'utf8')
const code = ts.transpileModule(source.replace(/^import .*$/gm, ''), {
  compilerOptions: { target: ts.ScriptTarget.ESNext },
}).outputText
const window = {}
let tick
new Function('window', 'App', 'mount', 'document', 'location', 'sessionStorage', 'setInterval', 'clearInterval', code)(
  window, {}, () => {}, { getElementById: () => null }, { search: '' },
  { getItem: () => null }, (fn) => { tick = fn; return 1 }, () => {},
)
const api = window.go.main.App

test('template inspection exposes the ArtGroup fields consumed by the UI', async () => {
  const probe = await api.ProbeTemplate()
  assert.deepEqual(probe.artGroups, [{ group: '插画', psbs: ['pattern.psb'], count: 6 }])
})

test('completed generation exposes GenFile records for result previews', async () => {
  await api.StartGen({ refs: ['demo/reference.jpg'], modes: ['background'], count: 2 })
  for (let i = 0; i < 4; i++) tick()
  const status = await api.GenStatus()
  assert.equal(status.tasks[0].phase, 'done')
  assert.deepEqual(status.tasks[0].files, [
    { path: 'demo/output/pattern-a/sample-1.jpg', mode: 'background', index: 1, same: true, reason: '', diff: '', retried: false },
    { path: 'demo/output/pattern-a/sample-2.jpg', mode: 'background', index: 2, same: true, reason: '', diff: '', retried: false },
  ])
})
