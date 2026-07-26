/*
 * openNexus 预加载脚本
 *
 * 当前前端完全通过 HTTP/SSE/WebSocket 与后端交互,不需要 Node 能力。
 * 这里仅暴露最小运行时信息(platform / versions),为未来扩展(如原生文件对话框、
 * 系统托盘、自动更新)预留一个 contextBridge 入口。
 *
 * getPathForFile:把拖入对话框的 File 对象反查为绝对路径,供"本地运行"场景下
 * 直接以 @<绝对路径> 引用文件(无需上传)。基于 Electron webUtils(Electron 30+)。
 *
 * 安全:contextIsolation=true,渲染进程拿不到 require/process。
 */

const path = require('path')
const { pathToFileURL } = require('url')
const { contextBridge, webUtils, ipcRenderer } = require('electron')

contextBridge.exposeInMainWorld('opennexus', {
  platform: process.platform,
  versions: {
    electron: process.versions.electron,
    node: process.versions.node,
    chrome: process.versions.chrome,
  },
  isElectron: true,
  // webview 预加载脚本,供前端 <webview preload="..."> 使用。
  // 必须是 file:// URL:Electron 的 webview preload 属性只接受 file:/asar: 协议,
  // 传裸路径会被静默忽略,导致 webview 里的选区/元素选择器完全失效。
  webviewPreload: pathToFileURL(path.join(__dirname, 'webview-preload.cjs')).toString(),
  // file 为渲染层 drop 事件或 <input type=file> 拿到的原生 File 对象。
  // 注意:不能跨 IPC 序列化,必须在渲染层同步调用。
  getPathForFile: (file) => webUtils.getPathForFile(file),
  // 重启后端进程并刷新前端页面（设置页"系统 → 重载程序"触发）。
  // 返回 { ok: boolean, error?: string }。仅桌面版可用。
  reloadBackend: () => ipcRenderer.invoke('reload-backend'),
})
