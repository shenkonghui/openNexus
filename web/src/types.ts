// 用户信息
export interface User {
  id: number;
  username: string;
  email: string;
  role: string;
  status: string;
}

// Agent 描述
export interface Agent {
  type: string;
  display_name: string;
  description: string;
}

// Agent 连接状态（侧边栏展示用）
export interface AgentStatus {
  agent_type: string;
  status: 'connected' | 'connecting' | 'disconnected';
  active_count: number;
}

// Agent 配置（设置页面管理的本地 ACP agent）
export interface AgentConfig {
  id: number;
  type: string;
  display_name: string;
  description: string;
  command: string;
  args: string[];
  env: Record<string, string>;
  api_key_env: string;
  timeout: string;
  enabled: boolean;
}

// Slash command（ACP available command）
export interface AgentCommand {
  name: string;
  description: string;
  has_input: boolean;
  path?: string;
  scope?: string;
  kind?: 'command' | 'agent';
}

// Config option 可选项值
export interface ConfigOptionValue {
  value: string;
  name: string;
  description: string;
}

// Config option（含模型选择等）
export interface ConfigOption {
  id: string;
  name: string;
  category: string; // "model" | "mode" | "thought_level" | ...
  type: string; // "select" | "boolean"
  current_value: string;
  options: ConfigOptionValue[];
}

// Session mode（ACP 协议的会话模式，如 plan/act）
export interface SessionMode {
  id: string;
  name: string;
  description: string;
}

// 权限请求选项（ACP RequestPermission）
export interface PermissionOption {
  optionId: string;
  name: string;
  kind: 'allow_once' | 'allow_always' | 'reject_once' | 'reject_always' | string;
}

export interface PermissionRequestPayload {
  request_id: string;
  tool_call?: { title?: string; toolCallId?: string };
  options: PermissionOption[];
}

// Agent Skill（agentskills.io 规范，SKILL.md 格式）
export interface AgentSkill {
  name: string;
  description: string;
  location: string;
  scope: string; // "project" | "user"
  path?: string;
}

// 工作区
export interface Workspace {
  id: number;
  user_id: number;
  name: string;
  cwd: string;
  directories: string[];
  mode: 'persistent' | 'temporary';
  temp_dir?: string;
  session_count?: number;
  created_at: string;
  updated_at: string;
}

// 会话
export interface Session {
  id: number;
  session_id: string;
  /** ACP agent 返回的 sessionId；pending 时为空 */
  agent_session_id?: string;
  agent_type: string;
  status: 'active' | 'closed' | 'error' | 'pending';
  user_id: number;
  workspace_id: number | null;
  workspace?: Workspace;
  last_prompt: string;
  title: string;
  source: 'manual' | 'scheduled' | 'classify' | 'orchestration';
  /** 父会话主键；编排管理会话为 null，编排子任务会话指向管理会话 */
  parent_session_id?: number | null;
  created_at: string;
  closed_at: string | null;
  // 标签 JSON 数组字符串，如 '["后端","mysql"]'，由任务自动分类写入
  tags?: string;
  /** 本任务是否开启 YOLO（未命中全局黑/白/询问名单时自动放行） */
  yolo?: boolean;
}

// 消息
export interface Message {
  id: number;
  session_id: string;
  role: 'user' | 'assistant' | 'tool';
  kind: string;
  content: string;
  raw_json: string;
  sequence: number;
  execution_id: number | null;
  created_at: string;
}

// 定时任务
export interface ScheduledTask {
  id: number;
  name: string;
  agent_type: string;
  workspace_id?: number;
  cwd: string;
  prompt: string;
  cron_expr: string;
  enabled: boolean;
  user_id: number;
  timeout_minutes: number;
  model_value: string;
  session_id: string;
  db_session_id: number;
  last_run_at: string | null;
  last_status: 'success' | 'running' | 'failed' | 'skipped' | '';
  last_error: string;
  created_at: string;
  updated_at: string;
}

