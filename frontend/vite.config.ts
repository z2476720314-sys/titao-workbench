import {defineConfig} from 'vite'
import {svelte} from '@sveltejs/vite-plugin-svelte'
import fs from 'node:fs'
import path from 'node:path'

// 浏览器审查专用：把本地绝对路径当静态资源发出来。
// 只在本机 dev server 用，构建产物里不含这段逻辑。
function localFilePlugin() {
  return {
    name: 'local-file-server',
    configureServer(server: any) {
      server.middlewares.use('/__local/', (req: any, res: any) => {
        try {
          const rel = decodeURIComponent((req.url || '').split('?')[0]).replace(/^\//, '')
          const file = rel
          if (!fs.existsSync(file) || !fs.statSync(file).isFile()) {
            res.statusCode = 404
            res.end('not found')
            return
          }
          const ext = path.extname(file).toLowerCase()
          const mime: Record<string, string> = {
            '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg',
            '.webp': 'image/webp', '.tif': 'image/tiff', '.tiff': 'image/tiff',
            '.json': 'application/json',
          }
          res.setHeader('Content-Type', mime[ext] || 'application/octet-stream')
          fs.createReadStream(file).pipe(res)
        } catch (e: any) {
          res.statusCode = 500
          res.end(String(e))
        }
      })
    },
  }
}

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [svelte(), localFilePlugin()],
  server: {
    port: 5180,
    strictPort: false,
  },
})
