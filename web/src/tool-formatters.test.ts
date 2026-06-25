import { describe, it, expect } from 'vitest'
import {
  formatMcpToolName,
  getToolCategory,
  getToolIcon,
  getToolSummary,
  getToolDisplayName,
  parsePtCommand,
  parseScriptExecution,
  stripMcpPrefix,
  TOOL_DISPLAY_OVERRIDES,
  SDK_TOOL_DISPLAY_NAMES,
} from './tool-formatters.js'

// ==========================================
// formatMcpToolName
// ==========================================
describe('formatMcpToolName', () => {
  it('strips mcp__<server>__ prefix and converts to Title Case', () => {
    expect(formatMcpToolName('mcp__data__query_table')).toBe('Query Table')
  })

  it('handles mcp__dashboard__ prefix', () => {
    expect(formatMcpToolName('mcp__dashboard__create_dashboard')).toBe('Create Dashboard')
  })

  it('handles multi-word snake_case after prefix', () => {
    expect(formatMcpToolName('mcp__data__search_job_data')).toBe('Search Job Data')
  })

  it('converts plain snake_case (no prefix) to Title Case', () => {
    expect(formatMcpToolName('get_variables')).toBe('Get Variables')
  })

  it('returns single word capitalized', () => {
    expect(formatMcpToolName('bash')).toBe('Bash')
  })

  it('applies display override when available', () => {
    expect(formatMcpToolName('mcp__data__list_customer_jobs')).toBe('List Jobs')
    expect(formatMcpToolName('list_customer_jobs')).toBe('List Jobs')
  })

  it('applies override for update_dashboard_layout', () => {
    expect(formatMcpToolName('update_dashboard_layout')).toBe('Update Layout')
    expect(formatMcpToolName('mcp__data__update_dashboard_layout')).toBe('Update Layout')
  })

  it('handles deeply nested mcp prefix', () => {
    expect(formatMcpToolName('mcp__some_server__do_thing')).toBe('Do Thing')
  })
})

// ==========================================
// stripMcpPrefix
// ==========================================
describe('stripMcpPrefix', () => {
  it('strips mcp__ prefix with server name', () => {
    expect(stripMcpPrefix('mcp__data__query_table')).toBe('query_table')
  })

  it('returns name unchanged if no mcp__ prefix', () => {
    expect(stripMcpPrefix('query_table')).toBe('query_table')
    expect(stripMcpPrefix('bash')).toBe('bash')
  })

  it('handles server names containing underscores', () => {
    expect(stripMcpPrefix('mcp__my_server__do_thing')).toBe('do_thing')
  })
})

// ==========================================
// TOOL_DISPLAY_OVERRIDES
// ==========================================
describe('TOOL_DISPLAY_OVERRIDES', () => {
  it('has override for list_customer_jobs', () => {
    expect(TOOL_DISPLAY_OVERRIDES['list_customer_jobs']).toBe('List Jobs')
  })

  it('has override for update_dashboard_layout', () => {
    expect(TOOL_DISPLAY_OVERRIDES['update_dashboard_layout']).toBe('Update Layout')
  })

  it('has override for create_multi_table', () => {
    expect(TOOL_DISPLAY_OVERRIDES['create_multi_table']).toBe('Create Multi-Table')
  })

  it('has override for get_analysis_progress', () => {
    expect(TOOL_DISPLAY_OVERRIDES['get_analysis_progress']).toBe('Analysis Progress')
  })

  it('has override for ask_user', () => {
    expect(TOOL_DISPLAY_OVERRIDES['ask_user']).toBe('Ask User')
  })
})

