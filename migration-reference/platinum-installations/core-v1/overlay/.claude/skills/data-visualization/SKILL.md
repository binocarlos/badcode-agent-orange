---
name: data-visualization
description: Interactive web visualizations using Vite, D3.js, Chart.js, Three.js, and Observable Plot
triggers:
  - interactive visualization
  - build a webapp
  - d3 chart
  - vite app
  - sankey diagram
  - interactive dashboard
keywords: [visualization, interactive, d3, chart.js, three.js, vite, webapp, dashboard, plot]
---

> **Recommended: Use the Dashboard Template**
> For standard dashboards with bar, line, pie, radar, heatmap, or table charts, use the `dashboard-template` skill instead of writing D3/Chart.js from scratch. The component library at `./src/lib/components/` handles all rendering -- you just import, configure, and compose. See the `dashboard-template` skill for examples.
>
> Use raw D3/Chart.js (this skill) only for: Sankey diagrams, treemaps, sunbursts, chord diagrams, word clouds, diverging stacked bars, bump charts, scatter with regression, lollipop charts, geographic maps, 3D visualizations, force-directed graphs, or novel interaction patterns.

# Interactive Web Visualizations

## When to Use
- When the user wants interactive, explorable visualizations — hover tooltips, zoom/pan, animated transitions, 3D views, or any interactivity beyond static charts
- For simple bar/line/pie charts, prefer `render_chart` instead

Your workspace is pre-initialized as a Vite web app with D3.js v7, Three.js, Chart.js, Observable Plot, and a full set of D3 plugins (Sankey, word cloud, annotations, legends, regression, funnel, textures, and more). All data files from `/workspace/data/` are auto-loaded at build time via the data-loader -- no manual imports or scaffolding needed.

## Mandatory Companion Skills

A webapp built without these is not shippable:

- **`table-provenance`** — every dataset in the webapp must come from a fully specified table (top, side, filter, weight, caseFilter). Defaults to filtering by "all answering" (`side_variable(*)`). Produces `data/spec-summary.md` for user sign-off **before** the webapp is built.
- **`data-provenance`** — TOC-first sourcing, source metadata embedded per chart, click-to-view drill-down.

> The Data Reference Document workflow below builds on the spec summary from `table-provenance`. If you have not run that workflow and obtained user sign-off, stop and do that first.

## Chart Selection Guide

Pick the right chart for the data shape. For standard bar/line/pie/radar, use the `dashboard-template` skill instead.

| Research Goal | Chart Type | Module/Plugin |
|---------------|-----------|---------------|
| Cross-tab comparison | Grouped bar, stacked bar, heatmap | D3 core |
| Part-to-whole breakdown | Treemap, sunburst, pie/donut | `d3-hierarchy` (core) |
| Flow / brand switching | Sankey diagram | `d3-sankey` |
| Bidirectional flow | Chord diagram | D3 core (`d3-chord`) |
| Brand positioning | Scatter biplot / perceptual map | D3 core + `d3-svg-annotation` |
| Open-ended themes | Word cloud | `d3-cloud` |
| Likert / rating scales | Diverging stacked bar | D3 core (custom layout) |
| Funnel / conversion | Funnel chart | `d3-funnel` |
| Time series tracking | Line + confidence bands | D3 core (`d3-shape`) |
| Demographic profiles | 100% stacked bar | D3 core (stacked layout) |
| Satisfaction drivers | Importance-performance quadrant | D3 scatter + `d3-svg-annotation` |
| Rankings over time | Bump chart | D3 core (line + ordinal Y) |
| Dense scatter | Hexbin plot | `d3-hexbin` |
| Correlation analysis | Scatter + regression line | `d3-regression` |
| Multi-attribute profiles | Radar / spider | Chart.js radar |

> **DATA VALIDATION GATE:** Steps 1b-1d below (data-transform module + test) are MANDATORY, not optional. You must write `data-transform.test.js`, run it, and see it pass BEFORE writing any visualization code in `main.js`. This prevents empty charts — a critical failure.

### Available Libraries

