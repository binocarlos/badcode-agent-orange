# Agent Skills — Web Visualizations

## Overview

Webapps are **plain HTML with no build step** (no Vite, no npm, no `node_modules`,
no `dist/`). Edit the starter at `/workspace/webapp/` and register its entry HTML.
Libraries load from a CDN via the import map in `webapp/index.html`, so you still
`import * as d3 from 'd3'` with nothing to install. Data is fetched at runtime by the
async data-loader.

## Workspace Structure

```
/workspace/
├── data/                   Tabulated JSON files land here (render_table)
├── webapp/                 The webapp you edit + register (no build)
│   ├── index.html          Entry: import map (CDN libs) + style.css link + app.js
│   ├── app.js              Your visualization code (edit this)
│   ├── style.css           Dark-theme dashboard styles
│   └── lib/
│       ├── platinum-data.js    Low-level PlatinumData transforms
│       ├── data-loader.js      Async runtime loader for ../../data/*.json
│       └── components/         Reusable chart + layout components
└── lib/                    Workspace utilities
    ├── SKILLS.md           This file
    ├── platinum.py          Python PlatinumData helper
    └── viz/
        └── inspect-data.js  Node tool to inspect tabulated data
```

## Available Libraries

Loaded via the import map in `webapp/index.html` (no install):

- **D3.js v7** — `import * as d3 from 'd3'`
- **Three.js** — `import * as THREE from 'three'`
- **Chart.js** — `import Chart from 'chart.js/auto'`
- **Observable Plot** — `import * as Plot from '@observablehq/plot'`
- **More (D3 plugins, tippy, etc.)** — add a line to the import map pointing at
  `https://esm.sh/<package>@<version>`, then import by name. See data-visualization skill.

**Never run `npm install`.**

## Workflow

### 1. Tabulate data

Use `render_table(..., datasetName="awareness")`. JSON files save to `/workspace/data/`.

### 2. Write visualization code

Edit `/workspace/webapp/app.js`. The data-loader is **async** — `await` it. The CSS is
linked in `index.html`; do not import it from JS.

```javascript
import * as d3 from 'd3'
import { getRecords, getMatrix, getDatasetMeta } from './lib/data-loader.js'

async function main() {
  const records = await getRecords('awareness')   // [{row, col, value}], 0-100
  // ...build charts...
}
main()
```

### 3. Preview & Register (no build)

```
screenshot_url(url="/workspace/webapp/index.html")
register_artifact(file_path="webapp/index.html", artifact_type="webapp", label="Interactive Dashboard")
```
Registering `webapp/index.html` captures the whole `webapp/` directory.

## Data Loader API

The data-loader (`webapp/lib/data-loader.js`) fetches `../../data/*.json` at runtime.
**Dataset accessors are async — await them.**

| Function | Returns | Description |
|----------|---------|-------------|
| `await listDatasets()` | `string[]` | Best-effort (optional `data/_manifest.json`); prefer explicit names |
| `await loadDataset(name)` | `object` | Single raw PlatinumData object |
| `await getRecords(name, metric?)` | `[{row, col, value}]` | Flat records for D3, Plot |
| `await getMatrix(name, metric?)` | `{rows, columns, values}` | Matrix for Chart.js |
| `await getDatasetMeta(name)` | `{top, side, ...}` | Table metadata |

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

For a second, separate webapp (rare), create another directory under `/workspace/`
(e.g. `/workspace/webapp2/`) with its own `index.html` (copy the import map), `app.js`,
and `lib/` (copy `/workspace/webapp/lib/`), then register `webapp2/index.html`. The
single `/workspace/webapp/` is preferred for one dashboard.

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

Pre-built chart and layout components at `webapp/lib/components/` (import as `./lib/components/index.js` from `app.js`). Import and configure instead of writing D3/Chart.js from scratch.

**Charts:** `barChart`, `lineChart`, `pieChart`, `radarChart`, `heatmap`, `dataTable`
**Layout:** `hero`, `kpiCards`, `chartSection`, `sectionNav`, `footer`
**Theme:** `applyTheme({ primary, accent, bg, text })`

See the `dashboard-template` skill for full usage examples.

## Python Libraries

**`/workspace/lib/pptx_template.py`** -- PowerPoint report generation with native editable charts. See `pptx-template` skill.
**`/workspace/lib/style_extract.py`** -- Extract visual style from reference files. See `style-extraction` skill.

## Architecture Rules

1. **No build step** — never run `vite` or `npm install`. Edit `webapp/` and register.
2. **Register `webapp/index.html`** with `artifact_type="webapp"` — captures the whole directory and enables the iframe renderer.
3. **Use relative paths** — `./lib/...`, `../data/...`, `./logo.png` (never leading `/`).
4. **Add libraries via the import map** in `index.html` (esm.sh), then import by name.
5. **`await` the data-loader** — it fetches `../../data/*.json` at runtime.
6. **Keep visualizations self-contained** — the iframe makes no authenticated API calls; data is loaded from the captured `../data` files.
