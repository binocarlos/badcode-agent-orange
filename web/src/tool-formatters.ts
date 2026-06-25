/**
 * Tool display formatting utilities for agent tool call cards.
 * Generic formatters only — Platinum's `pt` CLI command parsing stays
 * in the Platinum render-plugin bundle (it's app-specific).
 * Copied + split from frontend/src/components/agent/tool-formatters.ts.
 * See ../../docs/90-provenance-map.md.
 */

// Display name overrides for specific tools
export const TOOL_DISPLAY_OVERRIDES: Record<string, string> = {
  list_customer_jobs: 'List Jobs',
  update_dashboard_layout: 'Update Layout',
  create_multi_table: 'Create Multi-Table',
  get_analysis_progress: 'Analysis Progress',
  ask_user: 'Ask User',
}

// --- PT CLI command mapping ---
// Kept here for generic use (summary + display name fallback via parsePtCommand)
// Platinum's tool-plugin bundle may override these with richer handling.

interface PtCommandDef {
  /** Subcommand pattern to match (e.g. "dashboard create", "query") */
  pattern: string
  displayName: string
  category: ToolCategory
  /** Build a one-line summary from the full command string */
  summary?: (fullCommand: string) => string
}

/** Extract a named flag value: --flag value or --flag=value */
function flagValue(cmd: string, flag: string): string | undefined {
  // --flag=value
  const eqRe = new RegExp(`--${flag}=(\\S+)`)
  const eqMatch = cmd.match(eqRe)
  if (eqMatch) return eqMatch[1]
  // --flag value
  const spRe = new RegExp(`--${flag}\\s+(\\S+)`)
  const spMatch = cmd.match(spRe)
  if (spMatch) return spMatch[1]
  return undefined
}

/** Extract the first positional arg after "pt <subcommand>" */
function positionalArg(cmd: string, afterPattern: string): string | undefined {
  const re = new RegExp(`pt\\s+${afterPattern}\\s+(?!-)(\\S+)`)
  const m = cmd.match(re)
  return m ? m[1] : undefined
}

/** Extract a quoted search term (first quoted string in command) */
function quotedTerm(cmd: string): string | undefined {
  const m = cmd.match(/["']([^"']+)["']/)
  return m ? m[1] : undefined
}

/**
 * Registry of pt CLI subcommands mapped to display metadata.
 * Multi-word patterns must come before single-word ones.
 * Longer single-word patterns (e.g. "tables") before shorter prefixes ("table").
 */
export const PT_COMMANDS: PtCommandDef[] = [
  // Multi-word patterns first
  { pattern: 'chats search', displayName: 'Search Team Conversations', category: 'data',
    summary: (cmd) => { const q = quotedTerm(cmd); return q ? `"${q}"` : '' } },
  { pattern: 'dashboard create', displayName: 'Create Dashboard', category: 'dashboard' },
  { pattern: 'dashboard layout', displayName: 'Update Layout', category: 'dashboard',
    summary: (cmd) => { const id = flagValue(cmd, 'id') || positionalArg(cmd, 'dashboard layout'); return id ? `ID: ${id}` : '' } },
  { pattern: 'dashboard multitable', displayName: 'Create Multi-Table', category: 'dashboard',
    summary: (cmd) => { const id = flagValue(cmd, 'id') || positionalArg(cmd, 'dashboard multitable'); return id ? `ID: ${id}` : '' } },
  { pattern: 'analysis start', displayName: 'Start Analysis', category: 'dashboard',
    summary: (cmd) => { const id = flagValue(cmd, 'id') || positionalArg(cmd, 'analysis start'); return id ? `ID: ${id}` : '' } },
  { pattern: 'analysis progress', displayName: 'Analysis Progress', category: 'dashboard',
    summary: (cmd) => { const id = flagValue(cmd, 'id') || positionalArg(cmd, 'analysis progress'); return id ? `ID: ${id}` : '' } },
  // Single-word patterns (longer first)
  { pattern: 'tables', displayName: 'List Tables', category: 'data' },
  { pattern: 'table', displayName: 'Preview Table', category: 'data',
    summary: (cmd) => { const t = positionalArg(cmd, 'table') || flagValue(cmd, 'name'); return t ? `Table: ${t}` : '' } },
  { pattern: 'query', displayName: 'Query Table', category: 'data',
    summary: (cmd) => {
      const parts: string[] = []
      const side = flagValue(cmd, 'side')
      const top = flagValue(cmd, 'top')
      if (side) parts.push(`Side: ${side}`)
      if (top) parts.push(`Top: ${top}`)
      return parts.join(', ')
    } },
  { pattern: 'vars', displayName: 'Get Variables', category: 'data',
    summary: (cmd) => { const v = positionalArg(cmd, 'vars'); return v ? `Variable: ${v}` : '' } },
  { pattern: 'jobs', displayName: 'List Jobs', category: 'data' },
  { pattern: 'search', displayName: 'Search Data', category: 'data',
    summary: (cmd) => { const q = quotedTerm(cmd); return q ? `"${q}"` : '' } },
]