// Agent 模型 config option（用于定时任务模型选择）
export interface ModelOption {
  id: string;
  name: string;
  current_value: string;
  options: ConfigOptionValue[];
}

// 定时任务执行块聚合
export interface Execution {
  execution_id: number;
  started_at: string;
  finished_at: string;
  message_count: number;
  status: 'success' | 'running' | 'failed' | 'skipped' | '';
  error: string;
}

// 进行中任务（用于服务重启后的中断恢复）
export interface RunningTask {
  id: number;
  db_session_id: number;
  user_id: number;
  prompt: string;
  status: 'running' | 'interrupted' | 'done';
  last_seq: number;
  execution_id: number | null;
  started_at: string;
  finished_at: string | null;
}

// 认证响应
export interface AuthResponse {
  access_token: string;
  refresh_token: string;
  user: User;
}

// API 统一错误响应
export interface ApiError {
  error: {
    code: string;
    message: string;
  };
}

// 文档文件夹绑定（侧边栏文档分组用）：绑定一个目录，自动列出其下 .md 文档。
// 绑定信息存 localStorage，文档正文仍实时从磁盘读取。
export interface DocFolder {
  id: string          // 前端 UUID，用于路由定位
  name: string        // 文件夹显示名（取自目录名，或相对 cwd 的路径）
  path: string        // 绝对路径（扫描根）
  workspaceId: number // 所属工作区（按工作区隔离绑定）
}

// 文档文件夹扫描出的单个 .md 文件（后端 GET /filesystem/docs 返回）
export interface DocFileEntry {
  name: string     // 文件名（如 foo.md）
  rel_path: string // 相对扫描根目录的路径（如 sub/foo.md），用于侧边栏展示与路由
  abs_path: string // 绝对路径，用于读取内容
}

// 全局笔记
export interface Note {
  id: number;
  title: string;
  content: string;
  tags: string[];
  classify_pending: boolean;
  created_at: string;
  updated_at: string;
}

export interface NoteSettings {
  agent_type: string;
  model_value: string;
  classify_prompt: string;
  classify_interval_minutes: number;
  classify_session_id?: string;
  classify_db_session_id?: number;
  mcp_token?: string;
}

// 用户 agent 最近使用偏好（按 agent type → category → value）
export interface AgentPrefs {
  last_agent_type: string;
  prefs: Record<string, Record<string, string>>;
}

export interface AgentPrefsPatch {
  last_agent_type?: string;
  agent_type?: string;
  configs?: Record<string, string>;
}

// 任务设置：自动打标签 + AI 标题生成
export interface TaskSettings {
  auto_tag_enabled: boolean;
  auto_title_enabled: boolean;
  agent_type: string;
  model_value: string;
  // 预定义标签列表
  tags: string[];
  tag_prompt: string;
  title_prompt: string;
}

// 全局权限规则配置（白名单 / 询问名单 / 黑名单，对所有会话生效）。
// mode=yolo 为全局 YOLO（侧栏左下角开关）；会话级 yolo 仍可单独开启。
// 规则按 agent 上报的工具调用标题匹配，支持 `*` 通配符（如 "Bash(git status *)"）。
// 优先级：deny > allow > ask > (全局/会话 yolo→allow | 询问)。
export interface PermissionSettings {
  mode: 'normal' | 'yolo';
  allow: string[];
  ask: string[];
  deny: string[];
}

// ===== 日志查看器 =====

// 日志等级，与后端 models.LogEntry 的 level 字段一致
export type LogLevel = 'debug' | 'info' | 'warn' | 'error';

// 一条日志条目（前端运行时日志与后端推送日志共用此结构）
export interface LogEntry {
  // seq 单调递增序号，用于去重；前端日志和后端日志各自独立计数
  seq: number;
  // timestamp ISO 时间字符串
  timestamp: string;
  level: LogLevel;
  // source 日志来源，前端为模块名（api/sse/runtime 等），后端为 slog 解析出的包.函数名
  source: string;
  message: string;
}
