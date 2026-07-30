import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import styles from './ConvStatusBar.module.css'

export type ConvState = 'idle' | 'connecting' | 'streaming' | 'reconnecting' | 'waiting_permission'

interface ConvStatusBarProps {
  state: ConvState
  // 断线重连倒计时（后端推真实退避计划）：reconnecting 态且 seconds>0 时显示倒计时文案
  reconnect?: { seconds: number; attempt: number } | null
  children?: ReactNode
}

const stateKeys: Record<Exclude<ConvState, 'idle'>, string> = {
  connecting: 'session.conv_connecting',
  streaming: 'session.conv_streaming',
  reconnecting: 'session.conv_reconnecting',
  waiting_permission: 'session.conv_waiting_permission',
}

export default function ConvStatusBar({ state, reconnect, children }: ConvStatusBarProps) {
  const { t } = useTranslation()
  if (state === 'idle') return null

  // 重连倒计时：有后端退避计划时展示“N 秒后自动重连（第 M 次）”，无数据回退通用文案
  const text = state === 'reconnecting' && reconnect && reconnect.seconds > 0
    ? t('session.conv_reconnect_countdown', { seconds: reconnect.seconds, attempt: reconnect.attempt })
    : t(stateKeys[state])

  return (
    <div className={`${styles.bar} ${styles[`bar_${state}`]}`} role="status" aria-live="polite">
      <span className={styles.spinner} aria-hidden="true" />
      <span className={styles.text}>{text}</span>
      {children && <div className={styles.content}>{children}</div>}
    </div>
  )
}
