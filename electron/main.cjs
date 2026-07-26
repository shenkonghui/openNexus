/*
 * openNexus Electron 主进程
 *
 * 职责与现有 Pake 方案一致:
 *   1. 探测一个空闲端口(避免固定 8080 的冲突,支持多开)
 *   2. 拉起 Go 后端二进制(release 模式,单端口同时服务 API + 前端)
 *   3. 轮询 /health 直到后端就绪
 *   4. 打开 BrowserWindow 加载 http://127.0.0.1:<port>
 *   5. 应用退出时回收后端进程
 *
 * 前端与后端均无改动:前端全部使用相对路径 /api/v1,同源访问后端。
 */

const { app, BrowserWindow, shell, dialog, ipcMain } = require('electron')
const { spawn, execSync } = require('child_process')
const net = require('net')
const http = require('http')
const path = require('path')
const fs = require('fs')
const os = require('os')

let backend = null
let mainWindow = null
let backendCrashed = false
let currentPort = null // 当前后端监听端口，重载时复用以保持前端 baseURL 不变
const LOG_TAG = '[opennexus-electron]'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

// 探测一个空闲端口传给后端(SERVER_PORT 环境变量,后端 config.go 已支持)。
function pickFreePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer()
    srv.unref()
    srv.on('error', reject)
    srv.listen(0, '127.0.0.1', () => {
      const port = srv.address().port
      srv.close(() => resolve(port))
    })
  })
}

// 数据目录：与本地开发统一为 ~/.openNexus
function userDataDir() {
  return path.join(os.homedir(), '.openNexus')
}

// 定位 Go 后端二进制:打包后位于 resources/backend,开发模式为项目根编译产物。
function backendBinPath() {
  const binName = process.platform === 'win32' ? 'opennexus.exe' : 'opennexus'
  if (app.isPackaged) {
    return path.join(process.resourcesPath, 'backend', binName)
  }
  return path.join(__dirname, '..', binName)
}

// 定位回退用 config.yaml（仅当 ~/.openNexus/config.yaml 不存在时使用）
function fallbackConfigPath() {
  if (app.isPackaged) {
    return path.join(process.resourcesPath, 'config.yaml')
  }
  return path.join(__dirname, '..', 'config.yaml')
}

// 后端工作目录:开发模式用项目根(便于相对路径/日志定位),打包用 resources 目录。
function backendCwd() {
  return path.dirname(fallbackConfigPath())
}

// 拉起 Go 后端。将 stdout/stderr 落盘到数据目录 launcher.log,便于排障。
function startBackend(port) {
  const dataDir = userDataDir()
  fs.mkdirSync(dataDir, { recursive: true })

  const binPath = backendBinPath()
  if (!fs.existsSync(binPath)) {
    throw new Error(`后端二进制不存在: ${binPath}\n请先执行 \`make build\` (开发) 或重新打包。`)
  }

  const userCfg = path.join(dataDir, 'config.yaml')
  const env = {
    ...process.env,
    SERVER_PORT: String(port),
    SERVER_MODE: 'release',
    WEB_DIST: app.isPackaged
      ? path.join(process.resourcesPath, 'web')
      : path.join(__dirname, '..', 'web', 'dist'),
  }
  // 用户目录无配置时，回退到 bundle/项目 config（CONFIG_PATH 优先于 ResolveConfigPath 的用户目录）
  if (!fs.existsSync(userCfg)) {
    env.CONFIG_PATH = fallbackConfigPath()
  }

  const logFile = path.join(dataDir, 'launcher.log')
  const logStream = fs.createWriteStream(logFile, { flags: 'a' })
  logStream.write(`\n${LOG_TAG} starting backend at ${new Date().toISOString()} port=${port} config=${env.CONFIG_PATH || userCfg}\n`)

  backend = spawn(binPath, ['--data-dir', dataDir], {
    env,
    cwd: backendCwd(),
    stdio: ['ignore', 'pipe', 'pipe'],
  })

  backend.stdout.on('data', (d) => logStream.write(d))
  backend.stderr.on('data', (d) => logStream.write(d))
  backend.on('exit', (code, signal) => {
    console.log(`${LOG_TAG} backend exited code=${code} signal=${signal}`)
    logStream.write(`${LOG_TAG} backend exited code=${code} signal=${signal}\n`)
    // 后端非正常退出且窗口还在,标记崩溃以便阻止窗口反复 loadURL
    if (code !== 0 && code !== null) backendCrashed = true
    backend = null
  })

  return dataDir
}