// ==========================================
// getToolCategory
// ==========================================
describe('getToolCategory', () => {
  it('returns "data" for data query tools', () => {
    expect(getToolCategory('query_table')).toBe('data')
    expect(getToolCategory('get_variables')).toBe('data')
    expect(getToolCategory('get_variable_codes')).toBe('data')
    expect(getToolCategory('search_job_data')).toBe('data')
    expect(getToolCategory('list_tables')).toBe('data')
    expect(getToolCategory('list_customer_jobs')).toBe('data')
    expect(getToolCategory('preview_table')).toBe('data')
    expect(getToolCategory('render_table')).toBe('data')
    expect(getToolCategory('render_chart')).toBe('data')
  })

  it('returns "dashboard" for dashboard tools', () => {
    expect(getToolCategory('create_dashboard')).toBe('dashboard')
    expect(getToolCategory('update_dashboard_layout')).toBe('dashboard')
    expect(getToolCategory('create_multi_table')).toBe('dashboard')
    expect(getToolCategory('start_analysis')).toBe('dashboard')
    expect(getToolCategory('get_analysis_progress')).toBe('dashboard')
  })

  it('returns "code" for code execution tools', () => {
    expect(getToolCategory('bash')).toBe('code')
  })

  it('returns "file" for file tools', () => {
    expect(getToolCategory('text_editor')).toBe('file')
    expect(getToolCategory('read')).toBe('file')
  })

  it('returns "system" for advance_phase and ask_user', () => {
    expect(getToolCategory('advance_phase')).toBe('system')
    expect(getToolCategory('ask_user')).toBe('system')
  })

  it('returns "other" for unknown tools', () => {
    expect(getToolCategory('something_unknown')).toBe('other')
  })

  it('strips mcp prefix before categorizing', () => {
    expect(getToolCategory('mcp__data__query_table')).toBe('data')
    expect(getToolCategory('mcp__dashboard__create_dashboard')).toBe('dashboard')
  })
})

// ==========================================
// getToolIcon
// ==========================================
describe('getToolIcon', () => {
  it('returns search icon for data category', () => {
    expect(getToolIcon('data')).toBe('🔍') // magnifying glass
  })

  it('returns chart icon for dashboard category', () => {
    expect(getToolIcon('dashboard')).toBe('📊') // bar chart
  })

  it('returns terminal icon for code category', () => {
    expect(getToolIcon('code')).toBe('💻') // computer/terminal
  })

  it('returns file icon for file category', () => {
    expect(getToolIcon('file')).toBe('📄') // page facing up
  })

  it('returns gear icon for system category', () => {
    expect(getToolIcon('system')).toBe('⚙️') // gear
  })

  it('returns play button for script category', () => {
    expect(getToolIcon('script')).toBe('▶️') // play button
  })

  it('returns wrench icon for other category', () => {
    expect(getToolIcon('other')).toBe('🔧') // wrench
  })
})

// ==========================================
// getToolSummary
// ==========================================
describe('getToolSummary', () => {
  it('generates summary for query_table', () => {
    const input = { side_axis: 'Gender', top_axis: 'AgeGroups' }
    expect(getToolSummary('query_table', input)).toBe('Side: Gender, Top: AgeGroups')
  })

  it('generates summary for query_table with only side_axis', () => {
    const input = { side_axis: 'Gender' }
    expect(getToolSummary('query_table', input)).toBe('Side: Gender')
  })

  it('generates summary for get_variables', () => {
    const input = { job: 'wave1' }
    expect(getToolSummary('get_variables', input)).toBe('Job: wave1')
  })

  it('generates summary for get_variables with default', () => {
    const input = {}
    expect(getToolSummary('get_variables', input)).toBe('Job: current')
  })

  it('generates summary for search_job_data', () => {
    const input = { query: 'brand awareness' }
    expect(getToolSummary('search_job_data', input)).toBe('"brand awareness"')
  })

  it('generates summary for create_dashboard', () => {
    const input = { name: 'Q4 Report' }
    expect(getToolSummary('create_dashboard', input)).toBe('"Q4 Report"')
  })

  it('generates summary for get_variable_codes', () => {
    const input = { variable_name: 'Gender' }
    expect(getToolSummary('get_variable_codes', input)).toBe('Variable: Gender')
  })

  it('generates summary for list_tables', () => {
    const input = { job: 'wave1' }
    expect(getToolSummary('list_tables', input)).toBe('Job: wave1')
  })

  it('generates summary for list_customer_jobs', () => {
    const input = { customer: 'acme' }
    expect(getToolSummary('list_customer_jobs', input)).toBe('Customer: acme')
  })

  it('generates summary for bash', () => {
    const input = { command: 'ls -la /workspace' }
    expect(getToolSummary('bash', input)).toBe('ls -la /workspace')
  })

  it('truncates long bash commands', () => {
    const input = { command: 'a'.repeat(100) }
    const summary = getToolSummary('bash', input)
    expect(summary.length).toBeLessThanOrEqual(63) // 60 + "..."
  })

  it('generates summary for preview_table', () => {
    const input = { table_name: 'Demographics' }
    expect(getToolSummary('preview_table', input)).toBe('Table: Demographics')
  })

  it('generates summary for advance_phase', () => {
    const input = { summary: 'Collected 5 variables' }
    expect(getToolSummary('advance_phase', input)).toBe('Collected 5 variables')
  })

  it('truncates long advance_phase summaries', () => {
    const input = { summary: 'x'.repeat(100) }
    const summary = getToolSummary('advance_phase', input)
    expect(summary.length).toBeLessThanOrEqual(63)
  })

  it('generates summary for render_table', () => {
    const input = { title: 'Brand Tracker' }
    expect(getToolSummary('render_table', input)).toBe('Table: Brand Tracker')
  })

  it('generates summary for render_chart', () => {
    const input = { title: 'Trend Analysis' }
    expect(getToolSummary('render_chart', input)).toBe('Chart: Trend Analysis')
  })

  it('generates summary for ask_user', () => {
    const input = { question: 'What is your name?' }
    expect(getToolSummary('ask_user', input)).toBe('What is your name?')
  })

  it('generates summary for text_editor create', () => {
    const input = { command: 'create', file_path: '/workspace/report.py' }
    expect(getToolSummary('text_editor', input)).toBe('Created report.py')
  })

  it('generates summary for text_editor view', () => {
    const input = { command: 'view', file_path: '/workspace/report.py' }
    expect(getToolSummary('text_editor', input)).toBe('view report.py')
  })

  it('generates summary for read', () => {
    const input = { file_path: '/workspace/data.csv' }
    expect(getToolSummary('read', input)).toBe('data.csv')
  })

  it('returns empty string for unknown tools', () => {
    expect(getToolSummary('something_unknown', { foo: 'bar' })).toBe('')
  })

  it('strips mcp prefix before generating summary', () => {
    const input = { side_axis: 'Gender', top_axis: 'AgeGroups' }
    expect(getToolSummary('mcp__data__query_table', input)).toBe('Side: Gender, Top: AgeGroups')
  })
})