| Library | Best For | Import |
|---------|----------|--------|
| **D3.js v7** | Custom SVG/Canvas, maps, network graphs, complex layouts | `import * as d3 from 'd3'` |
| **Three.js** | 3D graphics, WebGL scenes, spatial data | `import * as THREE from 'three'` |
| **Chart.js** | Quick interactive charts (bar, line, radar, doughnut) | `import Chart from 'chart.js/auto'` |
| **Observable Plot** | Grammar-of-graphics, faceted plots, statistical charts | `import * as Plot from '@observablehq/plot'` |
| **D3 Plugins** | Sankey, word cloud, annotations, legends, hexbin, regression, funnel | See Plugin Reference below |

> **All libraries are pre-installed** in `/opt/viz-template/node_modules`. **Never run `npm install`** -- it breaks the node_modules symlink. Everything you need for data visualization is already available.

### Plugin Reference

All D3 plugins are pre-installed. Use these exact imports in your `src/main.js`:

| Plugin | Import | Creates |
|--------|--------|---------|
| `d3-sankey` | `import { sankey, sankeyLinkHorizontal } from 'd3-sankey'` | Sankey flow diagrams |
| `d3-cloud` | `import cloud from 'd3-cloud'` | Word cloud layouts |
| `d3-svg-annotation` | `import { annotation, annotationCalloutElbow, annotationLabel } from 'd3-svg-annotation'` | Chart annotations and callouts |
| `d3-svg-legend` | `import { legendColor, legendSize } from 'd3-svg-legend'` | Color/size legends from any d3 scale |
| `d3-hexbin` | `import { hexbin } from 'd3-hexbin'` | Hexagonal binning for dense scatter |
| `d3-regression` | `import { regressionLinear, regressionLoess, regressionPoly } from 'd3-regression'` | Trend/regression lines |
| `d3-interpolate-path` | `import { interpolatePath } from 'd3-interpolate-path'` | Smooth path morphing between chart states |
| `d3-scale-cluster` | `import { scaleCluster } from 'd3-scale-cluster'` | K-means clustering scale for natural breaks |
| `d3-funnel` | `import D3Funnel from 'd3-funnel'` | Funnel and pyramid charts |
| `textures` | `import textures from 'textures'` | SVG pattern fills for accessibility |
| `tippy.js` | `import tippy from 'tippy.js'; import 'tippy.js/dist/tippy.css'` | Rich tooltips with positioning and themes |

## Workflow

### 1. Tabulate data (MUST be done before building)

Use `render_table` with `datasetName` so you can reference data by name in your code:
```
render_table(spec=..., title="Brand Awareness", datasetName="awareness")
render_table(spec=..., title="Purchase Intent", datasetName="intent")
```
JSON files auto-save to `/workspace/data/awareness.json`, `/workspace/data/intent.json`.

**The data-loader resolves at build time via `import.meta.glob`. If you build before data exists, the visualization will show no data. Always tabulate first.**

After tabulating, verify the data before writing visualization code:
```bash
node /workspace/lib/viz/inspect-data.js              # list all datasets
node /workspace/lib/viz/inspect-data.js awareness     # see columns, rows, and preview values
```
This shows exactly what `getRecords`/`getMatrix` will return. **Do NOT try to import data-loader.js in Node** — it uses Vite-only APIs. Always use the inspect tool for debugging.

### 1b. Write the visualization

Use the data-loader directly in `/workspace/src/main.js`. The data-loader auto-discovers all JSON files in `/workspace/data/` at build time.

**Simple visualizations (1-2 datasets, direct charting)** — use `getRecords` or `getMatrix` directly:

```javascript
// /workspace/src/main.js
import * as d3 from 'd3'
import './style.css'
import { listDatasets, getRecords, getMatrix, getDatasetMeta } from './lib/data-loader.js'

// Discover datasets — never hardcode filenames
const names = listDatasets()
console.log('Available datasets:', names)

// getRecords returns [{row, col, value}] — flat records for D3, Observable Plot
const records = getRecords(names[0])

// getMatrix returns {rows: string[], columns: string[], values: number[][]} — for Chart.js
const { rows, columns, values } = getMatrix(names[0])

// Metadata: {top, side, filter, weight, name}
const meta = getDatasetMeta(names[0])
```

> **Do NOT** create separate "simplified" JSON files from PlatinumData. Do NOT use `toRecords` — use `getRecords` (which calls `toRecords` internally). Do NOT hardcode dataset filenames. Values are already 0-100 percentages.