export interface PtCommandMatch {
  def: PtCommandDef
  fullCommand: string
}

/**
 * Detect a `pt` CLI command inside a bash tool call and return display metadata.
 * Returns null if the tool is not bash or the command doesn't start with `pt `.
 */
export function parsePtCommand(
  toolName: string,
  input?: Record<string, unknown>,
): PtCommandMatch | null {
  if (toolName.toLowerCase() !== 'bash' || !input) return null
  let cmd = typeof input.command === 'string' ? input.command.trim() : ''
  // Strip leading KEY=VALUE env var assignments (e.g. "SESSION_CUSTOMER=foo SESSION_JOB=bar pt vars")
  cmd = cmd.replace(/^(?:[A-Za-z_][A-Za-z0-9_]*=\S+\s+)+/, '')
  if (!cmd.startsWith('pt ')) return null

  // Strip "pt " prefix for pattern matching
  const rest = cmd.slice(3)
  for (const def of PT_COMMANDS) {
    if (rest.startsWith(def.pattern + ' ') || rest === def.pattern) {
      return { def, fullCommand: cmd }
    }
  }
  return null
}

/**
 * Strip the mcp__<server>__ prefix from a tool name.
 * Returns the bare tool name (e.g., "query_table").
 * Server names may contain underscores, so we find the last `__` after `mcp__`.
 */
export function stripMcpPrefix(name: string): string {
  if (!name.startsWith('mcp__')) return name
  const rest = name.slice(5) // remove "mcp__"
  const lastDunder = rest.lastIndexOf('__')
  if (lastDunder === -1) return name
  return rest.slice(lastDunder + 2)
}

/**
 * Convert snake_case to Title Case.
 */