// 轮询 /health(沿用 Pake launcher 的 ~30s 超时逻辑)。
function waitReady(port, timeoutMs = 30000) {
  const deadline = Date.now() + timeoutMs
  return new Promise((resolve, reject) => {
    const tick = () => {
      if (backendCrashed) return reject(new Error('后端启动后立即退出,请查看 launcher.log'))
      if (Date.now() > deadline) return reject(new Error('等待后端就绪超时(30s)'))
      const req = http.get(`http://127.0.0.1:${port}/health`, (res) => {
        res.resume()
        if (res.statusCode === 200) resolve()
        else setTimeout(tick, 500)
      })
      req.on('error', () => setTimeout(tick, 500))
      req.setTimeout(2000, () => {
        req.destroy()
        setTimeout(tick, 500)
      })
    }
    tick()
  })
}

// 同步杀死后端进程树。Windows 下 SIGTERM 无效,需用 taskkill。
function killBackend() {
  if (!backend || backend.killed) return
  try {
    if (process.platform === 'win32') {
      // /t 杀掉子进程树(npx 拉起的 agent 子进程也要回收)
      spawn('taskkill', ['/pid', String(backend.pid), '/f', '/t'])
    } else {
      backend.kill('SIGTERM')
      // 兜底:3s 后仍未退出则 SIGKILL
      const pid = backend.pid
      setTimeout(() => {
        try {
          if (pid) process.kill(pid, 0) // 仍存活则抛错进入 catch
          process.kill(pid, 'SIGKILL')
        } catch {
          /* 已退出 */
        }
      }, 3000)
    }
  } catch (e) {
    console.error(`${LOG_TAG} kill backend failed:`, e)
  }
}

async function bootstrap() {
  let port
  try {
    port = await pickFreePort()
  } catch (e) {
    return fatal(`无法获取空闲端口: ${e.message}`)
  }
  currentPort = port

  let dataDir
  try {
    dataDir = startBackend(port)
  } catch (e) {
    return fatal(e.message)
  }

  try {
    await waitReady(port)
  } catch (e) {
    return fatal(`${e.message}\n\n日志: ${path.join(dataDir, 'launcher.log')}`)
  }

  mainWindow = new BrowserWindow({
    width: 1280,
    height: 800,
    minWidth: 900,
    minHeight: 600,
    title: 'openNexus',
    icon: path.join(__dirname, 'icon.png'),
    show: false,
    webPreferences: {
      preload: path.join(__dirname, 'preload.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false, // preload.cjs 需用 node require
      webviewTag: true, // 启用 <webview> 内置浏览器
    },
  })

  // 创建后再显示,避免白屏
  mainWindow.once('ready-to-show', () => mainWindow.show())

  // 外链(http(s))用系统浏览器打开,避免在应用内跳走丢失会话
  mainWindow.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https?:\/\//i.test(url)) {
      shell.openExternal(url)
      return { action: 'deny' }
    }
    return { action: 'allow' }
  })

  await mainWindow.loadURL(`http://127.0.0.1:${port}`)
}

// 弹出致命错误对话框并退出应用。
function fatal(message) {
  console.error(`${LOG_TAG} FATAL: ${message}`)
  if (app.isReady()) {
    dialog.showErrorBox('openNexus 启动失败', message)
  }
  app.quit()
}

// 重启后端进程（硬重载）。由设置页"系统 → 重载程序"通过 IPC 触发。
// 流程：杀旧进程 → 等待端口释放 → 同端口重新 spawn → 等待就绪 → 刷新前端页面。
// 同端口复用避免前端缓存的 baseURL 失效；刷新页面让前端重新拉取配置。
// 注意：进行中的会话/任务会随旧后端进程终止而被标记为 interrupted（用户可在任务列表恢复）。
ipcMain.handle('reload-backend', async () => {
  if (!currentPort) {
    return { ok: false, error: '后端尚未运行' }
  }
  try {
    killBackend()
    // killBackend 有 3s SIGKILL 兜底，等待端口彻底释放后再重新 spawn
    await sleep(3500)
    backendCrashed = false // 重置崩溃标记，允许 waitReady 重新轮询
    startBackend(currentPort)
    await waitReady(currentPort)
    if (mainWindow && !mainWindow.isDestroyed()) {
      mainWindow.webContents.reload()
    }
    return { ok: true }
  } catch (e) {
    return { ok: false, error: (e && e.message) ? e.message : String(e) }
  }
})

// ---- 生命周期 ----
app.whenReady().then(bootstrap).catch((e) => fatal(e && e.message ? e.message : String(e)))

app.on('window-all-closed', () => {
  // 非单实例场景直接退出;macOS 由用户从 Dock 退出
  killBackend()
  if (process.platform !== 'darwin') app.quit()
})

app.on('before-quit', () => killBackend())
app.on('will-quit', () => killBackend())

// 防止多实例导致后端冲突(可选,提升体验)
const gotLock = app.requestSingleInstanceLock()
if (!gotLock) {
  app.quit()
} else {
  app.on('second-instance', () => {
    if (mainWindow) {
      if (mainWindow.isMinimized()) mainWindow.restore()
      mainWindow.focus()
    }
  })
}