**Complex visualizations (derived metrics, multi-dataset merges)** — write a `data-transform.js` module:

```javascript
// /workspace/src/data-transform.js
import { listDatasets, getRecords } from './lib/data-loader.js'

export function transformData(records) {
  const columns = [...new Set(records.map(r => r.col))]
  const rows = [...new Set(records.map(r => r.row))]
  const lookup = Object.fromEntries(
    columns.map(col => [col, Object.fromEntries(
      records.filter(r => r.col === col).map(r => [r.row, r.value])
    )])
  )
  return { columns, rows, lookup }
}

const names = listDatasets()
const records = getRecords(names[0])
export const { columns, rows, lookup } = transformData(records)
```

For complex transforms, write a test: `node --test src/data-transform.test.js` (see data-processing skill for test patterns). For statistical calculations (chi-squared, correlation), use Python with `scipy` and export results as JSON.

### 2. Customize styling

The base `style.css` provides a dark-theme dashboard foundation with CSS variables, grid layouts, card components, KPI widgets, and tooltip styles. Extend or replace it as needed.

### 3. Build

```bash
ls /workspace/data/    # Verify data files exist
cd /workspace && vite build
```

This produces `/workspace/dist/index.html` with bundled JS/CSS in `dist/assets/`.

### 4. Register source code and build artifacts

**Step 1: Register source files** so the user can inspect the code:
```
register_artifact(file_path="src/main.js", artifact_type="code", label="main.js", description="Visualization source code")
register_artifact(file_path="src/style.css", artifact_type="code", label="style.css", description="Stylesheet")
register_artifact(file_path="src/data-transform.js", artifact_type="code", label="data-transform.js", description="Data transform module")
```

**Step 2: Register the built webapp.** Immediately after a successful `vite build`, you MUST call `register_artifact` with `artifact_type="webapp"` pointing to `dist/index.html`. Do not wait for the user to ask.
```
register_artifact(
  file_path="dist/index.html",
  label="Interactive Dashboard",
  description="D3 dashboard showing survey results",
  artifact_type="webapp"
)
```

**Critical:** Register `dist/index.html` (the built output), NOT the root `index.html`. The `artifact_type` MUST be `"webapp"` for the iframe renderer.

### 5. Generate the Data Reference Document

Every webapp must ship with a Data Reference Document so the user can verify the underlying numbers. Webapp credibility relies on this — wrong figures in a webapp damage trust in the entire research project.

**Save to** `/workspace/dist/data-reference.md` (so it ships with the build) **and** `/workspace/data/data-reference.md` (so it appears as a regular artifact).

It must include, for every dataset that appears anywhere in the webapp:

| Field | Notes |
|-------|-------|
| Dataset name | The filename you passed via `datasetName` |
| Where it appears | "Hero KPI", "Awareness chart on page 2", etc. |
| Top axis | Verbatim Carbon syntax |
| Side axis | Verbatim Carbon syntax |
| Filter | The job filter or explicit filter expression — never blank |
| Weight | The weight name — never blank |
| Case filter | The caseFilter expression (default `side_variable(*)`) — never blank |
| Source | TOC path, or "ad-hoc" with reason |
| Base size | The unweighted base from the rendered table |

You already have most of this in `data/spec-summary.md` from the `table-provenance` skill. Extend it with **chart-to-dataset mapping**, **base sizes**, and a plain-language note per chart.

For each dataset: heading per chart → dataset name, source (TOC path), top/side/filter/weight/caseFilter verbatim, base (unweighted), plain-English reading of what the number shows.

Register and ask for sign-off:
```python
register_artifact(file_path="data/data-reference.md", label="Data Reference — please verify", artifact_type="file")
```
Then `ask_user`: *"I've saved a Data Reference Document at `data/data-reference.md` with the spec, filter, weight, case filter, and base for every chart. Please verify the figures before we share. Anything to change?"* — wait for response; if wrong, fix the table, regenerate, rebuild, update.

### 6. Logo and Image Handling

#### Never draw logos yourself

NEVER create SVG approximations of client logos — always use the actual image file from `/workspace/uploads/`. If none exists, ask before proceeding.

#### Use relative paths, never absolute