// ==========================================
// parsePtCommand
// ==========================================
describe('parsePtCommand', () => {
  it('returns null for non-bash tools', () => {
    expect(parsePtCommand('text_editor', { command: 'pt query' })).toBeNull()
    expect(parsePtCommand('mcp__data__query_table', { command: 'pt query' })).toBeNull()
  })

  it('returns null for bash without pt prefix', () => {
    expect(parsePtCommand('bash', { command: 'ls -la' })).toBeNull()
    expect(parsePtCommand('bash', { command: 'echo hello' })).toBeNull()
  })

  it('returns null when input is missing or command is not a string', () => {
    expect(parsePtCommand('bash')).toBeNull()
    expect(parsePtCommand('bash', {})).toBeNull()
    expect(parsePtCommand('bash', { command: 123 })).toBeNull()
  })

  it('matches pt query', () => {
    const result = parsePtCommand('bash', { command: 'pt query --side Age --top Gender' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Query Table')
    expect(result!.def.category).toBe('data')
  })

  it('matches pt vars', () => {
    const result = parsePtCommand('bash', { command: 'pt vars' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Get Variables')
  })

  it('matches pt vars with positional arg', () => {
    const result = parsePtCommand('bash', { command: 'pt vars Gender' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Get Variables')
  })

  it('matches pt jobs', () => {
    const result = parsePtCommand('bash', { command: 'pt jobs' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('List Jobs')
  })

  it('matches pt tables (not pt table)', () => {
    const result = parsePtCommand('bash', { command: 'pt tables' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('List Tables')
  })

  it('matches pt table (preview)', () => {
    const result = parsePtCommand('bash', { command: 'pt table Demographics' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Preview Table')
  })

  it('matches pt search', () => {
    const result = parsePtCommand('bash', { command: 'pt search "brand awareness"' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Search Data')
  })

  it('matches pt chats search', () => {
    const result = parsePtCommand('bash', { command: 'pt chats search "sentiment"' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Search Team Conversations')
  })

  it('matches pt dashboard create', () => {
    const result = parsePtCommand('bash', { command: 'pt dashboard create' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Create Dashboard')
    expect(result!.def.category).toBe('dashboard')
  })

  it('matches pt dashboard layout', () => {
    const result = parsePtCommand('bash', { command: 'pt dashboard layout --id abc123' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Update Layout')
  })

  it('matches pt dashboard multitable', () => {
    const result = parsePtCommand('bash', { command: 'pt dashboard multitable --id abc' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Create Multi-Table')
  })

  it('matches pt analysis start', () => {
    const result = parsePtCommand('bash', { command: 'pt analysis start --id d1' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Start Analysis')
    expect(result!.def.category).toBe('dashboard')
  })

  it('matches pt analysis progress', () => {
    const result = parsePtCommand('bash', { command: 'pt analysis progress --id d1' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Analysis Progress')
  })

  it('returns null for unknown pt subcommand', () => {
    expect(parsePtCommand('bash', { command: 'pt unknown-thing' })).toBeNull()
  })

  it('matches with capital-B "Bash" tool name (SDK casing)', () => {
    const result = parsePtCommand('Bash', { command: 'pt vars Gender' })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Get Variables')
  })

  it('strips env var prefixes before matching pt command', () => {
    const result = parsePtCommand('bash', {
      command: 'SESSION_CUSTOMER=bprtesting SESSION_JOB=tsapi-demo pt vars',
    })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Get Variables')
  })

  it('strips env var prefixes and still extracts summary', () => {
    const result = parsePtCommand('bash', {
      command: 'SESSION_CUSTOMER=bprtesting SESSION_JOB=tsapi-demo pt vars Gender',
    })
    expect(result).not.toBeNull()
    expect(result!.def.displayName).toBe('Get Variables')
    expect(result!.def.summary!(result!.fullCommand)).toBe('Variable: Gender')
  })
})

// ==========================================
// getToolCategory with pt commands
// ==========================================
describe('getToolCategory with pt commands', () => {
  it('returns "data" for pt query bash call', () => {
    expect(getToolCategory('bash', { command: 'pt query --side Age' })).toBe('data')
  })

  it('returns "data" for pt search bash call', () => {
    expect(getToolCategory('bash', { command: 'pt search "test"' })).toBe('data')
  })

  it('returns "dashboard" for pt dashboard create bash call', () => {
    expect(getToolCategory('bash', { command: 'pt dashboard create' })).toBe('dashboard')
  })

  it('returns "code" for non-pt bash call', () => {
    expect(getToolCategory('bash', { command: 'ls -la' })).toBe('code')
  })

  it('returns "code" for bash without input', () => {
    expect(getToolCategory('bash')).toBe('code')
  })

  it('handles capital-B "Bash" from SDK', () => {
    expect(getToolCategory('Bash')).toBe('code')
    expect(getToolCategory('Bash', { command: 'pt query --side Age' })).toBe('data')
  })
})

// ==========================================
// getToolSummary for pt commands
// ==========================================
describe('getToolSummary for pt commands', () => {
  it('generates summary for pt query with --side and --top', () => {
    expect(getToolSummary('bash', { command: 'pt query --side Age --top Gender' }))
      .toBe('Side: Age, Top: Gender')
  })

  it('generates summary for pt query with only --side', () => {
    expect(getToolSummary('bash', { command: 'pt query --side Age' }))
      .toBe('Side: Age')
  })

  it('generates summary for pt search with quoted term', () => {
    expect(getToolSummary('bash', { command: 'pt search "brand awareness"' }))
      .toBe('"brand awareness"')
  })

  it('generates summary for pt table with positional arg', () => {
    expect(getToolSummary('bash', { command: 'pt table Demographics' }))
      .toBe('Table: Demographics')
  })

  it('generates summary for pt vars with positional arg', () => {
    expect(getToolSummary('bash', { command: 'pt vars Gender' }))
      .toBe('Variable: Gender')
  })

  it('generates summary for pt chats search', () => {
    expect(getToolSummary('bash', { command: 'pt chats search "sentiment"' }))
      .toBe('"sentiment"')
  })

  it('generates summary for pt dashboard layout with --id', () => {
    expect(getToolSummary('bash', { command: 'pt dashboard layout --id abc123' }))
      .toBe('ID: abc123')
  })

  it('generates summary for pt analysis progress with --id=value', () => {
    expect(getToolSummary('bash', { command: 'pt analysis progress --id=d1' }))
      .toBe('ID: d1')
  })

  it('returns empty string for pt commands without summary generator', () => {
    expect(getToolSummary('bash', { command: 'pt jobs' })).toBe('')
  })

  it('falls back to truncated command for non-pt bash', () => {
    expect(getToolSummary('bash', { command: 'ls -la /workspace' }))
      .toBe('ls -la /workspace')
  })

  it('handles capital-B "Bash" from SDK', () => {
    expect(getToolSummary('Bash', { command: 'pt vars Gender' })).toBe('Variable: Gender')
    expect(getToolSummary('Bash', { command: 'ls -la' })).toBe('ls -la')
  })
})

// ==========================================
// parseScriptExecution
// ==========================================
describe('parseScriptExecution', () => {
  it('returns null for non-bash tools', () => {
    expect(parseScriptExecution('text_editor', { command: 'python test.py' })).toBeNull()
    expect(parseScriptExecution('mcp__data__query_table', { command: 'python test.py' })).toBeNull()
  })

  it('returns null for non-script bash commands', () => {
    expect(parseScriptExecution('bash', { command: 'ls -la' })).toBeNull()
    expect(parseScriptExecution('bash', { command: 'echo hello' })).toBeNull()
    expect(parseScriptExecution('bash', { command: 'pip install pandas' })).toBeNull()
  })

  it('returns null when input is missing or command is not a string', () => {
    expect(parseScriptExecution('bash')).toBeNull()
    expect(parseScriptExecution('bash', {})).toBeNull()
    expect(parseScriptExecution('bash', { command: 123 })).toBeNull()
  })

  it('matches python script execution', () => {
    const result = parseScriptExecution('bash', { command: 'python analysis.py' })
    expect(result).not.toBeNull()
    expect(result!.runtime).toBe('python')
    expect(result!.scriptFile).toBe('analysis.py')
    expect(result!.displayName).toBe('Run Python Script')
    expect(result!.language).toBe('Python')
  })

  it('matches python3 script execution', () => {
    const result = parseScriptExecution('bash', { command: 'python3 analysis.py' })
    expect(result).not.toBeNull()
    expect(result!.runtime).toBe('python')
    expect(result!.scriptFile).toBe('analysis.py')
  })

  it('matches python with full path', () => {
    const result = parseScriptExecution('bash', { command: 'python /workspace/scripts/analysis.py' })
    expect(result).not.toBeNull()
    expect(result!.scriptFile).toBe('analysis.py')
    expect(result!.fullCommand).toBe('python /workspace/scripts/analysis.py')
  })

  it('matches node script execution', () => {
    const result = parseScriptExecution('bash', { command: 'node script.js' })
    expect(result).not.toBeNull()
    expect(result!.runtime).toBe('node')
    expect(result!.scriptFile).toBe('script.js')
    expect(result!.displayName).toBe('Run Node Script')
    expect(result!.language).toBe('JavaScript')
  })

  it('matches Rscript execution', () => {
    const result = parseScriptExecution('bash', { command: 'Rscript report.R' })
    expect(result).not.toBeNull()
    expect(result!.runtime).toBe('Rscript')
    expect(result!.scriptFile).toBe('report.R')
    expect(result!.displayName).toBe('Run R Script')
    expect(result!.language).toBe('R')
  })

  it('matches bash/sh script execution', () => {
    const result = parseScriptExecution('bash', { command: 'bash setup.sh' })
    expect(result).not.toBeNull()
    expect(result!.runtime).toBe('bash')
    expect(result!.scriptFile).toBe('setup.sh')
    expect(result!.displayName).toBe('Run Shell Script')
    expect(result!.language).toBe('Shell')

    const result2 = parseScriptExecution('bash', { command: 'sh setup.sh' })
    expect(result2).not.toBeNull()
    expect(result2!.scriptFile).toBe('setup.sh')
  })

  it('matches ts-node script execution', () => {
    const result = parseScriptExecution('bash', { command: 'ts-node transform.ts' })
    expect(result).not.toBeNull()
    expect(result!.runtime).toBe('ts-node')
    expect(result!.scriptFile).toBe('transform.ts')
    expect(result!.displayName).toBe('Run TypeScript Script')
    expect(result!.language).toBe('TypeScript')
  })

  it('handles capital-B "Bash" tool name (SDK casing)', () => {
    const result = parseScriptExecution('Bash', { command: 'python analysis.py' })
    expect(result).not.toBeNull()
    expect(result!.scriptFile).toBe('analysis.py')
  })

  it('strips env var prefixes before matching', () => {
    const result = parseScriptExecution('bash', {
      command: 'PYTHONPATH=/workspace DATA_DIR=/data python analysis.py',
    })
    expect(result).not.toBeNull()
    expect(result!.scriptFile).toBe('analysis.py')
    expect(result!.runtime).toBe('python')
  })

  it('does not match pt commands as scripts', () => {
    expect(parseScriptExecution('bash', { command: 'pt query --side Age' })).toBeNull()
  })
})

// ==========================================
// getToolCategory with script execution
// ==========================================
describe('getToolCategory with script execution', () => {
  it('returns "script" for python script execution', () => {
    expect(getToolCategory('bash', { command: 'python analysis.py' })).toBe('script')
  })

  it('returns "script" for node script execution', () => {
    expect(getToolCategory('bash', { command: 'node script.js' })).toBe('script')
  })

  it('still returns "data" for pt commands (higher priority)', () => {
    expect(getToolCategory('bash', { command: 'pt query --side Age' })).toBe('data')
  })

  it('still returns "code" for generic bash commands', () => {
    expect(getToolCategory('bash', { command: 'ls -la' })).toBe('code')
  })
})

// ==========================================
// getToolSummary with script execution
// ==========================================
describe('getToolSummary with script execution', () => {
  it('returns filename for python script', () => {
    expect(getToolSummary('bash', { command: 'python analysis.py' })).toBe('analysis.py')
  })

  it('returns filename (not full path) for script with path', () => {
    expect(getToolSummary('bash', { command: 'python /workspace/scripts/analysis.py' })).toBe('analysis.py')
  })

  it('returns filename for node script', () => {
    expect(getToolSummary('bash', { command: 'node transform.js' })).toBe('transform.js')
  })

  it('still returns pt summary for pt commands (higher priority)', () => {
    expect(getToolSummary('bash', { command: 'pt vars Gender' })).toBe('Variable: Gender')
  })
})

// ==========================================
// SDK_TOOL_DISPLAY_NAMES
// ==========================================
describe('SDK_TOOL_DISPLAY_NAMES', () => {
  it('maps bash to "Run Command"', () => {
    expect(SDK_TOOL_DISPLAY_NAMES['bash']).toBe('Run Command')
  })

  it('maps text_editor to "Edit File"', () => {
    expect(SDK_TOOL_DISPLAY_NAMES['text_editor']).toBe('Edit File')
  })
})

// ==========================================
// getToolDisplayName
// ==========================================
describe('getToolDisplayName', () => {
  it('returns "Write File" for text_editor with create command', () => {
    expect(getToolDisplayName('text_editor', { command: 'create' })).toBe('Write File')
  })

  it('returns "Read File" for text_editor with view command', () => {
    expect(getToolDisplayName('text_editor', { command: 'view' })).toBe('Read File')
  })

  it('returns "Edit File" for text_editor with str_replace command', () => {
    expect(getToolDisplayName('text_editor', { command: 'str_replace' })).toBe('Edit File')
  })

  it('returns "Show Image" for read with image file path', () => {
    expect(getToolDisplayName('read', { file_path: '/workspace/chart.png' })).toBe('Show Image')
    expect(getToolDisplayName('read', { file_path: '/workspace/photo.jpg' })).toBe('Show Image')
  })

  it('returns pt command display name for pt bash calls', () => {
    expect(getToolDisplayName('bash', { command: 'pt query --side Age' })).toBe('Query Table')
    expect(getToolDisplayName('bash', { command: 'pt vars' })).toBe('Get Variables')
  })

  it('returns script display name for script bash calls', () => {
    expect(getToolDisplayName('bash', { command: 'python analysis.py' })).toBe('Run Python Script')
    expect(getToolDisplayName('bash', { command: 'node script.js' })).toBe('Run Node Script')
  })

  it('returns SDK display name for plain bash', () => {
    expect(getToolDisplayName('bash', { command: 'ls -la' })).toBe('Run Command')
  })

  it('formats MCP tool names', () => {
    expect(getToolDisplayName('mcp__data__query_table')).toBe('Query Table')
    expect(getToolDisplayName('mcp__dashboard__create_dashboard')).toBe('Create Dashboard')
  })

  it('applies display overrides via formatMcpToolName', () => {
    expect(getToolDisplayName('list_customer_jobs')).toBe('List Jobs')
    expect(getToolDisplayName('mcp__data__list_customer_jobs')).toBe('List Jobs')
  })
})
