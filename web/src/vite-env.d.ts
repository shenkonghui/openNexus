/// <reference types="vite/client" />

// Electron preload 通过 contextBridge 注入 window.opennexus。
// 浏览器(远程运行)场景下整个对象为 undefined。
interface Window {
  opennexus?: {
    isElectron: boolean
    platform: string
    versions: {
      electron: string
      node: string
      chrome: string
    }
    // webview 预加载脚本绝对路径(仅 Electron 可用)。
    webviewPreload?: string
    // 把拖入的 File 对象反查为本地绝对路径(仅 Electron 可用)。
    // 浏览器场景下不存在,消费前必须先判 isElectron。
    getPathForFile?: (file: File) => string
    // 重启后端进程并刷新前端页面(设置页"系统 → 重载程序"触发)。仅桌面版可用。
    reloadBackend?: () => Promise<{ ok: boolean; error?: string }>
  }
}
