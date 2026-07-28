// diff 解析与行级差异工具。
// ACP tool_call / tool_call_update 消息的 raw_json 是扁平化的 SessionUpdate JSON，
// 其中 content[] 可能包含 {"type":"diff","path":...,"oldText":...,"newText":...}。

import type { Message } from '../types'

// 单个文件的 diff 数据
export interface FileDiff {
  path: string // diff 里原始路径（可能是绝对路径）
  oldText: string | null // null 表示新文件
  newText: string
}

// 行级 diff 的单行
export interface DiffLine {
  type: 'add' | 'del' | 'ctx'
  oldNo: number | null // 旧文件行号（del/ctx 有值）
  newNo: number | null // 新文件行号（add/ctx 有值）
  text: string
}

// 行级 diff 统计
export interface DiffStats {
  added: number
  removed: number
}

// 聚合后的文件改动项（按文件路径去重，保留最新一次 diff）
export interface FileChangeItem {
  path: string // 原始路径
  relPath: string // 相对 cwd
  diff: FileDiff // 最新一次 diff
  added: number
  removed: number
  isNew: boolean
}

// 行数超过该阈值时退化为"全量替换"展示，避免 LCS 性能问题
const MAX_LINES_FOR_LCS = 2000

// parseDiffsFromMessage 从消息的 raw_json 中提取所有 FileDiff。
// 仅处理 tool_call / tool_call_update 两种 kind。raw_json 兼容多行 NDJSON（合并消息）。
export function parseDiffsFromMessage(msg: Message): FileDiff[] {
  if (msg.kind !== 'tool_call' && msg.kind !== 'tool_call_update') return []
  if (!msg.raw_json) return []
  const diffs: FileDiff[] = []
  for (const part of msg.raw_json.split('\n')) {
    const line = part.trim()
    if (!line) continue
    let parsed: any
    try {
      parsed = JSON.parse(line)
    } catch {
      continue
    }
    const content = parsed?.content
    if (!Array.isArray(content)) continue
    for (const item of content) {
      if (item && item.type === 'diff' && typeof item.path === 'string') {
        diffs.push({
          path: item.path,
          oldText: typeof item.oldText === 'string' ? item.oldText : null,
          newText: typeof item.newText === 'string' ? item.newText : '',
        })
      }
    }
  }
  return diffs
}

// computeLineStats 基于行级 diff 计算 +N -M。
export function computeLineStats(oldText: string | null, newText: string): DiffStats {
  const lines = diffLines(oldText, newText)
  let added = 0
  let removed = 0
  for (const l of lines) {
    if (l.type === 'add') added++
    else if (l.type === 'del') removed++
  }
  return { added, removed }
}

// diffLines 对 oldText / newText 做逐行 LCS diff，返回 DiffLine[]。
// oldText 为 null 时视为新文件，全部行标记为 add。
// 任一端行数超过 MAX_LINES_FOR_LCS 时退化为简单拼接（旧全删 + 新全增）。
export function diffLines(oldText: string | null, newText: string): DiffLine[] {
  const oldLines = oldText == null ? [] : splitLines(oldText)
  const newLines = splitLines(newText)

  // 新文件
  if (oldText == null) {
    return newLines.map((text, i) => ({ type: 'add', oldNo: null, newNo: i + 1, text }))
  }

  // 性能保护：超大文本退化
  if (oldLines.length > MAX_LINES_FOR_LCS || newLines.length > MAX_LINES_FOR_LCS) {
    const result: DiffLine[] = oldLines.map((text, i) => ({
      type: 'del',
      oldNo: i + 1,
      newNo: null,
      text,
    }))
    newLines.forEach((text, i) =>
      result.push({ type: 'add', oldNo: null, newNo: i + 1, text }),
    )
    return result
  }

  return lcsDiff(oldLines, newLines)
}

// splitLines 按行切分，保留空行，去掉末尾换行符。保留尾部空行（若以 \n 结尾）。
function splitLines(text: string): string[] {
  if (text === '') return []
  const parts = text.split('\n')
  // 末尾若为 \n，split 会产生一个空串；去掉它以避免多出一行
  if (parts.length > 0 && parts[parts.length - 1] === '' && text.endsWith('\n')) {
    parts.pop()
  }
  return parts
}

// lcsDiff 基于动态规划 LCS 的逐行 diff。
function lcsDiff(a: string[], b: string[]): DiffLine[] {
  const n = a.length
  const m = b.length
  // dp[i][j] = a[0..i) 与 b[0..j) 的 LCS 长度
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      if (a[i] === b[j]) dp[i][j] = dp[i + 1][j + 1] + 1
      else dp[i][j] = Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }
  // 回溯生成 diff
  const result: DiffLine[] = []
  let i = 0
  let j = 0
  let oldNo = 0
  let newNo = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      oldNo++
      newNo++
      result.push({ type: 'ctx', oldNo, newNo, text: a[i] })
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      oldNo++
      result.push({ type: 'del', oldNo, newNo: null, text: a[i] })
      i++
    } else {
      newNo++
      result.push({ type: 'add', oldNo: null, newNo, text: b[j] })
      j++
    }
  }
  while (i < n) {
    oldNo++
    result.push({ type: 'del', oldNo, newNo: null, text: a[i] })
    i++
  }
  while (j < m) {
    newNo++
    result.push({ type: 'add', oldNo: null, newNo, text: b[j] })
    j++
  }
  return result
}

// toRelativePath 将 diff 中的路径（可能绝对）转为相对 session cwd 的路径。
// 若已经在 cwd 之下则去掉前缀；否则原样返回（调用方自行处理）。
export function toRelativePath(path: string, cwd: string): string {
  if (!cwd) return path
  // 统一用 / 比较
  const norm = (p: string) => p.replace(/\\/g, '/').replace(/\/+$/, '')
  const c = norm(cwd)
  const p = norm(path)
  if (p === c) return '.'
  if (p.startsWith(c + '/')) return p.slice(c.length + 1)
  // 可能是相对路径，直接返回
  return path
}

// shortPath 取路径末尾若干段用于紧凑展示。
export function shortPath(path: string, maxSegments = 2): string {
  const parts = path.replace(/\\/g, '/').split('/').filter(Boolean)
  if (parts.length <= maxSegments) return path
  return '.../' + parts.slice(-maxSegments).join('/')
}

// aggregateChanges 遍历所有消息，按归一化后的相对路径去重，保留最新一次 diff。
export function aggregateChanges(messages: Message[], cwd: string): FileChangeItem[] {
  const map = new Map<string, FileChangeItem>()
  for (const msg of messages) {
    const diffs = parseDiffsFromMessage(msg)
    for (const d of diffs) {
      const relPath = toRelativePath(d.path, cwd)
      const stats = computeLineStats(d.oldText, d.newText)
      map.set(relPath, {
        path: relPath,
        relPath,
        diff: d,
        added: stats.added,
        removed: stats.removed,
        isNew: d.oldText == null,
      })
    }
  }
  return Array.from(map.values())
}
