import { createReadStream, existsSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import http from 'node:http'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const distDir = path.join(__dirname, 'dist')
const host = process.env.HOST || '0.0.0.0'
const port = Number(process.env.PORT || '4173')
const apiBaseURL = (process.env.VALDOCTOR_API_BASE_URL || '').trim()

if (!existsSync(path.join(distDir, 'index.html'))) {
  console.error('Standalone build not found. Run `npm run build:standalone` first.')
  process.exit(1)
}

const contentTypes = new Map([
  ['.css', 'text/css; charset=utf-8'],
  ['.html', 'text/html; charset=utf-8'],
  ['.js', 'application/javascript; charset=utf-8'],
  ['.json', 'application/json; charset=utf-8'],
  ['.svg', 'image/svg+xml'],
  ['.txt', 'text/plain; charset=utf-8'],
  ['.woff2', 'font/woff2'],
])

const server = http.createServer(async (req, res) => {
  const requestPath = new URL(req.url || '/', 'http://localhost').pathname

  if (requestPath === '/config.js') {
    res.writeHead(200, { 'Content-Type': 'application/javascript; charset=utf-8' })
    res.end(`window.__VALDOCTOR_CONFIG__ = { apiBaseURL: ${JSON.stringify(apiBaseURL)} };`)
    return
  }

  const relativePath = requestPath === '/' ? 'index.html' : requestPath.replace(/^\/+/, '')
  const diskPath = path.join(distDir, relativePath)

  if (diskPath.startsWith(distDir) && existsSync(diskPath)) {
    const contentType = contentTypes.get(path.extname(diskPath)) || 'application/octet-stream'
    res.writeHead(200, { 'Content-Type': contentType })
    createReadStream(diskPath).pipe(res)
    return
  }

  const indexHTML = await readFile(path.join(distDir, 'index.html'))
  res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
  res.end(indexHTML)
})

server.listen(port, host, () => {
  console.log(`Valdoctor live UI listening on http://${host}:${port}`)
  if (apiBaseURL !== '') {
    console.log(`Proxy-free API target: ${apiBaseURL}`)
  }
})