function snakeToTitleCase(name: string): string {
  return name
    .split('_')
    .map(word => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ')
}

/**
 * Format an MCP tool name for display.
 * 1. Strip mcp__<server>__ prefix
 * 2. Check override map
 * 3. Convert snake_case to Title Case
 */
export function formatMcpToolName(name: string): string {
  const bare = stripMcpPrefix(name)
  if (TOOL_DISPLAY_OVERRIDES[bare]) {
    return TOOL_DISPLAY_OVERRIDES[bare]
  }
  return snakeToTitleCase(bare)
}

// --- Script execution detection ---

export interface ScriptExecutionMatch {
  /** Runtime name: "python", "node", "Rscript", "bash", "ts-node" */
  runtime: string
  /** Just the filename, e.g. "analysis.py" */
  scriptFile: string
  /** User-facing name, e.g. "Run Python Script" */
  displayName: string
  /** Language label for badge, e.g. "Python" */
  language: string
  /** The full command string */
  fullCommand: string
}

const SCRIPT_PATTERNS: {
  regex: RegExp
  runtime: string
  displayName: string
  language: string
}[] = [
  { regex: /^python[3]?\s+(\S+\.py)\b/, runtime: 'python', displayName: 'Run Python Script', language: 'Python' },
  { regex: /^node\s+(\S+\.js)\b/, runtime: 'node', displayName: 'Run Node Script', language: 'JavaScript' },
  { regex: /^ts-node\s+(\S+\.ts)\b/, runtime: 'ts-node', displayName: 'Run TypeScript Script', language: 'TypeScript' },
  { regex: /^Rscript\s+(\S+\.R)\b/i, runtime: 'Rscript', displayName: 'Run R Script', language: 'R' },
  { regex: /^(?:bash|sh)\s+(\S+\.sh)\b/, runtime: 'bash', displayName: 'Run Shell Script', language: 'Shell' },
]

/**
 * Detect a script execution command inside a bash tool call.
 * Returns null if the tool is not bash or the command doesn't match a script pattern.
 */
export function parseScriptExecution(
  toolName: string,
  input?: Record<string, unknown>,
): ScriptExecutionMatch | null {
  if (toolName.toLowerCase() !== 'bash' || !input) return null
  let cmd = typeof input.command === 'string' ? input.command.trim() : ''
  if (!cmd) return null
  // Strip leading KEY=VALUE env var assignments
  cmd = cmd.replace(/^(?:[A-Za-z_][A-Za-z0-9_]*=\S+\s+)+/, '')

  for (const pat of SCRIPT_PATTERNS) {
    const m = cmd.match(pat.regex)
    if (m) {
      const fullPath = m[1]
      const scriptFile = fullPath.split('/').pop() || fullPath
      return {
        runtime: pat.runtime,
        scriptFile,
        displayName: pat.displayName,
        language: pat.language,
        fullCommand: cmd,
      }
    }
  }
  return null
}

// Tool categories
export type ToolCategory = 'data' | 'dashboard' | 'code' | 'file' | 'system' | 'script' | 'other'

const DATA_TOOLS = new Set([
  'query_table',
  'get_variables',
  'get_variable_codes',
  'search_job_data',
  'list_tables',
  'list_customer_jobs',
  'preview_table',
  'render_table',
  'render_chart',
])

const DASHBOARD_TOOLS = new Set([
  'create_dashboard',
  'update_dashboard_layout',
  'create_multi_table',
  'start_analysis',
  'get_analysis_progress',
])

const CODE_TOOLS = new Set(['bash'])

const FILE_TOOLS = new Set(['text_editor', 'read'])

const SYSTEM_TOOLS = new Set(['advance_phase', 'ask_user'])

/**
 * Get the category for a tool name. Strips MCP prefix first.
 * When input is provided, checks for pt CLI commands before falling through.
 */
export function getToolCategory(name: string, input?: Record<string, unknown>): ToolCategory {
  const pt = parsePtCommand(name, input)
  if (pt) return pt.def.category

  const script = parseScriptExecution(name, input)
  if (script) return 'script'

  const bare = stripMcpPrefix(name.toLowerCase())
  if (DATA_TOOLS.has(bare)) return 'data'
  if (DASHBOARD_TOOLS.has(bare)) return 'dashboard'
  if (CODE_TOOLS.has(bare)) return 'code'
  if (FILE_TOOLS.has(bare)) return 'file'
  if (SYSTEM_TOOLS.has(bare)) return 'system'
  return 'other'
}

const CATEGORY_ICONS: Record<ToolCategory, string> = {
  data: '🔍',      // magnifying glass
  dashboard: '📊',  // bar chart
  code: '💻',       // computer/terminal
  file: '📄',       // page facing up
  system: '⚙️',     // gear
  script: '▶️',     // play button
  other: '🔧',      // wrench
}

/**
 * Get the icon for a tool category.
 */
export function getToolIcon(category: ToolCategory): string {
  return CATEGORY_ICONS[category]
}

function truncate(s: string, max: number): string {
  return s.length > max ? s.slice(0, max) + '...' : s
}

type SummaryGenerator = (input: Record<string, unknown>) => string

const SUMMARY_GENERATORS: Record<string, SummaryGenerator> = {
  query_table: (input) => {
    const parts: string[] = []
    if (input.side_axis) parts.push(`Side: ${input.side_axis}`)
    if (input.top_axis) parts.push(`Top: ${input.top_axis}`)
    return parts.join(', ')
  },
  get_variables: (input) => `Job: ${input.job || 'current'}`,
  get_variable_codes: (input) => `Variable: ${input.variable_name || ''}`,
  search_job_data: (input) => `"${input.query || ''}"`,
  list_tables: (input) => `Job: ${input.job || 'current'}`,
  list_customer_jobs: (input) => `Customer: ${input.customer || ''}`,
  preview_table: (input) => `Table: ${input.table_name || ''}`,
  create_dashboard: (input) => `"${input.name || ''}"`,
  bash: (input) => truncate(String(input.command || ''), 60),
  advance_phase: (input) => truncate(String(input.summary || ''), 60),
  text_editor: (input) => {
    const cmd = input.command as string || ''
    const path = input.file_path as string || ''
    const fileName = path.split('/').pop() || path
    return cmd === 'create' ? `Created ${fileName}` : `${cmd} ${fileName}`
  },
  read: (input) => {
    const filePath = input.file_path as string || ''
    return filePath.split('/').pop() || filePath
  },
  render_table: (input) => `Table: ${input.title || 'Untitled'}`,
  render_chart: (input) => `Chart: ${input.title || 'Untitled'}`,
  ask_user: (input) => truncate(String(input.question || ''), 60),
}

/**
 * Generate a one-line summary from a tool's input params.
 * Checks for pt CLI commands first, then strips MCP prefix before lookup.
 */
export function getToolSummary(name: string, input: Record<string, unknown>): string {
  const pt = parsePtCommand(name, input)
  if (pt) return pt.def.summary ? pt.def.summary(pt.fullCommand) : ''

  const script = parseScriptExecution(name, input)
  if (script) return script.scriptFile

  const bare = stripMcpPrefix(name.toLowerCase())
  const gen = SUMMARY_GENERATORS[bare]
  return gen ? gen(input) : ''
}

/**
 * SDK tool names mapped to user-friendly display names.
 */
export const SDK_TOOL_DISPLAY_NAMES: Record<string, string> = {
  bash: 'Run Command',
  text_editor: 'Edit File',
}

/**
 * Get a user-friendly display name for a tool.
 * Maps SDK built-in tools to friendly names.
 * For MCP tools (mcp__<server>__<name>), strips prefix and converts to Title Case.
 * For text_editor, uses the command field to pick a more specific name.
 */
export function getToolDisplayName(toolName: string, toolInput?: Record<string, unknown>): string {
  const normalized = toolName.toLowerCase()
  if (normalized === 'text_editor' && toolInput) {
    const command = toolInput.command as string | undefined
    if (command === 'create') return 'Write File'
    if (command === 'view') return 'Read File'
    if (command === 'str_replace') return 'Edit File'
  }
  if (normalized === 'read' && toolInput) {
    const filePath = toolInput.file_path as string | undefined
    if (filePath && /\.(png|jpg|jpeg|gif|webp|bmp|ico|tiff?)$/i.test(filePath)) {
      return 'Show Image'
    }
  }
  // Check for pt CLI commands inside bash calls
  const pt = parsePtCommand(toolName, toolInput)
  if (pt) return pt.def.displayName

  // Check for script execution (python, node, etc.)
  const script = parseScriptExecution(toolName, toolInput)
  if (script) return script.displayName

  if (SDK_TOOL_DISPLAY_NAMES[normalized]) {
    return SDK_TOOL_DISPLAY_NAMES[normalized]
  }
  // Format MCP tool names and unknown tools
  return formatMcpToolName(toolName)
}
