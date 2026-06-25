---
name: data-visualization
description: Interactive no-build web visualizations using D3.js, Chart.js, Three.js, and Observable Plot (plain HTML + CDN import map, no Vite)
triggers:
  - interactive visualization
  - build a webapp
  - d3 chart
  - web app
  - sankey diagram
  - interactive dashboard
keywords: [visualization, interactive, d3, chart.js, three.js, webapp, dashboard, plot]
---

> **Recommended: Use the Dashboard Template**
> For standard dashboards with bar, line, pie, radar, heatmap, or table charts, use the `dashboard-template` skill instead of writing D3/Chart.js from scratch. The component library at `./lib/components/` handles all rendering -- you just import, configure, and compose.
>
> Use raw D3/Chart.js (this skill) only for: Sankey diagrams, treemaps, sunbursts, chord diagrams, word clouds, diverging stacked bars, bump charts, scatter with regression, lollipop charts, geographic maps, 3D visualizations, force-directed graphs, or novel interaction patterns.

# Interactive Web Visualizations (no build)

## When to Use
- When the user wants interactive, explorable visualizations — hover tooltips, zoom/pan, animated transitions, 3D views, or any interactivity beyond static charts
- For simple bar/line/pie charts, prefer `render_chart` instead

**There is no build step.** Webapps here are plain HTML: you edit files under
`/workspace/webapp/` and register the entry HTML. There is no Vite, no npm, no
`node_modules`, no `dist/`. Libraries load from a CDN via the import map in
`webapp/index.html`, so you still `import * as d3 from 'd3'` exactly as before —
just with nothing to install or compile. **Never run `npm install` or `vite`.**

The starter at `/workspace/webapp/` already contains `index.html` (the import map +
`style.css` link + `app.js` script), `app.js` (your code), `style.css` (curated dark
theme), and `lib/` (the data-loader, platinum-data transforms, and reusable components).

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

> **DATA VALIDATION GATE:** Verify every dataset with `node /workspace/lib/viz/inspect-data.js <name>` BEFORE writing any visualization code in `app.js`. This prevents empty charts — a critical failure.

### Available Libraries (via the import map)

Libraries resolve through the `<script type="importmap">` in `webapp/index.html`.
The starter maps the core set; **import them by their bare name**:

| Library | Best For | Import |
|---------|----------|--------|
| **D3.js v7** | Custom SVG/Canvas, maps, network graphs, complex layouts | `import * as d3 from 'd3'` |
| **Three.js** | 3D graphics, WebGL scenes, spatial data | `import * as THREE from 'three'` |
| **Chart.js** | Quick interactive charts (bar, line, radar, doughnut) | `import Chart from 'chart.js/auto'` |
| **Observable Plot** | Grammar-of-graphics, faceted plots, statistical charts | `import * as Plot from '@observablehq/plot'` |

**Need another library (a D3 plugin, tippy, etc.)?** Add one line to the import map
in `webapp/index.html` pointing at esm.sh, then import it by name. esm.sh resolves
transitive dependencies automatically. Example — add Sankey + word cloud:

```html
<script type="importmap">
{
  "imports": {
    "d3": "https://esm.sh/d3@7",
    "chart.js/auto": "https://esm.sh/chart.js@4/auto",
    "d3-sankey": "https://esm.sh/d3-sankey@0.12",
    "d3-cloud": "https://esm.sh/d3-cloud@1.2"
  }
}
</script>
```

