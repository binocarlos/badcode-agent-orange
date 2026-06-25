// Map file extensions to Prism language identifiers for syntax highlighting.
// Copied from frontend/src/utils/prismLanguage.ts.
// See ../../docs/90-provenance-map.md.

const EXT_TO_PRISM: Record<string, string> = {
  py: 'python',
  js: 'javascript',
  jsx: 'jsx',
  ts: 'typescript',
  tsx: 'tsx',
  sql: 'sql',
  sh: 'bash',
  bash: 'bash',
  r: 'r',
  rb: 'ruby',
  go: 'go',
  rs: 'rust',
  java: 'java',
  cs: 'csharp',
  css: 'css',
  html: 'markup',
  xml: 'markup',
  json: 'json',
  yaml: 'yaml',
  yml: 'yaml',
  md: 'markdown',
}

export function getPrismLanguage(fileName: string): string {
  const ext = fileName.split('.').pop()?.toLowerCase() || ''
  return EXT_TO_PRISM[ext] || 'plain'
}
