---
name: correspondence-analysis
description: Correspondence analysis and perceptual mapping of brands/attributes from cross-tabulation data
triggers:
  - correspondence analysis
  - perceptual map
  - brand positioning map
  - how do brands relate
  - biplot
  - brand attribute map
keywords: [correspondence analysis, perceptual map, biplot, brands, attributes, CA, dimension reduction, positioning]
---

# Correspondence Analysis & Perceptual Mapping

## When to Use
- User asks for correspondence analysis, perceptual mapping, or brand positioning map
- User wants to visualise relationships between brands and attributes
- User asks "how do brands relate to each other?" or "where does our brand sit?"
- User wants a biplot showing category structure

> **Uses cross-tab data** — see the **platinum-tables** and **carbon-syntax** skills for table specs. Uses **data-visualization** skill for the webapp output.

## Data Requirements

Correspondence analysis needs a contingency table: **brands (rows) x attributes (columns)** with frequency or percentage values.

Typical source: a grid/matrix question where respondents rate multiple brands on multiple attributes.

## Workflow

### 1. Find the right data

```
pt search "brand image"
pt search "attribute"
pt vars
```

Look for grid variables where brands are coded against attributes. Use `ask_user` to confirm which brand/attribute variables to use.

### 2. Build the contingency table

Run cross-tabs to build a brands x attributes matrix:

```python
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe
import pandas as pd

# Option A: Single grid variable (if available)
df = to_dataframe('/workspace/data/brand_image.json', metric='freq')

# Option B: Multiple tables combined
tables = ['brand_image_quality.json', 'brand_image_value.json', 'brand_image_trust.json']
dfs = [to_dataframe(f'/workspace/data/{t}', metric='freq') for t in tables]
df = pd.concat(dfs, axis=1)
```

### 3. Run correspondence analysis

```python
import numpy as np
from scipy.stats import chi2_contingency

# Chi-squared test for independence
chi2, p, dof, expected = chi2_contingency(df.values)
print(f"Chi-squared: {chi2:.1f}, p={p:.4f}, df={dof}")
print(f"Total inertia: {chi2 / df.values.sum():.4f}")

# Correspondence analysis via SVD
contingency = df.values.astype(float)
n = contingency.sum()
P = contingency / n
r = P.sum(axis=1)  # row masses
c = P.sum(axis=0)  # column masses

# Standardised residuals
Dr_inv = np.diag(1.0 / np.sqrt(r))
Dc_inv = np.diag(1.0 / np.sqrt(c))
S = Dr_inv @ (P - np.outer(r, c)) @ Dc_inv

U, sigma, Vt = np.linalg.svd(S, full_matrices=False)

# Row and column coordinates (principal coordinates)
row_coords = Dr_inv @ U[:, :2] * sigma[:2]
col_coords = Dc_inv @ Vt[:2, :].T * sigma[:2]

# Variance explained
inertia = sigma ** 2
pct_explained = inertia / inertia.sum() * 100
print(f"Dim 1: {pct_explained[0]:.1f}%, Dim 2: {pct_explained[1]:.1f}%")
```

### 4. Build the perceptual map

**Always build as standalone HTML first** — embed data directly, no external fetches.

```python
import json

map_data = {
    'brands': [{'name': n, 'x': float(row_coords[i, 0]), 'y': float(row_coords[i, 1])}
               for i, n in enumerate(df.index)],
    'attributes': [{'name': n, 'x': float(col_coords[i, 0]), 'y': float(col_coords[i, 1])}
                   for i, n in enumerate(df.columns)],
    'variance': [float(pct_explained[0]), float(pct_explained[1])],
    'chi2': float(chi2), 'p': float(p)
}

html = f"""<!DOCTYPE html>
<html><head>
<script src="https://d3js.org/d3.v7.min.js"></script>
<style>
  body {{ font-family: sans-serif; margin: 20px; background: #fff; }}
  .brand {{ fill: #2563eb; font-weight: bold; }}
  .attribute {{ fill: #dc2626; font-style: italic; }}
  .axis-line {{ stroke: #ccc; stroke-dasharray: 4; }}
</style>
</head><body>
<h2>Perceptual Map</h2>
<p>Dim 1: {pct_explained[0]:.1f}% | Dim 2: {pct_explained[1]:.1f}% | Chi2={chi2:.0f}, p={p:.4f}</p>
<div id="map"></div>
<script>
const data = {json.dumps(map_data)};
// ... D3 biplot rendering code ...
</script>
</body></html>"""

with open('/workspace/perceptual_map.html', 'w') as f:
    f.write(html)
```

Register: `register_artifact(file_path="perceptual_map.html", label="Perceptual Map", artifact_type="webapp")`

### 5. Write the executive summary

Structure:
1. **Methodology** — what CA is, how many dimensions, variance explained
2. **Map interpretation** — which brands cluster together, which attributes differentiate
3. **Key insights** — competitive positioning, white space opportunities
4. **Caveats** — base sizes, statistical significance

## Statistics to Report

| Metric | What it means |
|--------|--------------|
| Total inertia | Overall association strength |
| % variance per dimension | How much each axis explains |
| Chi-squared + p-value | Whether the association is statistically significant |
| Contribution of each point | Which brands/attributes drive each dimension |

## Common Mistakes

| Wrong | Right | Why |
|-------|-------|-----|
| Using percentages as input | Use frequencies (counts) | CA needs raw contingency data |
| Including Total/NES rows | Remove before analysis | They distort the geometry |
| Map renders blank in the webapp | Build/test the map as standalone HTML first | Avoids blank-screen debugging |
| Plotting >2 dimensions without explanation | Show 2D map, mention additional dimensions in text | Users can't interpret 3D+ |
| Zero cells in contingency table | Add small constant (0.5) or flag to user | SVD fails with structural zeros |

## Rules

1. Always use frequency counts as input, not percentages
2. Remove Total, NES, and "Don't Know" rows/columns before analysis
3. Report chi-squared test — if p > 0.05, warn that associations may not be significant
4. Report variance explained for each dimension
5. Build the perceptual map as standalone HTML with embedded data — test it renders before registering
6. Label brands and attributes clearly on the map with different colours
7. Include a methodology section in any executive summary
8. Handle zero cells: add 0.5 continuity correction or alert the user