```html
<!-- CORRECT -->
<img src="./logo.png" />
<img src="./images/brand.png" />

<!-- WRONG — breaks in cloud storage -->
<img src="/logo.png" />
<img src="/images/brand.png" />
```

Absolute paths starting with `/` fail when the webapp is served from cloud storage, because the root URL does not point to the `dist/` folder. The `vite.config.js` uses `base: './'` for the same reason — relative paths only.

#### Copy assets to `dist/` after build

Vite does not copy arbitrary files from `/workspace/uploads/` into `dist/`. After `vite build`, copy any logos or images you reference:

```bash
# Build first
cd /workspace && vite build

# Copy logo into dist so it ships with the webapp
cp /workspace/uploads/ClientLogo.png /workspace/dist/ClientLogo.png

# Verify the copy
ls -la /workspace/dist/ClientLogo.png

# Then register the webapp
register_artifact(file_path="dist/index.html", artifact_type="webapp")
```

For images that live in a subfolder (e.g. `./images/brand.png`), create the folder in `dist/` first: `mkdir -p /workspace/dist/images && cp ... /workspace/dist/images/`.

#### Pre-registration checklist

Before calling `register_artifact(artifact_type="webapp")`, verify each of:

1. The source image exists: `ls -la /workspace/uploads/`
2. The HTML/JS uses a relative path (`./filename.png`, not `/filename.png`)
3. The image has been copied into `dist/`: `cp /workspace/uploads/<file> /workspace/dist/<file>`
4. The copy succeeded: `ls -la /workspace/dist/<file>`
5. The Data Reference Document (step 5) has been written and registered

Skipping any of these will produce a webapp that looks broken in the iframe.

## Data Loader Reference

The data-loader (`/workspace/src/lib/data-loader.js`) auto-discovers all JSON files in `/workspace/data/` using Vite's `import.meta.glob`. Functions:

| Function | Returns | Description |
|----------|---------|-------------|
| `listDatasets()` | `string[]` | All dataset names (filenames without .json) |
| `getAllDatasets()` | `{ name: obj }` | All datasets as an object |
| `getDataset(name)` | `object` | Single PlatinumData object |
| `getRecords(name, metric?)` | `[{row, col, value}]` | Flat records for D3, Plot |
| `getMatrix(name, metric?)` | `{rows, columns, values}` | Matrix for Chart.js, heatmaps |
| `getDatasetMeta(name)` | `{top, side, ...}` | Table metadata |
| `getBaseSizes(data)` | `[{column, base}]` | Unweighted counts (pass raw data) |
| `getColumnLabels(data)` | `string[]` | Top axis labels (pass raw data) |
| `getRowLabels(data)` | `string[]` | Side axis labels (pass raw data) |

**Metric options:** `'colpc'` (column %, default), `'rowpc'` (row %), `'freq'` (counts)

### PlatinumData format notes
- Raw JSON stores `colpc`/`rowpc` as 0-1 decimals (e.g., 0.455 = 45.5%)
- The helpers automatically convert to 0-100 — NEVER multiply by 100 yourself
- Labels come from survey data and may include prefixes like "NET:", long descriptions, or special formatting — NEVER hardcode expected labels
- Always derive labels from `getColumnLabels()`/`getRowLabels()` or from records

For complex transforms, use Python preprocessing as an alternative:

```python
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe
df = to_dataframe('/workspace/data/my-data.json')
df.to_json('/workspace/data/processed.json', orient='records')
```

## Examples

### D3 — Interactive Bar Chart

```javascript
import * as d3 from 'd3'
import './style.css'
import { listDatasets, getRecords, getDatasetMeta } from './lib/data-loader.js'

// Discover datasets — never hardcode filenames
const names = listDatasets()
const records = getRecords(names[0])
const meta = getDatasetMeta(names[0])
const app = d3.select('#app')
app.html('')
app.append('h1').text(meta.name || 'Results')

const width = 800, height = 500, margin = { top: 30, right: 30, bottom: 60, left: 60 }
const svg = app.append('svg').attr('viewBox', `0 0 ${width} ${height}`)

const x = d3.scaleBand().domain(records.map(d => d.row)).range([margin.left, width - margin.right]).padding(0.2)
const y = d3.scaleLinear().domain([0, d3.max(records, d => d.value)]).nice().range([height - margin.bottom, margin.top])

svg.selectAll('rect').data(records).join('rect')
  .attr('x', d => x(d.row)).attr('y', d => y(d.value))
  .attr('width', x.bandwidth()).attr('height', d => y(0) - y(d.value))
  .attr('fill', '#3b82f6')
  .on('mouseover', function() { d3.select(this).attr('fill', '#2563eb') })
  .on('mouseout', function() { d3.select(this).attr('fill', '#3b82f6') })

svg.append('g').attr('transform', `translate(0,${height - margin.bottom})`).call(d3.axisBottom(x))
svg.append('g').attr('transform', `translate(${margin.left},0)`).call(d3.axisLeft(y))
```

