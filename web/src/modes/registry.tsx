import { Code2, BookOpenText, Network } from 'lucide-react'
import type { ModeDef, PanelDef, LayoutNode } from './types'
import { leaf, split, tabs } from './types'
import { PANELS } from './panels'

export type { LayoutNode }

/** 面板注册表访问器（供 LayoutRenderer 查询） */
export function getPANELS(): PanelDef[] {
  return PANELS
}

/**
 * 模式注册表。新增一个模式只需 push 一条 + 对应 i18n key。
 * ChatPage 不需要改动——TaskModeSwitch 也从 MODES 自动读取选项。
 *
 * 示例：未来加"调试模式"
 *   MODES.push({
 *     id: 'debug', titleKey: 'taskMode.debug', icon: <Bug/>,
 *     sessionKind: 'primary', configBar: 'coding',
 *     layout: split('row', [
 *       leaf('chat', 1),
 *       split('col', [leaf('terminal',1), leaf('debug',1)]),
 *     ]),
 *   })
 */
export const MODES: ModeDef[] = [
  {
    id: 'coding',
    titleKey: 'taskMode.coding',
    icon: <Code2 size={14} />,
    sessionKind: 'primary',
    configBar: 'coding',
    layout: split('row', [
      // 左：AI 对话
      leaf('chat', 1),
      // 右：合并为单一标签组
      tabs(['files', 'terminal', 'changes', 'debug', 'browser'], 1.3, 'terminal'),
    ]),
  },
  {
    id: 'docs',
    titleKey: 'taskMode.docs',
    icon: <BookOpenText size={14} />,
    sessionKind: 'docs',
    configBar: 'docs',
    requiresDocTarget: true,
    layout: split('row', [
      // 左：AI 对话（与编码模式一致，对话统一靠左）
      leaf('chat', 1),
      // 右：文档预览/编辑（含 drawio 渲染）
      leaf('doc-preview', 1.3),
    ]),
  },
  {
    // 任务管理模式：layout 仅占位，实际由 ChatPage 拦截渲染 TaskManagerView（不走 LayoutRenderer）。
    // 不是任务类型：不出现在 TaskModeSwitch；唯一入口为侧边栏「任务管理」。
    id: 'taskmanager',
    titleKey: 'taskMode.taskmanager',
    icon: <Network size={14} />,
    sessionKind: 'primary',
    configBar: 'none',
    layout: leaf('chat'),
  },
]

/** 按 id 取模式定义；未注册时回退到首个模式（避免白屏） */
export function getMode(id: string | null | undefined): ModeDef {
  return MODES.find((m) => m.id === id) || MODES[0]
}

export const DEFAULT_MODE_ID = MODES[0].id
