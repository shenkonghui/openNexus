import { apiFetch } from './client'
import type { TunnelStatus } from '../types'

// 隧道配置保存请求（写回 config.yaml 的 tunnel 段）。
// token 不传表示保留现有值；传空字符串表示清除。
export interface TunnelConfigPayload {
  enabled: boolean            // 服务启动时自动开启
  mode: 'quick' | 'token'     // quick=临时隧道随机域名；token=具名隧道
  token?: string
  hostname?: string           // token 模式对外域名（界面展示用）
  cloudflared_path?: string   // 自定义 cloudflared 路径
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
