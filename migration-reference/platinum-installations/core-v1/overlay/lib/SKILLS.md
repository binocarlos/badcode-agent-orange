# Agent Skills — Web Visualizations

## Overview

Your workspace (`/workspace/`) is pre-initialized as a Vite web app. All data files from `/workspace/data/` are auto-loaded at build time — no scaffolding or manual imports needed. Just write visualization code and build.

## Workspace Structure

```
/workspace/
├── index.html              Vite entry point
├── package.json            Project config (type: module)
├── vite.config.js          Vite config (base: './')
├── node_modules/           → /opt/viz-template/node_modules (symlink)
├── data/                   Tabulated JSON files land here
├── src/
│   ├── main.js             Your visualization code (edit this)
│   ├── style.css           Dark-theme dashboard styles
│   └── lib/
│       ├── platinum-data.js    Low-level PlatinumData helpers
│       └── data-loader.js     Auto-discovers all data/*.json
├── dist/                   Build output (after vite build)
└── lib/                    Workspace utilities
    ├── SKILLS.md           This file
    ├── platinum.py          Python PlatinumData helper
    └── viz/
        └── scaffold.sh     Create additional sub-projects
```

## Available Libraries

- **D3.js v7** — data-driven SVG/Canvas visualizations
- **Three.js** — 3D graphics and WebGL
- **Chart.js** — canvas-based charts (bar, line, pie, radar, etc.)
- **Observable Plot** — high-level grammar-of-graphics charting
- **D3 Plugins** — d3-sankey, d3-cloud, d3-svg-annotation, d3-svg-legend, d3-hexbin, d3-regression, d3-interpolate-path, d3-scale-cluster, d3-funnel, textures, tippy.js (see data-visualization skill for usage)

All libraries are pre-installed at `/opt/viz-template/node_modules/`. No `npm install` needed.

## Workflow

### 1. Tabulate data

Use `pt query` or `render_table`. JSON files save to `/workspace/data/`.

### 2. Write visualization code

Edit `/workspace/src/main.js`. Data is already available via the data-loader:

```javascript
import * as d3 from 'd3'
import './style.css'
import { listDatasets, getAllDatasets, getRecords, getMatrix, getDatasetMeta } from './lib/data-loader.js'

const datasets = getAllDatasets()  // { filename: platinumDataObj, ... }
const records = getRecords('my_table')  // [{row, col, value}, ...]
```

### 3. Build

```bash
cd /workspace && vite build
```

### 4. Register the artifact

```
register_artifact(
  file_path="dist/index.html",
  label="Interactive Dashboard",
  description="D3 dashboard showing survey results",
  artifact_type="webapp"
)
```

## Data Loader API

The data-loader (`src/lib/data-loader.js`) auto-imports all `data/*.json` at build time.

| Function | Returns | Description |
|----------|---------|-------------|
| `listDatasets()` | `string[]` | All dataset names |
| `getAllDatasets()` | `{ name: obj }` | All datasets |
| `getDataset(name)` | `object` | Single PlatinumData object |
| `getRecords(name, metric?)` | `[{row, col, value}]` | Flat records for D3, Plot |
| `getMatrix(name, metric?)` | `{rows, columns, values}` | Matrix for Chart.js |
| `getDatasetMeta(name)` | `{top, side, ...}` | Table metadata |

**Metric options:** `'colpc'` (default), `'rowpc'`, `'freq'`

Low-level helpers (pass raw PlatinumData object directly):

| Function | Returns | Description |
|----------|---------|-------------|
| `toRecords(data, metric?, includeBase?)` | `[{row, col, value}]` | Flat records |
| `toMatrix(data, metric?, includeBase?)` | `{rows, columns, values}` | Matrix format |
| `getBaseSizes(data)` | `[{column, base}]` | Unweighted counts per column |
| `getMeta(data)` | `{top, side, filter, weight, name}` | Table metadata |
| `getColumnLabels(data)` | `string[]` | Top axis labels |
| `getRowLabels(data)` | `string[]` | Side axis labels |

## CSS Theme

The base `style.css` provides a dark-theme dashboard foundation:

- CSS custom properties (colors, spacing, shadows)
- `.dashboard-grid` — responsive grid layout (auto-fit, or `.cols-2`, `.cols-3`)
- `.card` / `.card-full` — content cards
- `.kpi-row` / `.kpi` — stat/metric widgets
- `.tooltip` — positioned tooltip
- SVG defaults for D3 axis styling

## Multi-project Use

For separate Vite projects (rare), scaffold additional ones:

```bash
bash /workspace/lib/viz/scaffold.sh my-other-viz
```

The workspace root app (`/workspace/`) is preferred for single-dashboard builds.

## PlatinumData JSON Schema

The raw PlatinumData JSON returned by tabulation queries has this structure:

```json
{
  "cells": {
    "rows": [
      { "cell": [ { "freq": 150, "colpc": 45.5, "rowpc": 30.2, "sig": "" }, ... ] },
      ...
    ]
  },
  "top": {
    "groups": [...],
    "vecs": [
      { "type": "Base", "label": "Base", "letter": "", "allowPC": false },
      { "type": "Code", "label": "Male", "letter": "a", "allowPC": true },
      { "type": "Code", "label": "Female", "letter": "b", "allowPC": true },
      { "type": "Net", "label": "Total", "letter": "", "allowPC": true }
    ]
  },
  "side": {
    "groups": [...],
    "vecs": [
      { "type": "Base", "label": "Base", "letter": "", "allowPC": false },
      { "type": "Code", "label": "18-24", "letter": "", "allowPC": true },
      { "type": "Code", "label": "25-34", "letter": "", "allowPC": true }
    ]
  },
  "meta": { "top": "Gender", "side": "Age", "filter": "", "weight": "" },
  "name": "table_name"
}
```

### Vec types
- `"Code"` — standard data code
- `"Net"` — net/subtotal
- `"Arith"` — arithmetic/calculated element
- `"Stat"` — statistical test row
- `"Base"` — base/sample size (skipped by helpers)
- `"Spacer"` — visual spacer (skipped by helpers)
- `"Empty"` — empty placeholder (skipped by helpers)

### Cell values
- `freq` — raw frequency (unweighted count)
- `colpc` — column percentage. Raw JSON stores as 0-1 decimals (e.g., 0.455). The helpers automatically convert to 0-100 (e.g., 45.5%)
- `rowpc` — row percentage. Same 0-1 → 0-100 conversion by helpers
- `sig` — significance test letters (e.g. "ab" means significantly different from columns a and b)

### Important
- The data-loader helpers (`getRecords`, `getMatrix`, etc.) automatically skip Base/Spacer/Empty vecs
- **Always use the helpers** instead of accessing raw JSON paths — they handle filtering, labeling, and percentage scaling
- Helper output is already 0-100 percentages — do NOT multiply by 100 again
- **Never parse raw PlatinumData JSON directly** — always use `toRecords()`, `toMatrix()`, or the data-loader functions

## Component Library

Pre-built chart and layout components at `./app/src/lib/components/`. Import and configure instead of writing D3/Chart.js from scratch.

**Charts:** `barChart`, `lineChart`, `pieChart`, `radarChart`, `heatmap`, `dataTable`
**Layout:** `hero`, `kpiCards`, `chartSection`, `sectionNav`, `footer`
**Theme:** `applyTheme({ primary, accent, bg, text })`

See the `dashboard-template` skill for full usage examples.

## Python Libraries

**`/workspace/lib/pptx_template.py`** -- PowerPoint report generation with native editable charts. See `pptx-template` skill.
**`/workspace/lib/style_extract.py`** -- Extract visual style from reference files. See `style-extraction` skill.

## Architecture Rules

1. **Always use `base: './'`** in `vite.config.js` — ensures relative paths for proxy URL
2. **Always run `vite build`** before registering — frontend loads from `dist/`
3. **Use ES module imports** — Vite resolves them at build time
4. **Register `dist/index.html`** — not the root `index.html`
5. **Artifact type must be `webapp`** — enables iframe renderer
6. **Keep visualizations self-contained** — all data is bundled; no external API calls from iframe
