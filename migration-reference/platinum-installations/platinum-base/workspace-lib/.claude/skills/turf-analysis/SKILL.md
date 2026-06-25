---
name: turf-analysis
description: TURF (Total Unduplicated Reach and Frequency) analysis for optimising product/brand portfolios
triggers:
  - turf
  - reach and frequency
  - optimal portfolio
  - which combination of products
  - incremental reach
  - unduplicated reach
keywords: [TURF, reach, frequency, incremental reach, portfolio, optimisation, unduplicated, multi-response]
---

# TURF Analysis (Total Unduplicated Reach & Frequency)

## When to Use
- User asks for TURF, reach analysis, or portfolio optimisation
- User wants to know "which combination of N items reaches the most people?"
- User asks about incremental reach, overlap analysis, or optimal product mix

> **Requires raw case data** — see the **casedata** skill for `pt casedata` usage.

## Data Requirements

TURF needs **binary per-respondent data**: for each item, does this person select it (1) or not (0)?

Typical sources: multi-response brand consideration, product usage/interest, any multi-select question.

**When to use TURF vs not:**
- Multi-response data (binary) → natural fit for TURF
- MaxDiff data → requires thresholding utilities to binary first (discuss with user)

## Workflow

### 1. Find the variable and read the existing spec

```
pt search "consideration"
pt vars BrandConsideration
```

**Critical:** Before downloading case data, read the existing table spec to get the correct filters:

```
pt table "Brand/Consideration"
```

The `[spec]` section contains `filt=` and `casefilt=` which MUST be applied to case data to match the table's universe. For example, a table may filter to a specific time period or segment.

### 2. Extract case data

**Note:** `pt casedata` does not support a `--filter` flag. Download all cases, then filter in Python using the filter logic from the spec.

```
pt casedata BrandConsideration
```

### 3. Validate base sizes

**Always verify:** check that your case count matches the base size in the reference table.

```python
import sys; sys.path.insert(0, '/workspace/lib')
from casedata import to_dataframe
import pandas as pd, json

data = json.load(open('/workspace/data/casedata_brandconsideration.json'))
codes = data['codes']
values = data['values']
total = len([v for v in values if v is not None])
print(f"Total cases with data: {total}")
# Compare this to the base size from pt table output
```

### 4. Build binary matrix

```python
# Multi-response: each value is a list of codes or a single code
binary = pd.DataFrame(index=range(len(values)))
for code, label in codes.items():
    code_int = int(code)
    binary[label] = [
        1 if (isinstance(v, list) and code_int in v) or v == code_int
        else 0
        for v in values
    ]

# Drop respondents with no selections
binary = binary[binary.sum(axis=1) > 0]
print(f"Items: {list(binary.columns)}")
print(f"Respondents: {len(binary)}")
print(f"Reach per item:\n{(binary.mean() * 100).round(1)}")
```

### 5. Run TURF analysis

#### Standard TURF (start from scratch)

```python
import numpy as np

def turf_analysis(binary_df, max_items=None):
    items = list(binary_df.columns)
    n = len(binary_df)
    if max_items is None:
        max_items = min(len(items), 10)

    selected = []
    remaining = set(items)
    current_reached = np.zeros(n, dtype=bool)
    results = []

    for step in range(max_items):
        best_item, best_reach, best_inc = None, 0, 0
        for item in remaining:
            new_reached = current_reached | binary_df[item].values.astype(bool)
            total_reach = new_reached.sum()
            if total_reach > best_reach:
                best_item, best_reach = item, total_reach
                best_inc = total_reach - current_reached.sum()
        if best_item is None:
            break
        selected.append(best_item)
        remaining.remove(best_item)
        current_reached = current_reached | binary_df[best_item].values.astype(bool)
        results.append({
            'rank': step + 1, 'item': best_item,
            'incremental_reach': best_inc / n * 100,
            'cumulative_reach': current_reached.sum() / n * 100,
        })
    return pd.DataFrame(results)

turf_results = turf_analysis(binary)
print(turf_results.to_string(index=False))
```

#### Anchor-brand TURF (start with a fixed item)

When the user says "which brands should we add alongside Sky?":

```python
def turf_with_anchor(binary_df, anchor_items, max_additional=5):
    """Start with anchor items fixed, find best additions."""
    n = len(binary_df)
    current_reached = np.zeros(n, dtype=bool)
    for item in anchor_items:
        current_reached = current_reached | binary_df[item].values.astype(bool)

    anchor_reach = current_reached.sum() / n * 100
    print(f"Anchor reach ({', '.join(anchor_items)}): {anchor_reach:.1f}%")

    remaining = set(binary_df.columns) - set(anchor_items)
    results = []
    for step in range(max_additional):
        best_item, best_reach, best_inc = None, 0, 0
        for item in remaining:
            new_reached = current_reached | binary_df[item].values.astype(bool)
            total_reach = new_reached.sum()
            if total_reach > best_reach:
                best_item, best_reach = item, total_reach
                best_inc = total_reach - current_reached.sum()
        if best_item is None or best_inc == 0:
            break
        selected.append(best_item)
        remaining.remove(best_item)
        current_reached = current_reached | binary_df[best_item].values.astype(bool)
        results.append({
            'rank': step + 1, 'item': best_item,
            'incremental_reach': best_inc / n * 100,
            'cumulative_reach': current_reached.sum() / n * 100,
        })
    return pd.DataFrame(results)
```

### 6. Visualise and present

Show: optimal portfolio order, cumulative reach curve, incremental reach bars, point of diminishing returns (<2% incremental), recommended portfolio size.

## Fallback: Cross-tab Overlap Method

If `pt casedata` fails (variable has no exported CaseData files), approximate overlaps using pairwise cross-tabs:

1. Filter to item A considerers: `pt query --filter "Brand(33)" --side "Brand(*)" --top "count(1)"`
2. Read overlap percentages from the filtered table
3. Derive incremental reach from pairwise overlaps

This is approximate — true TURF requires per-respondent data.

## Rules

1. Always use greedy algorithm — exhaustive search is O(n^k) and impractical for >15 items
2. Always read the existing table spec (`pt table`) for correct filters before downloading case data
3. `pt casedata` does not support `--filter` — download all cases and filter in Python
4. Always validate: check case count matches the base size in the reference table
5. Report both cumulative and incremental reach
6. Flag diminishing returns (<2% incremental)
7. Multi-response values must be expanded into binary columns
8. If `pt casedata` fails, explain the limitation and use the cross-tab overlap workaround
9. For anchor-brand TURF, start with the anchor fixed and find optimal additions