## D3 Chart-Type Guide

One row per chart type. Import lines are exact pinned sandbox packages — do not alter them.

| Chart | Use for | Import | Input shape | Gotcha |
|-------|---------|--------|-------------|--------|
| **Chord** | Bidirectional brand switching, mutual flow | `import * as d3 from 'd3'` (core only) | Square matrix — rows and cols must be the same label set | Build an n×n numeric matrix indexed by `labels.indexOf(r.row/col)` before passing to `d3.chord()`; ribbon color comes from `labels[d.source.index]`, not from the link itself |
| **Heatmap** | Cross-tab as colour grid | `import * as d3 from 'd3'` (core only) | `[{row, col, value}]` flat records | Derive SVG width/height from cell counts (`cellW * cols.length + margins`) rather than fixed px so large cross-tabs don't clip; flip label text color at a threshold (e.g. `value > 60` → white) so dark cells stay readable |
| **Force-directed network** | Brand associations, co-occurrence | `import * as d3 from 'd3'` (core only) | `nodes: [{id, group?}]`, `links: [{source, target, value?}]` | Release fixed position on drag-end: `fx = null, fy = null`; set `alphaTarget(0.3)` on drag-start and back to `0` on drag-end — omitting either causes nodes to freeze or never settle |
| **Sankey** | Directional brand switching, purchase funnel | `import { sankey, sankeyLinkHorizontal } from 'd3-sankey'` | Switching matrix rows `{row, col, value}` | **Prefix node IDs** (`src_<label>` / `tgt_<label>`) to avoid collisions when the same brand name appears on both source and target sides; filter links where `value === 0` before passing to layout |
| **Diverging stacked bar** | Likert scales, sentiment | `import * as d3 from 'd3'` (core only) | `{row: question, col: scale point, value: %}` | Negative segments grow leftward from centre-zero (`negX -= v` before appending rect); compute `maxExtent` across both halves to set a symmetric x-domain; use `d3.schemeRdYlGn[n]` keyed on exact scale-point count |
| **Treemap** | Hierarchical category breakdown | `import * as d3 from 'd3'` (core only) | Flat records grouped by `d.col` → parent, `d.row` → leaf | Suppress leaf labels when cell is too small: only render text when `x1-x0 > 40 && y1-y0 > 18`; use `paddingTop(20)` for group header labels |
| **Sunburst** | Hierarchical drill-down | `import * as d3 from 'd3'` (core only) | Same nested structure as treemap | Pass `[2 * Math.PI, radius]` to `d3.partition().size()` (angular × radial); suppress labels on thin slices with `(d.x1 - d.x0) > 0.12` threshold; colour by root-level ancestor (`while (p.depth > 1) p = p.parent`) |
| **Word cloud** | Open-ended coded themes | `import cloud from 'd3-cloud'` | Single-column records `{row: theme, value: frequency%}` — filter to `r.col === firstCol` | Layout is **async** — all rendering must happen inside the `.on('end', draw)` callback, not after `.start()`; cap at top 80 words; translate the `<g>` to `(width/2, height/2)` since d3-cloud positions words relative to centre |
| **Scatter + regression** | Correlation, driver analysis | `import { regressionLinear } from 'd3-regression'` | Two-column records pivoted to `{label, x, y}` by row | `regressionLinear().x().y()(points)` returns `[[x0,y0],[x1,y1]]` with `.rSquared` attached — draw as a `<line>` using those two endpoints; use `.nice()` on both scales or the line may extend beyond the axes |
| **Grouped bar** | Multi-variable comparison | `import * as d3 from 'd3'` (core only) | `{row: category, col: group, value}` | Use nested band scales: `x0` for outer (rows), `x1` for inner (cols) with `x1.range([0, x0.bandwidth()])`; rotate x-axis labels (`rotate(-30)`, `text-anchor: end`) for long category names |
| **Donut with centre label** | Part-to-whole with headline KPI | `import * as d3 from 'd3'` (core only) | Single-column records filtered to `r.col === firstCol` | `innerRadius * 0.55` / `outerRadius * 0.85` ratio keeps hole large enough for two-line centre text; suppress arc labels for slices below 5% (`d.data.value >= 5`) |
| **Lollipop** | Ranked comparison, cleaner bar alternative | `import * as d3 from 'd3'` (core only) | Single-column records sorted descending | Derive SVG height from data length (`data.length * 30 + 60`) rather than fixed px; draw stems as `<line>` before dots so dots render on top |
| **Bump chart** | Ranking changes over time | `import * as d3 from 'd3'` (core only) | `{row: brand, col: time period, value}` | Convert values to ranks per period (`sort` descending, then `map((d,i) => rank: i+1`)); use `d3.curveBumpX` for smooth S-curves; use `d3.scalePoint` (not scaleBand) for the time x-axis; use CSS-safe class names for per-brand dot selections (e.g. `.dot-${brand}`) |
| **Tooltips** | Any D3 chart | `import * as d3 from 'd3'` — or `import tippy from 'tippy.js'; import 'tippy.js/dist/tippy.css'` for rich tooltips | Attach to any selection | Plain D3: append a `div` to `body` with `position:absolute; pointer-events:none`; use `visibility` not `display` so element dimensions are preserved for positioning |
| **Zoom / pan** | Any SVG chart | `import * as d3 from 'd3'` (core only) | — | Apply zoom to the outer `<svg>`, transform the inner `<g>`: `svg.call(d3.zoom().scaleExtent([0.5, 8]).on('zoom', e => g.attr('transform', e.transform)))` |
| **Transitions** | Animated entry / data update | `import * as d3 from 'd3'` (core only) | — | Stagger entry: `.delay((d,i) => i * 50)`; smooth update: `.duration(750).ease(d3.easeCubicOut)` on same selection after `.data(newData)` |
| **Annotations** | Callouts on any chart | `import { annotation, annotationCalloutElbow, annotationLabel } from 'd3-svg-annotation'` | Array of `{note, x, y, dx, dy, type}` objects | Append to an existing `<g>` (not root `<svg>`); style `.annotation text` **after** `.call(makeAnnotations)` — styles applied before are overwritten by the plugin |
| **Legends** | Any chart with a colour scale | `import { legendColor, legendSize } from 'd3-svg-legend'` | Any d3 ordinal or sequential scale | Call `legendColor().scale(color).orient('vertical')` then `svg.append('g').call(legend)` — style text after the call |

