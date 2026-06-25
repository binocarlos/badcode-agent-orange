---
name: composite-tables
description: Combine Carbon cross-tab outputs with external data rows/columns for unified charts and reports
triggers:
  - overlay external data
  - combine survey and sales data
  - add ad spend to chart
  - merge external data
  - composite table
  - chart with non-survey data
keywords: [composite, merge, external data, ad spend, overlay, additional rows, combine, mixed data, augment]
---

# Composite Tables (Carbon + External Data)

## When to Use
- User wants to overlay external data (ad spend, sales, campaign dates) onto a Carbon cross-tab
- User asks to chart survey metrics alongside non-survey data
- User has an Excel/CSV file with supplementary data to combine with tabulations
- User wants trend charts combining tracker data with business metrics

> **Do NOT import external data into Carbon.** This skill works by merging data at the Python/DataFrame level after tabulation. The Carbon engine only handles survey data.

## Workflow

### 1. Run the Carbon table

```
render_table(spec='{"top":"wave(cwf\\Total\\;*)","side":"brandconsideration(cwf\\Total\\;*)"}', title="Consideration by Wave", datasetName="consideration")
```

### 2. Load the Carbon data

```python
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe, get_meta
import pandas as pd

df = to_dataframe('/workspace/data/consideration.json')
meta = get_meta('/workspace/data/consideration.json')
print(f"Columns (top axis): {list(df.columns)}")
print(f"Rows (side axis): {list(df.index)}")
```

### 3. Get the external data

**From customer files (Excel/CSV):**
```bash
pt files list --folder "Docs/Data"
pt files download "Docs/Data/Monthly Ad Spend.xlsx"
```

```python
ext = pd.read_excel('/workspace/Monthly Ad Spend.xlsx')
print(ext.head())
# Columns might be: Month, Spend_GBP, Campaign
```

**From user upload:**
```python
ext = pd.read_excel('/workspace/uploads/ad_spend.xlsx')
```

**From inline values (user provides in chat):**
```python
ext = pd.DataFrame({
    'Wave 1': [50000],
    'Wave 2': [75000],
    'Wave 3': [60000],
}, index=['Ad Spend (GBP)'])
```

### 4. Align and merge

The key step: ensure column labels match between Carbon data and external data.

```python
# Carbon columns might be: ['Wave 1 (Jan 2026)', 'Wave 2 (Feb 2026)', ...]
# External columns might be: ['Jan 2026', 'Feb 2026', ...]
# Build a mapping if needed

# Option A: External data already matches Carbon column names
composite = pd.concat([df, ext])

# Option B: Need to align by partial match or date
column_map = {}
for carbon_col in df.columns:
    for ext_col in ext.columns:
        if ext_col.lower() in carbon_col.lower():
            column_map[ext_col] = carbon_col

ext_aligned = ext.rename(columns=column_map)
composite = pd.concat([df, ext_aligned[df.columns]])

print(composite)
```

**Checkpoint:** Use `ask_user` to confirm the merged table looks correct before charting.

### 5. Visualise

The composite DataFrame can be charted like any other data. Common patterns:

**Dual-axis chart (survey metric + business metric):**
```python
import matplotlib.pyplot as plt

fig, ax1 = plt.subplots(figsize=(10, 5))

# Primary axis: survey metric (percentage)
metric_row = composite.loc['Brand Consideration']
ax1.plot(metric_row.index, metric_row.values, 'b-o', label='Consideration %')
ax1.set_ylabel('Consideration %', color='blue')
ax1.tick_params(axis='y', labelcolor='blue')

# Secondary axis: business metric (absolute value)
ax2 = ax1.twinx()
spend_row = composite.loc['Ad Spend (GBP)']
ax2.bar(spend_row.index, spend_row.values, alpha=0.3, color='green', label='Ad Spend')
ax2.set_ylabel('Ad Spend (GBP)', color='green')
ax2.tick_params(axis='y', labelcolor='green')

plt.title('Brand Consideration vs Ad Spend')
fig.legend(loc='upper left', bbox_to_anchor=(0.1, 0.95))
plt.tight_layout()
plt.savefig('/workspace/composite_chart.png', dpi=150)
```

**For interactive webapps:** Export the composite DataFrame as JSON and use Chart.js with dual Y axes. See the **data-visualization** skill.

### 6. Register output

```
register_artifact(file_path="composite_chart.png", label="Consideration vs Ad Spend", artifact_type="image")
```

## Data Alignment Patterns

| Scenario | Approach |
|----------|----------|
| Same column labels | Direct `pd.concat` |
| Different date formats | Parse dates, match by month/quarter |
| External has fewer columns | Align on intersection: `ext[df.columns.intersection(ext.columns)]` |
| External has extra columns | Filter to Carbon columns only |
| Multiple external rows | Concat all, then reorder with `composite.loc[desired_order]` |

## Rules

1. Never modify the Carbon PlatinumData JSON files -- work at the DataFrame level
2. Always verify column alignment before merging -- misaligned data produces misleading charts
3. Use `ask_user` to confirm the composite table before charting
4. Label external data rows clearly (include units: "Ad Spend (GBP)", "Sales (units)")
5. When mixing percentages and absolute values, use dual-axis charts
6. Note data provenance in chart titles or footnotes ("Source: Carbon survey + client ad spend data")
7. External data files may be in `/workspace/uploads/` (user upload) or downloaded via `pt files download`
