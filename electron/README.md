# openNexus Electron 客户端

openNexus 的 Electron 桌面壳,与现有 Pake(Tauri)客户端**并存**,可自由选择使用哪一种。

## 工作原理

```
Electron 主进程(main.cjs)
   │  spawn ./opennexus --data-dir <userDataDir>   (release 模式)
   │  SERVER_PORT=<随机空闲端口> SERVER_MODE=release WEB_DIST=<web/dist>
   │
   ▼
Go 后端(单端口,同时服务 API + 前端静态文件)
   │  BrowserWindow.loadURL('http://127.0.0.1:<port>')
   ▼
React SPA(前端全部用相对路径 /api/v1,同源访问后端)
```

前端、后端**均无改动**:前端已用相对路径 `/api/v1`,SSE 与 WebSocket(终端)都基于 `window.location.host` 推导,Electron 加载后端同源页面即自动打通。

## 前置条件

- Go >= 1.25(编译后端)
- Node.js >= 20(前端构建 + Electron)
- 已执行过根目录的 `make build`,产出:
  - 根目录 `opennexus`(Go 二进制)
  - `web/dist/`(前端构建产物)

## 常用命令(在项目根目录执行)

```bash
# 开发:先 make build 出后端+前端,再拉起 Electron 窗口
make electron-dev

# 打包当前平台桌面应用(macOS 产出 dmg)
make electron-dist

# 一键安装到本机 /Applications(macOS),含打包
make electron-install

# 启动已安装的应用
make electron-run

# 卸载
make electron-uninstall
```

## 数据目录与日志

本地开发与桌面安装版共用同一数据根目录：

| 平台 | 数据目录 |
|------|---------|
| 全部 | `~/.openNexus` |

目录内容：`opennexus.db`（数据库）、`session/`（临时会话工作区）、可选 `config.yaml`。

配置加载顺序：`CONFIG_PATH` → `~/.openNexus/config.yaml` → 安装包/项目旁的 `config.yaml`。

后端日志写入 `~/.openNexus/launcher.log`，启动失败时可在此排查。

## 与 Pake 版的差异

| 项 | Electron | Pake(Tauri) |
|----|---------|-------------|
| 渲染层 | 自带 Chromium | 系统 WebView |
| 安装体积 | ~90–130MB | ~15–20MB |
| 渲染一致性 | 三端一致 | 受系统 WebView 影响 |
| 打包工具 | electron-builder | pake-cli |
| 端口 | 动态空闲端口 | 固定 8080 |

## 手动构建(不走 Makefile)

```bash
cd electron
npm install
npm start        # 开发运行
npm run dist     # 打包到 electron/dist/
```

打包前需确保根目录已 `make build`(产出 `opennexus` 二进制与 `web/dist`)。

> 注意：`extraResources` 会把根目录当前的 `opennexus` 二进制原样打进包，因此本地手动打包只能产出**与当前机器同架构/同系统**的应用。跨平台/跨架构分发请走下方 CI。

## 平台覆盖与代码签名

### CI 产物（`.github/workflows/release.yml` 的 `electron` job）

每个目标在对应系统的 runner 上**各自编译匹配架构的 Go 后端**再打包，因此不会打错架构二进制：

| 目标 | Runner | 后端构建 | 产物 |
|------|--------|---------|------|
| macos-arm64 | macos-latest | `darwin/arm64` CGO 原生 | `.dmg` |
| macos-x64 | macos-latest | `darwin/amd64` CGO 交叉编译 | `.dmg` |
| linux-x64 | ubuntu-latest | `linux/amd64` CGO 原生 | `.AppImage` |
| windows-x64 | windows-latest | `windows/amd64` 纯 Go SQLite | `.exe` |

### macOS 签名与公证（可选，需配置 Secrets）

默认**不签名/不公证**，产出的 `.dmg` 首次打开需在「系统设置 → 隐私与安全性」放行。配置以下 GitHub Secrets 后自动启用签名 + 公证（缺任一项即静默跳过对应步骤，构建仍成功）：

| Secret | 用途 |
|--------|------|
| `MAC_CSC_LINK` | base64 的 `.p12` 证书 |
| `MAC_CSC_KEY_PASSWORD` | 证书密码 |
| `APPLE_ID` | Apple 开发者账号邮箱 |
| `APPLE_APP_SPECIFIC_PASSWORD` | App 专用密码 |
| `APPLE_TEAM_ID` | 团队 ID |

签名相关配置：硬化运行时（`hardenedRuntime`）+ `entitlements.mac.plist` 授权（JIT、环境变量、库校验放宽以允许加载内置 Go 后端），公证由 `notarize.cjs`（`afterSign` 钩子，依赖 `@electron/notarize`）在检测到上述 Apple 环境变量时执行。