## Accessibility

- Add `svg.attr('role','img')`, `svg.append('title').text(...)`, and `svg.append('desc').text(...)` to every SVG chart.
- Make interactive elements keyboard-focusable: `.attr('tabindex','0')` + `keydown` handler for Enter/Space.
- Prefer `d3.schemeTableau10` (colour-vision-deficiency safe). Minimum contrast: 4.5:1 for text, 3:1 for large graphics (WCAG 2.1 AA).
- Never encode data with colour alone — use the `textures` plugin (`import textures from 'textures'`) for redundant pattern fills on bars/areas.

## Market Research Data Patterns

### Cross-Tab Data (most common shape)

`getRecords(name)` returns `[{row, col, value}]` where:
- **rows** are side-axis labels (brands, demographics, scale points)
- **cols** are top-axis labels (metrics, time periods, segments)
- **values** are percentages 0-100 (already scaled, never multiply by 100)

Typical cross-tab shapes:
- **Brand x Metric**: rows=brands, cols=awareness/consideration/purchase
- **Brand x Wave**: rows=brands, cols=Q1/Q2/Q3 (use line or bump chart)
- **Likert x Question**: rows=questions, cols=scale points (use diverging stacked bar)
- **Square matrix**: rows=cols=brands (switching data, use chord or Sankey)

### Hierarchical Data from Flat Records

