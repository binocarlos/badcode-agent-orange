---
name: pptx-report-learnings
description: Data handling rules for PPTX reports — chart type selection, clustered/stacked patterns, data loading, and gotchas that generate_pptx.py cannot catch
triggers:
  - powerpoint report
  - pptx report
  - chart formatting
  - clustered bar
  - stacked bar
  - switch rows columns
keywords: [pptx, powerpoint, report, chart, data handling, clustered, stacked, transpose]
---

# PPTX Data Handling Reference

Chart formatting, layout, and positioning are handled by `generate_pptx.py`. This skill covers what the agent still needs to get right: choosing chart types, loading data, and building the manifest correctly.

## Chart Type Selection

| Data Pattern | Manifest chart_type | series_columns |
|--------------|---------------------|----------------|
| 2-5 categories, single metric | `pie` | (default: Total) |
| 6+ categories, single metric | `bar` | (default: Total) |
| Comparing groups side-by-side | `bar` with explicit columns | `["Male", "Female"]` |
| Agree/disagree scales | `stacked bar` | `"all_except_total"` |
| Rating distributions | `stacked bar` | `"all_except_total"` |
| Demographics x answer options | `stacked bar` | explicit columns to stack |

## Clustered Bar/Column Charts

Use `chart_type: "bar"` (horizontal) or `"column"` (vertical) with multiple `series_columns` to get side-by-side bars for comparison.

```json
{
  "chart_type": "bar",
  "series_columns": ["Regionality is important", "Regionality not important"],
  "data_file": "natrep_q4.json"
}
```

This renders two bars per category, one for each segment. The spec typically says "Clustered bar" and notes which columns to put in the legend.

**Reading the spec notes:** When notes say "chart regionality columns only" or "put the 2 gender columns in the legend", set `series_columns` to those specific column names from the data.

## Stacked Charts — Automatic Transpose

**Stacked charts are automatically transposed by `generate_pptx.py`.** This is equivalent to PowerPoint's "Switch Row/Column" button.

PlatinumData tables come with:
- Rows = response options (Strongly agree, Agree, Disagree...)
- Columns = demographics/breaks (Total, Male, Female, 16-34...)

For stacked charts, the correct orientation is:
- Y-axis (categories) = demographics
- Stacking segments (series) = response options

The script transposes automatically for all stacked chart types. You can also force it with `"transpose": true` on any chart:

```json
{
  "chart_type": "stacked bar",
  "series_columns": "all_except_total",
  "data_file": "natrep_q9a.json"
}
```

Result: demographics on Y-axis, answer options stacking as coloured segments.

If the spec says "switch rows/columns" in the notes, this is already handled. If a non-stacked chart needs transposing, add `"transpose": true` explicitly.

## Data Loading

Always use `to_dataframe()`. Values are 0-100 scale (29.3 = 29.3%). The script uses `number_format = '0"%"'` automatically.

**Never:**
- Parse raw JSON colpc values manually
- Divide values by 100
- Use `number_format = '0%'` (multiplies by 100, shows "2930%")

## Manifest Data Fields

```json
{
  "data_file": "filename.json",
  "chart_type": "bar",
  "series_columns": ["Total"],
  "exclude_rows": ["Average", "Mean", "Total"],
  "transpose": false,
  "insight": "AI-generated insight with real numbers",
  "footer": "Table Name | n=1,000"
}
```

- `data_file`: filename in the data directory (set via `--data-dir`)
- `series_columns`: omit to use smart defaults based on chart_type
- `exclude_rows`: omit to use defaults (Average, Mean, Avg, Total)
- `transpose`: omit for default (auto-transpose on stacked, no transpose otherwise)
- The script handles empty data gracefully (skips chart, keeps slide)

## Template Placeholder Reference

| Layout | Title (idx=0) | Body (idx) | Footer (idx) |
|--------|--------------|------------|-------------|
| `Title slide` | yes | -- | -- |
| `One column slide` | yes | idx=17 | idx=18 |
| `Graph slide` | yes | -- | idx=22 |
| `Section title slide gradient A-E` | yes | -- | idx=18 |
| `Appendix slide` | yes | -- | idx=19 |
| `Thank you slide` | yes | -- | -- |

## Python Gotchas

```python
# Pandas Series truthiness — WRONG (crashes)
if base_sizes and 'Total' in base_sizes:

# CORRECT
if base_sizes is not None and len(base_sizes) > 0:
    if 'Total' in base_sizes.index:
        base = base_sizes['Total']
```

## What generate_pptx.py Handles (You Don't)

All of this is baked into the script. Do not reimplement:
- `chart.category_axis.reverse_order = True` on bar charts
- `chart.has_legend = False` on single-series
- `chart.has_title = False` (hides "Total" label)
- Automatic transpose for stacked charts (switch rows/columns)
- Adaptive layout (title font sizing, chart positioning)
- Adaptive body font sizing on text slides (prevents overflow)
- Insight below chart, footer below insight
- `number_format = '0"%"'`
- Empty data detection
- Row filtering (Average/Mean/Total)
- Section gradient cycling (A through E)
- Auto-fit text flags
- Adaptive chart formatting (font sizes based on density)
