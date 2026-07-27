import { Code2 } from 'lucide-react'
import type { ModeDef, PanelDef, LayoutNode } from './types'
import { leaf, split, tabs } from './types'
import { PANELS } from './panels'

export type { LayoutNode }

/** 面板注册表访问器（供 LayoutRenderer 查询） */
export function getPANELS(): PanelDef[] {
  return PANELS
}

/**
 * 模式注册表。任务类型已合并为单一统一模式（原编码/文档模式合一）：
 * 左侧 AI 对话 + 右侧标签组（文件/终端/变更/调试/浏览器/文档预览）。
 * 新增模式只需 push 一条 + 对应 i18n key，ChatPage 不需要改动。
 */
export const MODES: ModeDef[] = [
  {
    id: 'coding',
    titleKey: 'codingMode.chatEmptyTitle',
    icon: <Code2 size={14} />,
    layout: split('row', [
      // 左：AI 对话
      leaf('chat', 1),
      // 右：合并为单一标签组（文档预览并入其中）
      tabs(['files', 'terminal', 'changes', 'debug', 'browser', 'doc-preview'], 1.3, 'terminal'),
    ]),
  },
]

/** 按 id 取模式定义；未注册时回退到首个模式（避免白屏） */
export function getMode(id: string | null | undefined): ModeDef {
  return MODES.find((m) => m.id === id) || MODES[0]
}

export const DEFAULT_MODE_ID = MODES[0].id
