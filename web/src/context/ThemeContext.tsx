import { createContext, useContext, useState, useEffect, useCallback, type ReactNode } from 'react'

type Theme = 'dark' | 'light'
type FontSize = 'small' | 'medium' | 'large'

// 字体大小对应的像素值
const FONT_SIZE_MAP: Record<FontSize, string> = {
  small: '13px',
  medium: '14px',
  large: '16px',
}

interface ThemeContextValue {
  theme: Theme
  toggleTheme: () => void
  setTheme: (t: Theme) => void
  fontSize: FontSize
  setFontSize: (size: FontSize) => void
}

const ThemeContext = createContext<ThemeContextValue | null>(null)

// 读取初始主题：localStorage > 默认浅色（白色背景）
function getInitialTheme(): Theme {
  const stored = localStorage.getItem('theme')
  if (stored === 'dark' || stored === 'light') return stored
  return 'light'
}

// 读取初始字体大小：localStorage > 默认 medium
function getInitialFontSize(): FontSize {
  const stored = localStorage.getItem('font-size')
  if (stored === 'small' || stored === 'medium' || stored === 'large') return stored
  return 'medium'
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(getInitialTheme)
  const [fontSize, setFontSizeState] = useState<FontSize>(getInitialFontSize)

  // 同步主题到 document data-theme 属性与 localStorage
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    localStorage.setItem('theme', theme)
  }, [theme])

  // 同步字体大小到 CSS 变量与 localStorage
  useEffect(() => {
    document.documentElement.style.setProperty('--font-size-base', FONT_SIZE_MAP[fontSize])
    localStorage.setItem('font-size', fontSize)
  }, [fontSize])

  const setTheme = useCallback((t: Theme) => setThemeState(t), [])
  const toggleTheme = useCallback(
    () => setThemeState((prev) => (prev === 'dark' ? 'light' : 'dark')),
    [],
  )
  const setFontSize = useCallback((size: FontSize) => setFontSizeState(size), [])

  return (
    <ThemeContext.Provider value={{ theme, toggleTheme, setTheme, fontSize, setFontSize }}>
      {children}
    </ThemeContext.Provider>
  )
}

export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext)
  if (!ctx) {
    throw new Error('useTheme 必须在 ThemeProvider 内使用')
  }
  return ctx
}

export type { Theme, FontSize }
