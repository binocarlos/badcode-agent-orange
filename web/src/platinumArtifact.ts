// Detection helpers for Platinum table/chart JSON artifacts.
// Extracted from ArtifactViewer.tsx so the inline ArtifactPreviewDialog can apply
// the exact same rules and render the rich Platinum component instead of raw JSON.

/** Data parsed from a Platinum JSON artifact — passed to renderPlatinumData. */
export interface PlatinumArtifactData {
  platinumData: unknown
  title?: string
  customer?: string
  job?: string
  spec?: string
  viewMode?: 'table' | 'graph'
  chartType?: string
}

/** Try to parse JSON text as a Platinum table or chart artifact.
 *  Detects by:
 *  1. Wrapped format: JSON with __render_table/__render_chart markers (from tool_result messages)
 *  2. Filename pattern: table_*.json / chart_*.json files contain raw PlatinumData
 */
export function tryParsePlatinumJson(text: string, fileName: string): PlatinumArtifactData | null {
  try {
    const obj = JSON.parse(text) as Record<string, unknown>

    // Wrapped format with explicit markers
    if (obj.__render_table && obj.platinumData) {
      return {
        platinumData: obj.platinumData,
        title: obj.title as string | undefined,
        customer: obj.customer as string | undefined,
        job: obj.job as string | undefined,
        spec: obj.spec as string | undefined,
        viewMode: 'table',
      }
    }
    if (obj.__render_chart && obj.platinumData) {
      return {
        platinumData: obj.platinumData,
        title: obj.title as string | undefined,
        customer: obj.customer as string | undefined,
        job: obj.job as string | undefined,
        spec: obj.spec as string | undefined,
        viewMode: 'graph',
        chartType: obj.chartType as string | undefined,
      }
    }

    // Raw PlatinumData detected by filename pattern (table_*.json / chart_*.json)
    const baseName = fileName.replace(/\.json$/, '')
    if (/^table_/.test(baseName)) {
      const title = baseName.replace(/^table_/, '').replace(/_/g, ' ')
      return { platinumData: obj, title, viewMode: 'table' }
    }
    if (/^chart_/.test(baseName)) {
      const title = baseName.replace(/^chart_/, '').replace(/_/g, ' ')
      return { platinumData: obj, title, viewMode: 'graph', chartType: 'bar' }
    }
  } catch {
    // Not valid JSON or not a Platinum artifact
  }
  return null
}
