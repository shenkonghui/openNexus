import { apiFetch } from './client'
import type { TunnelStatus } from '../types'

// 隧道配置保存请求（写回 config.yaml 的 tunnel 段）。
// token 不传表示保留现有值；传空字符串表示清除。
export interface TunnelConfigPayload {
  enabled: boolean                    // 服务启动时自动开启
  mode: 'quick' | 'token' | 'ngrok' | 'vscode' // quick=临时隧道随机域名；token=具名隧道；ngrok=ngrok 隧道；vscode=VS Code Remote Tunnel
  token?: string                      // token=具名隧道 token；ngrok=authtoken（可留空用 ngrok 本地配置）
  hostname?: string                   // 对外域名（token 模式展示用；ngrok 模式为保留域名；vscode 模式为隧道名称 --name）
  provider?: 'github' | 'microsoft'   // vscode 模式登录账号提供商
  cloudflared_path?: string           // 自定义 cloudflared 路径
  ngrok_path?: string                 // 自定义 ngrok 路径
  vscode_path?: string                // 自定义 code CLI 路径
}

export function getTunnel(): Promise<{ data: TunnelStatus }> {
  return apiFetch('/tunnel')
}

export function updateTunnelConfig(payload: TunnelConfigPayload): Promise<{ data: { message: string } }> {
  return apiFetch('/tunnel', {
    method: 'PUT',
    body: JSON.stringify(payload),
  })
}

export function startTunnel(): Promise<{ data: TunnelStatus }> {
  return apiFetch('/tunnel/start', { method: 'POST' })
}

export function stopTunnel(): Promise<{ data: TunnelStatus }> {
  return apiFetch('/tunnel/stop', { method: 'POST' })
}
