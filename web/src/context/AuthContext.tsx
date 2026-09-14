import { createContext, useContext, useState, useEffect, useCallback, type ReactNode } from 'react'
import type { User, AuthResponse } from '../types'
import * as authApi from '../api/auth'
import LoadingSpinner from '../components/LoadingSpinner'

// 认证状态
interface AuthState {
  user: User | null
  loading: boolean
}

// 认证上下文值
interface AuthContextValue extends AuthState {
  login: (account: string, password: string) => Promise<void>
  register: (username: string, email: string, password: string) => Promise<void>
  logout: () => Promise<void>
  refreshUser: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

const AUTH_INIT_RETRIES = 3
const AUTH_INIT_RETRY_DELAY = 1000

// 静态访问令牌（auth.static_token）的本地存储键：设备级长期凭证
const STATIC_TOKEN_KEY = 'opennexus.static_token'

// 捕获 URL 中的 ?token=xxx（静态访问令牌一次性授权）：存本地后从地址栏清除
function consumeTokenFromURL(): string | null {
  try {
    const params = new URLSearchParams(window.location.search)
    const token = params.get('token')
    if (!token) return null
    params.delete('token')
    const qs = params.toString()
    window.history.replaceState(null, '',
      `${window.location.pathname}${qs ? `?${qs}` : ''}${window.location.hash}`)
    return token
  } catch {
    return null
  }
}

// 用静态访问令牌换取 JWT（失败返回 null）
async function loginWithStaticToken(token: string): Promise<AuthResponse | null> {
  try {
    return await authApi.tokenLogin(token)
  } catch {
    return null
  }
}

async function waitForBackend(attempt: number): Promise<void> {
  if (attempt >= AUTH_INIT_RETRIES - 1) return
  await new Promise((resolve) => setTimeout(resolve, AUTH_INIT_RETRY_DELAY))
}

// AuthProvider 提供认证状态管理
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [loading, setLoading] = useState(true)

  // 启动时验证 token；失效时依次尝试 auto_login 与静态访问令牌（?token= 一次性授权过的设备）
  useEffect(() => {
    async function initAuth() {
      try {
        // URL 携带 ?token=xxx：静态访问令牌一次性授权，存本地并从地址栏清除
        const urlToken = consumeTokenFromURL()
        if (urlToken) localStorage.setItem(STATIC_TOKEN_KEY, urlToken)

        if (!urlToken) {
          const token = localStorage.getItem('access_token')
          if (token) {
            for (let attempt = 0; attempt < AUTH_INIT_RETRIES; attempt += 1) {
              try {
                const resp = await authApi.getMe({ skipAuthRedirect: true })
                setUser(resp.data)
                return
              } catch {
                await waitForBackend(attempt)
              }
            }
          }
        }
        localStorage.removeItem('access_token')
        localStorage.removeItem('refresh_token')

        // auto_login 未启用或后端未就绪时，回落静态访问令牌
        let tokenResp: AuthResponse | null = null
        if (!urlToken) {
          for (let attempt = 0; attempt < AUTH_INIT_RETRIES; attempt += 1) {
            try {
              const resp = await authApi.autoLogin()
              tokenResp = resp
              break
            } catch {
              await waitForBackend(attempt)
            }
          }
        }
        if (!tokenResp) {
          const staticToken = urlToken || localStorage.getItem(STATIC_TOKEN_KEY)
          if (staticToken) tokenResp = await loginWithStaticToken(staticToken)
        }
        if (tokenResp) {
          // 无论 auto_login 还是静态令牌，都必须落盘 JWT：否则后续受保护请求无
          // Authorization 头而 401，被 clearTokensAndRedirect 整页打回登录页形成刷新循环。
          localStorage.setItem('access_token', tokenResp.access_token)
          localStorage.setItem('refresh_token', tokenResp.refresh_token)
          setUser(tokenResp.user)
          return
        }
        // auto_login / 静态令牌都不可用，展示登录页
      } finally {
        setLoading(false)
      }
    }
    void initAuth()
  }, [])

  const login = useCallback(async (account: string, password: string) => {
    const resp = await authApi.login(account, password)
    localStorage.setItem('access_token', resp.access_token)
    localStorage.setItem('refresh_token', resp.refresh_token)
    setUser(resp.user)
  }, [])

  const register = useCallback(async (username: string, email: string, password: string) => {
    const resp = await authApi.register(username, email, password)
    localStorage.setItem('access_token', resp.access_token)
    localStorage.setItem('refresh_token', resp.refresh_token)
    setUser(resp.user)
  }, [])

  const logout = useCallback(async () => {
    try {
      await authApi.logout()
    } catch {
      // 忽略登出 API 错误
    }
    localStorage.removeItem('access_token')
    localStorage.removeItem('refresh_token')
    // 静态访问令牌是设备级凭证：登出仅清 JWT，下次打开仍自动登录（要彻底退出需清浏览器数据或改配置）
    setUser(null)
  }, [])

  // 重新拉取当前用户信息（个人中心更新后刷新）
  const refreshUser = useCallback(async () => {
    const resp = await authApi.getMe()
    setUser(resp.data)
  }, [])

  const value: AuthContextValue = { user, loading, login, register, logout, refreshUser }

  if (loading) {
    return <LoadingSpinner />
  }

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

// useAuthContext 消费认证上下文（内部使用）
export function useAuthContext(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuthContext 必须在 AuthProvider 内使用')
  }
  return ctx
}