Plugin → esm.sh specifier: `d3-sankey`, `d3-cloud`, `d3-svg-annotation`,
`d3-svg-legend`, `d3-hexbin`, `d3-regression`, `d3-funnel`, `textures`, `tippy.js`.
For tippy's CSS, add `<link rel="stylesheet" href="https://esm.sh/tippy.js@6/dist/tippy.css">`
to `index.html` (you can't `import` CSS without a bundler).

## Workflow

### 1. Tabulate data (MUST be done before building)

Use `render_table` with `datasetName` so you can reference data by name in your code:
```
render_table(spec=..., title="Brand Awareness", datasetName="awareness")
render_table(spec=..., title="Purchase Intent", datasetName="intent")
```
JSON files auto-save to `/workspace/data/awareness.json`, `/workspace/data/intent.json`.

After tabulating, verify the data before writing visualization code:
```bash
node /workspace/lib/viz/inspect-data.js              # list all datasets
node /workspace/lib/viz/inspect-data.js awareness     # see columns, rows, and preview values
```
This shows exactly what `getRecords`/`getMatrix` will return. The browser data-loader
uses `fetch`, which doesn't run in Node — always use the inspect tool for shell debugging.

### 2. Write the visualization

Edit `/workspace/webapp/app.js`. The data-loader is **async** (it fetches the JSON
at runtime) — `await` every call. You always know your dataset names because you set
them via `datasetName`; pass them explicitly.

```javascript
// /workspace/webapp/app.js
import * as d3 from 'd3'
import { getRecords, getMatrix, getDatasetMeta } from './lib/data-loader.js'

async function main() {
  // getRecords → [{row, col, value}] flat records for D3, Observable Plot (values 0-100)
  const records = await getRecords('awareness')
  // getMatrix → {rows, columns, values} for Chart.js
  const { rows, columns, values } = await getMatrix('awareness')
  // Metadata: {top, side, filter, weight, name}
  const meta = await getDatasetMeta('awareness')

  const app = d3.select('#app')
  // ...build charts...
}
main()
```

> **Do NOT** create separate "simplified" JSON files from PlatinumData. Do NOT use `toRecords` — use `getRecords` (which calls `toRecords` internally). Do NOT hardcode dataset filenames. Values are already 0-100 percentages.

The CSS is linked in `index.html` (`<link rel="stylesheet" href="./style.css">`) —
do **not** `import './style.css'` from JS (that needs a bundler). For derived metrics
or multi-dataset merges, write a small helper module under `webapp/lib/` and import it.
For statistical calculations (chi-squared, correlation), use Python with `scipy` and
write the results to `/workspace/data/<name>.json`, then load them like any dataset.

### 3. Preview

```bash
ls /workspace/data/    # Verify data files exist
```
Then screenshot the page to catch visual bugs before delivering:
```
screenshot_url(url="/workspace/webapp/index.html")
```
`screenshot_url` serves `/workspace` over HTTP so relative `../data` paths resolve.
Never start a server manually — it hangs the session.

### 4. Register the webapp

Point `register_artifact` at the entry HTML. This captures the **entire `webapp/`
directory** (`app.js`, `style.css`, `lib/`, and any images), so the whole app ships:

```
register_artifact(
  file_path="webapp/index.html",
  label="Interactive Dashboard",
  description="D3 dashboard showing survey results",
  artifact_type="webapp"
)
```

**Critical:** `artifact_type` MUST be `"webapp"` for the iframe renderer. You do not
need to separately register `app.js`/`style.css` — they ride along inside the webapp
directory and the user can browse them in the artifact viewer.

### 5. Generate the Data Reference Document

Every webapp must ship with a Data Reference Document so the user can verify the underlying numbers. Webapp credibility relies on this — wrong figures in a webapp damage trust in the entire research project.

**Save to** `/workspace/webapp/data-reference.md` (so it ships inside the webapp).

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

Register and ask for sign-off:
```python
register_artifact(file_path="webapp/data-reference.md", label="Data Reference — please verify", artifact_type="file")
```
Then `ask_user`: *"I've saved a Data Reference Document with the spec, filter, weight, case filter, and base for every chart. Please verify the figures before we share. Anything to change?"* — wait for response; if wrong, fix the table, re-tabulate, update.

### 6. Logo and Image Handling

#### Never draw logos yourself
NEVER create SVG approximations of client logos — always use the actual image file from `/workspace/uploads/`. If none exists, ask before proceeding.

#### Use relative paths, never absolute
```html
<!-- CORRECT -->     <img src="./logo.png" />
<!-- WRONG -->       <img src="/logo.png" />
```
Absolute paths starting with `/` fail when the webapp is served from cloud storage.

#### Copy images into the webapp directory
Place any referenced image inside `webapp/` so it is captured on register:
```bash
cp /workspace/uploads/ClientLogo.png /workspace/webapp/ClientLogo.png
ls -la /workspace/webapp/ClientLogo.png   # verify
```
For a subfolder (e.g. `./images/brand.png`): `mkdir -p /workspace/webapp/images && cp ... /workspace/webapp/images/`.

#### Pre-registration checklist
1. The source image exists: `ls -la /workspace/uploads/`
2. The HTML/JS uses a relative path (`./filename.png`, not `/filename.png`)
3. The image is inside `webapp/`: `cp /workspace/uploads/<file> /workspace/webapp/<file>`
4. The copy succeeded: `ls -la /workspace/webapp/<file>`
5. The Data Reference Document (step 5) has been written and registered

## Data Loader Reference

The data-loader (`/workspace/webapp/lib/data-loader.js`) fetches PlatinumData JSON
from `/workspace/data/` at runtime. **All dataset accessors are async — await them.**

| Function | Returns | Description |
|----------|---------|-------------|
| `await getRecords(name, metric?)` | `[{row, col, value}]` | Flat records for D3, Plot |
| `await getMatrix(name, metric?)` | `{rows, columns, values}` | Matrix for Chart.js, heatmaps |
| `await getDatasetMeta(name)` | `{top, side, ...}` | Table metadata |
| `await loadDataset(name)` | `object` | Raw PlatinumData object |
| `await listDatasets()` | `string[]` | Best-effort (reads optional `data/_manifest.json`) — prefer explicit names |
| `getBaseSizes(data)` | `[{column, base}]` | Unweighted counts (pass a raw dataset object) |
| `getColumnLabels(data)` | `string[]` | Top axis labels (pass a raw dataset object) |
| `getRowLabels(data)` | `string[]` | Side axis labels (pass a raw dataset object) |

**Metric options:** `'colpc'` (column %, default), `'rowpc'` (row %), `'freq'` (counts)

### PlatinumData format notes
- Raw JSON stores `colpc`/`rowpc` as 0-1 decimals (e.g., 0.455 = 45.5%)
- The helpers automatically convert to 0-100 — NEVER multiply by 100 yourself
- Labels come from survey data and may include prefixes like "NET:", long descriptions, or special formatting — NEVER hardcode expected labels
- Always derive labels from `getColumnLabels()`/`getRowLabels()` or from records

## Examples

### D3 — Interactive Bar Chart

```javascript
// /workspace/webapp/app.js
import * as d3 from 'd3'
import { getRecords, getDatasetMeta } from './lib/data-loader.js'

async function main() {
  const records = await getRecords('awareness')   // never hardcode filenames
  const meta = await getDatasetMeta('awareness')
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
    .on('mouseover', function () { d3.select(this).attr('fill', '#2563eb') })
    .on('mouseout', function () { d3.select(this).attr('fill', '#3b82f6') })

  svg.append('g').attr('transform', `translate(0,${height - margin.bottom})`).call(d3.axisBottom(x))
  svg.append('g').attr('transform', `translate(${margin.left},0)`).call(d3.axisLeft(y))
}
main()
```

## D3 Chart-Type Guide

One row per chart type. Any plugin import (e.g. `d3-sankey`) must first be added to the
import map in `index.html` (see "Available Libraries" above).

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
| **Bump chart** | Ranking changes over time | `import * as d3 from 'd3'` (core only) | `{row: brand, col: time period, value}` | Convert values to ranks per period; use `d3.curveBumpX` for smooth S-curves; use `d3.scalePoint` (not scaleBand) for the time x-axis |
| **Tooltips** | Any D3 chart | `import * as d3 from 'd3'` — or add `tippy.js` to the import map | Attach to any selection | Plain D3: append a `div` to `body` with `position:absolute; pointer-events:none`; use `visibility` not `display` so element dimensions are preserved for positioning |
| **Zoom / pan** | Any SVG chart | `import * as d3 from 'd3'` (core only) | — | Apply zoom to the outer `<svg>`, transform the inner `<g>`: `svg.call(d3.zoom().scaleExtent([0.5, 8]).on('zoom', e => g.attr('transform', e.transform)))` |
| **Transitions** | Animated entry / data update | `import * as d3 from 'd3'` (core only) | — | Stagger entry: `.delay((d,i) => i * 50)`; smooth update: `.duration(750).ease(d3.easeCubicOut)` on same selection after `.data(newData)` |
| **Annotations** | Callouts on any chart | `import { annotation, annotationCalloutElbow } from 'd3-svg-annotation'` | Array of `{note, x, y, dx, dy, type}` objects | Append to an existing `<g>` (not root `<svg>`); style `.annotation text` **after** `.call(makeAnnotations)` |
| **Legends** | Any chart with a colour scale | `import { legendColor } from 'd3-svg-legend'` | Any d3 ordinal or sequential scale | Call `legendColor().scale(color)` then `svg.append('g').call(legend)` — style text after the call |

## Accessibility

- Add `svg.attr('role','img')`, `svg.append('title').text(...)`, and `svg.append('desc').text(...)` to every SVG chart.
- Make interactive elements keyboard-focusable: `.attr('tabindex','0')` + `keydown` handler for Enter/Space.
- Prefer `d3.schemeTableau10` (colour-vision-deficiency safe). Minimum contrast: 4.5:1 for text, 3:1 for large graphics (WCAG 2.1 AA).
- Never encode data with colour alone — add the `textures` plugin to the import map for redundant pattern fills.

## Market Research Data Patterns

### Cross-Tab Data (most common shape)
`await getRecords(name)` returns `[{row, col, value}]` where:
- **rows** are side-axis labels (brands, demographics, scale points)
- **cols** are top-axis labels (metrics, time periods, segments)
- **values** are percentages 0-100 (already scaled, never multiply by 100)

Typical cross-tab shapes:
- **Brand x Metric**: rows=brands, cols=awareness/consideration/purchase
- **Brand x Wave**: rows=brands, cols=Q1/Q2/Q3 (use line or bump chart)
- **Likert x Question**: rows=questions, cols=scale points (use diverging stacked bar)
- **Square matrix**: rows=cols=brands (switching data, use chord or Sankey)

### Hierarchical Data from Flat Records
Nest flat records for treemap/sunburst using `d3.group` / `d3.rollup`.

### Open-Ended / Coded Response Data
Coded theme frequency tables come as a single-column cross-tab. Filter to the first column and sort by value for word clouds or horizontal bar charts. Limit to top 20-30 themes.

### Switching / Flow Matrices
Brand switching data produces a square matrix (brands x brands). Diagonal = retention; off-diagonal = switching. Chord = bidirectional, Sankey = directional. Filter out the diagonal for clearer flow.

## Troubleshooting

| Problem | Cause | Fix |
|---------|-------|-----|
| Empty chart, no errors | Data not tabulated, or forgot to `await` the loader | Run `render_table` first; verify with `inspect-data.js`; `await` every `getRecords`/`getMatrix` |
| `fetch`/data-loader error in Node | Running the browser loader outside the browser | Use `inspect-data.js` for shell debugging, not the data-loader |
| Bare import fails ("Failed to resolve module specifier") | Library not in the import map | Add it to the `<script type="importmap">` in `index.html` (esm.sh URL), then import by name |
| Library 404 / blank page | Sandbox could not reach the CDN | Verify the esm.sh URL/version; check `screenshot_url` output for console errors |
| SVG renders off-screen | Missing `viewBox` | Always set `viewBox`, not fixed `width`/`height` |
| Tooltips behind elements | Missing `z-index` | Add `z-index: 100; pointer-events: none` to tooltip div |
| Axis labels cut off | Insufficient margins | Increase `margin.bottom` for rotated labels, `margin.left` for long labels |
| Colors invisible on dark bg | Hardcoded dark colors | Use light fills: `#e2e8f0`, `#f1f5f9`, or CSS variables |
| Chart shows "NaN%" | Multiplying already-scaled values | Values from getRecords/getMatrix are already 0-100 |
| Labels show "undefined" | Hardcoded expected labels | Derive labels from data: `records.map(r => r.row)` |
| Blank page | JS error in browser | Use `screenshot_url` to check, review console output |
| Word cloud shows nothing | d3-cloud async not awaited | Use `.on('end', draw)` callback pattern |

## Multi-project Use

For a second, separate webapp, create another directory under `/workspace/` (e.g.
`/workspace/webapp2/`) with its own `index.html` (copy the import map), `app.js`, and a
`lib/` (copy `/workspace/webapp/lib/`), then register `webapp2/index.html`. Prefer the
single `webapp/` for one dashboard.

## Rules

1. **No build step** — never run `vite` or `npm`. Edit files under `webapp/` and register the entry HTML.
2. **Always `artifact_type="webapp"`** pointing at `webapp/index.html` — this enables the iframe renderer and captures the whole directory.
3. **Keep data self-contained** — the webapp loads `../data/*.json` at runtime (which is captured); the iframe cannot make authenticated API calls.
4. **Add libraries via the import map** — one line in `index.html` per library; never `npm install`.
5. **Make visualizations responsive** — use `viewBox` for SVG, or percentage-based sizing, since the iframe size varies.
6. **Add a title and legend** — the user needs context for what they're seeing.
7. **Always use the data-loader** from `./lib/data-loader.js` and **`await`** it — do not manually parse PlatinumData JSON.
8. **Never hardcode data labels** — always derive column/row names from records or `getColumnLabels()`/`getRowLabels()`.
9. **Validate data first** — `node /workspace/lib/viz/inspect-data.js <name>` before writing `app.js`. Empty visualizations are a critical failure.
10. **Always run the `table-provenance` workflow first** — every dataset must have all five spec components (top, side, filter, weight, caseFilter) with user sign-off.
11. **Always generate a Data Reference Document (step 5)** and `ask_user` for sign-off before treating the webapp as final.
12. **Never draw logos as SVG approximations** — use the user-provided image file. If none exists, ask.
13. **All image paths must be relative** (`./logo.png`) and the image must be copied into `webapp/`.