Nest flat records for treemap/sunburst using `d3.group`:

```javascript
const grouped = d3.group(records, d => d.col, d => d.row)
// Or use d3.rollup for aggregation:
const sums = d3.rollup(records, v => d3.sum(v, d => d.value), d => d.col)
```

### Open-Ended / Coded Response Data

Coded theme frequency tables come as a single-column cross-tab. Filter to first column and sort by value for word clouds or horizontal bar charts. Always limit to top 20-30 themes for readability.

### Switching / Flow Matrices

Brand switching data produces a square matrix (brands x brands). The diagonal shows retention; off-diagonal shows switching.
- **Chord diagram**: bidirectional flow (shows mutual switching)
- **Sankey diagram**: directional flow (from previous to current)
- Filter out diagonal (retention) for clearer flow visualization

## Troubleshooting

| Problem | Cause | Fix |
|---------|-------|-----|
| Empty chart, no errors | Data not tabulated before build | Run `render_table` first, verify with `inspect-data.js` |
| `import.meta.glob` error in Node | Running data-loader outside Vite | Use `inspect-data.js` for Node debugging, not data-loader |
| SVG renders off-screen | Missing `viewBox` | Always set `viewBox`, not fixed `width`/`height` |
| Tooltips behind elements | Missing `z-index` | Add `z-index: 100; pointer-events: none` to tooltip div |
| Axis labels cut off | Insufficient margins | Increase `margin.bottom` for rotated labels, `margin.left` for long labels |
| Colors invisible on dark bg | Hardcoded dark colors | Use light fills: `#e2e8f0`, `#f1f5f9`, or CSS variables |
| Plugin import fails | Wrong import path | Check Plugin Reference table for exact syntax |
| `npm install` breaks workspace | Overwrites symlink | **Never run `npm install`** -- all packages are pre-installed |
| Chart shows "NaN%" | Multiplying already-scaled values | Values from getRecords/getMatrix are already 0-100 |
| Labels show "undefined" | Hardcoded expected labels | Derive labels from data: `records.map(r => r.row)` |
| Build succeeds but blank page | JS error in browser | Use `screenshot_url` to check, review console output |
| Word cloud shows nothing | d3-cloud async not awaited | Use `.on('end', draw)` callback pattern (see example) |
| Sankey links overlap badly | Too many small flows | Filter `links` to only those with `value > threshold` |

## Multi-project Use

For complex work needing multiple Vite projects: `bash /workspace/lib/viz/scaffold.sh my-other-viz` creates `/workspace/my-other-viz/` with its own Vite config. Prefer the root workspace app for single dashboards.

## Rules

1. **Always `vite build`** before registering — the frontend loads from `dist/`, not the dev server
2. **Always use `artifact_type="webapp"`** — this enables the iframe renderer with proper relative path resolution
3. **Keep data self-contained** — embed or import all data into the build; the iframe cannot make authenticated API calls
4. **The `vite.config.js` must have `base: './'`** — the workspace sets this up automatically
5. **Make visualizations responsive** — use `viewBox` for SVG, or percentage-based sizing, since the iframe size varies
6. **Add a title and legend** — the user needs context for what they're seeing
7. **Always use the data-loader** from `src/lib/data-loader.js` — do not manually parse PlatinumData JSON
8. **Never hardcode data labels** — always derive column/row names from `getRecords()`, `getColumnLabels()`, or `getRowLabels()`.
9. **Always write a tested data-transform module** before visualization code — `src/data-transform.js` exports clean structures, tested with `node --test src/data-transform.test.js`. Never create simplified JSON copies; never reimplement statistics in JS (use Python + scipy).
10. **Never skip the data-transform test** — step 1d is a hard gate. `node --test` must pass before writing any `main.js`. Empty visualizations are a critical failure.
11. **Always run the `table-provenance` workflow first** — every dataset must have all five spec components (top, side, filter, weight, caseFilter) with user sign-off.
12. **Always generate a Data Reference Document (step 5)** and `ask_user` for sign-off before treating the webapp as final.
13. **Never draw logos as SVG approximations** — use the user-provided image file. If none exists, ask before proceeding.
14. **All image paths must be relative** (`./logo.png`, not `/logo.png`) and the image must be copied into `dist/` after `vite build`.
