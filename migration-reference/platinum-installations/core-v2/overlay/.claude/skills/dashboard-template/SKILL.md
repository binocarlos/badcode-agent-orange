---
name: dashboard-template
description: Build interactive dashboards using the pre-built component library instead of writing D3/Chart.js from scratch
triggers:
  - dashboard
  - web app
  - visualization dashboard
  - interactive report
---

# Dashboard Template

Use the pre-built component library at `./lib/components/` to build dashboards quickly. Instead of writing 200+ lines of D3/Chart.js code, import components and configure them in ~30 lines.

> **No build step.** Edit `/workspace/webapp/app.js` and register `webapp/index.html`. There is no Vite/npm. Libraries load via the import map in `index.html`. The data-loader is **async** — `await` it.

> **Mandatory companion skills.** A dashboard webapp is not shippable without:
> - `table-provenance` — every dataset must have all five spec components (top, side, filter, weight, caseFilter), default caseFilter = `side_variable(*)` ("all answering"), and the user must sign off on `data/spec-summary.md` BEFORE you build.
> - `data-visualization` — for the Data Reference Document workflow (step 5) and the logo / image handling rules (step 6). Both apply to every dashboard you ship.
>
> If you have not run those workflows, the dashboard is not ready to register.

## When to Use This Template

**Use this template for:**
- Brand health dashboards
- Tracking/wave comparison reports
- KPI dashboards with multiple chart types
- Any standard chart layout (bar, line, pie, radar, heatmap, table)

**Use raw D3/Chart.js instead for:**
- Geographic maps
- 3D visualizations
- Novel/custom interaction patterns
- Chord diagrams, Sankey flows, force-directed graphs

## Complete Example

This is what your entire `webapp/app.js` should look like. Note the `async main()`
wrapper and `await` on each data call — the data-loader fetches at runtime.

```javascript
import { hero, kpiCards, chartSection, sectionNav, barChart, lineChart, pieChart, dataTable, footer, applyTheme } from './lib/components/index.js'
import { getRecords, getMatrix, getDatasetMeta } from './lib/data-loader.js'

async function main() {
// 1. Apply brand theme (check system prompt for ## Brand Themes, or use pt brand-themes)
applyTheme({ primary: '#0072CE', accent: '#FF6B00', bg: '#0f172a' })

// 2. Get data (await — async loader)
const awareness = await getRecords('brand_awareness')
const consideration = await getRecords('consideration')
const nps = await getRecords('nps_scores')

// 3. Build dashboard
const app = document.getElementById('app')
app.innerHTML = ''

hero(app, {
  title: 'Brand Health Dashboard',
  subtitle: 'Sky UK — Q1 2026',
  date: 'March 2026'
})

sectionNav(app, { sections: [
  { id: 'kpis', label: 'Key Metrics' },
  { id: 'awareness', label: 'Awareness' },
  { id: 'consideration', label: 'Consideration' },
] })

const kpiSection = document.createElement('section')
kpiSection.id = 'kpis'
app.appendChild(kpiSection)
kpiCards(kpiSection, { cards: [
  { label: 'Aided Awareness', value: '78%', change: '+3pp' },
  { label: 'Consideration', value: '45%', change: '-1pp' },
  { label: 'NPS', value: '+32', change: '+5' },
] })

const awarenessSection = document.createElement('section')
awarenessSection.id = 'awareness'
app.appendChild(awarenessSection)
chartSection(awarenessSection, {
  title: 'Aided Brand Awareness',
  chart: (el) => barChart(el, {
    records: awareness,
    horizontal: true,
    palette: { 'Sky': '#0072CE', 'BT': '#6400AA', 'Virgin': '#C8102E' }
  }),
  narrative: 'Sky leads aided awareness at 78%, 12pp ahead of the nearest competitor. This represents a 3pp increase from Q4 2025.'
})

const considSection = document.createElement('section')
considSection.id = 'consideration'
app.appendChild(considSection)
chartSection(considSection, {
  title: 'Brand Consideration Over Time',
  chart: (el) => lineChart(el, {
    records: consideration,
    palette: { 'Sky': '#0072CE', 'BT': '#6400AA' }
  }),
  narrative: 'Consideration has remained stable at 45% over the past three waves, with BT showing a slight upward trend.'
})

footer(app, {
  sources: ['Source: Brand Tracker Q1 2026, n=2,500'],
  methodology: 'Online panel, nationally representative, weighted by age/gender/region'
})
}

main()
```

## Available Components

### Charts
| Import | Usage | Data Format |
|--------|-------|-------------|
| `barChart` | `barChart(el, { records, title, palette, horizontal?, stacked?, metric? })` | records: `[{row, col, value}]` |
| `lineChart` | `lineChart(el, { records, title, palette, area? })` | records: `[{row, col, value}]` |
| `pieChart` | `pieChart(el, { records, title, palette, donut? })` | records: `[{row, col, value}]` |
| `radarChart` | `radarChart(el, { matrix, title, palette })` | matrix: `{rows, columns, values}` |
| `heatmap` | `heatmap(el, { matrix, title, colorScale? })` | matrix: `{rows, columns, values}` |
| `dataTable` | `dataTable(el, { records, title, highlightMax? })` | records: `[{row, col, value}]` |

### Layout
| Import | Usage |
|--------|-------|
| `hero` | `hero(el, { title, subtitle, date?, logo? })` |
| `kpiCards` | `kpiCards(el, { cards: [{label, value, change?}] })` |
| `chartSection` | `chartSection(el, { chart: fn, title, narrative })` |
| `sectionNav` | `sectionNav(el, { sections: [{id, label}] })` |
| `footer` | `footer(el, { methodology?, sources? })` |

### Theme
| Import | Usage |
|--------|-------|
| `applyTheme` | `applyTheme({ primary?, accent?, bg?, text?, card?, border? })` |

## Palette Object

The `palette` maps series names (column labels from data) to hex colors:
```javascript
palette: { 'Sky': '#0072CE', 'BT': '#6400AA', 'Virgin': '#C8102E' }
```

If omitted, components use a default accessible color palette.

## Brand Themes

Check the system prompt for a `## Brand Themes` section with pre-configured palettes. Or run:
```bash
pt brand-themes --format json
```

Apply brand colors via `applyTheme()` and chart `palette` objects.

## Data Validation

The data-transform test gate from the data-visualization skill still applies for complex transforms. For simple dashboards with direct data display, you can skip the test file -- but always validate data is non-empty:

```javascript
const records = await getRecords('dataset_name')
if (!records || records.length === 0) {
  // Show empty state or skip this section
}
```

## Multi-Page Dashboards

For dashboards with many sections, use `sectionNav` for navigation:
```javascript
sectionNav(app, { sections: [
  { id: 'overview', label: 'Overview' },
  { id: 'awareness', label: 'Awareness' },
  { id: 'consideration', label: 'Consideration' },
  { id: 'usage', label: 'Usage & Attitudes' },
] })
```

Each section should be wrapped in a `<section>` element with the matching id.

## Register (no build)

After editing `webapp/app.js`, screenshot to verify, then register the entry HTML.
There is no build step.
```
screenshot_url(url="/workspace/webapp/index.html")
register_artifact(file_path="webapp/index.html", artifact_type="webapp", label="Dashboard Title")
```
Registering `webapp/index.html` captures the whole `webapp/` directory (app.js,
style.css, lib/, images).
