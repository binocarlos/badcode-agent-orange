---
name: python-visualization
description: Static image charts and plots using matplotlib, seaborn, and plotly
triggers:
  - static chart
  - matplotlib
  - seaborn
  - publication quality chart
  - heatmap
  - multi-panel figure
keywords: [python, matplotlib, seaborn, plotly, chart, plot, heatmap, static, image, png, svg]
---

# Python Visualizations (Static Images)

## When to Use
- When the user wants static charts, publication-quality graphics, statistical plots, heatmaps, multi-panel figures, or report-ready images
- For any matplotlib/seaborn/scipy request, or when the user chooses "static image" from the visualization format prompt

### Available Libraries

| Library | Best For | Notes |
|---------|----------|-------|
| **matplotlib** | All chart types, full control over layout, multi-panel figures | Core plotting library |
| **seaborn** | Statistical plots, heatmaps, distribution plots, styled charts | Built on matplotlib |
| **scipy** | Statistical tests, correlations, regressions | Use with matplotlib for visualization |
| **pandas** | Data manipulation, built-in `.plot()` for quick charts | Always available |
| **plotly** | Interactive HTML charts or static PNG/SVG via kaleido | Use `fig.write_html()` or `fig.write_image()` |

## Workflow

### 1. Tabulate data

Use `render_table` or `pt query` to get data saved to `/workspace/data/`:

```
pt query --top "Gender" --side "Age" --format json > /workspace/data/gender_by_age.json
```

Or use `render_table` which auto-saves JSON to `/workspace/data/`.

### 2. Load with platinum helper

```python
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe, get_base_sizes, get_meta

df = to_dataframe('/workspace/data/gender_by_age.json')           # column % (default)
df = to_dataframe('/workspace/data/gender_by_age.json', 'freq')   # raw counts
df = to_dataframe('/workspace/data/gender_by_age.json', 'rowpc')  # row percentages
bases = get_base_sizes('/workspace/data/gender_by_age.json')
meta = get_meta('/workspace/data/gender_by_age.json')
```

The DataFrame has: **index = side labels** (rows), **columns = top labels**, **values = metric** (0-100 for percentages).

### 3. Validate data (MANDATORY — do not skip)

After loading, verify the DataFrame is non-empty and inspect its structure. This catches wrong variable names, missing files, and empty cross-tabs BEFORE you write any plot code.

```python
print(f"Shape: {df.shape}")       # must be (rows > 0, cols > 0)
print(f"Columns: {list(df.columns)}")
print(df.head(3))
assert len(df) > 0, "DataFrame is empty — fix data pipeline before plotting"
assert len(df.columns) > 0, "No columns — check variable names"
```

If the DataFrame is empty, STOP. Do not proceed to plotting. Diagnose: check that the JSON file exists in `/workspace/data/`, verify variable names match the data, try a different metric.

### 4. Process with pandas

```python
# Filter rows/columns
subset = df.loc[['18-24', '25-34', '35-44'], ['Male', 'Female']]

# Aggregate
means = df.mean(axis=1)          # mean across columns per row
ranked = df.rank(ascending=False) # rank values

# Reshape for multi-table analysis
combined = pd.concat([df1, df2], keys=['Wave 1', 'Wave 2'])
```

### 5. Create visualization

Use matplotlib or seaborn to build the chart.

### 6. Save image

```python
plt.savefig('/workspace/chart.png', dpi=150, bbox_inches='tight', facecolor='white')
plt.close()
```

### 7. Register artifact

```
register_artifact(
  file_path="chart.png",
  label="Gender by Age Chart",
  description="Bar chart showing gender distribution across age groups",
  artifact_type="image"
)
```

## Image Settings Reference

| Setting | Screen/Web | Print/Report |
|---------|-----------|--------------|
| DPI | 150 | 300 |
| figsize | `(10, 6)` | `(12, 8)` |
| format | PNG | PNG, SVG, or PDF |

Always use `bbox_inches='tight'` to avoid clipped labels, and `facecolor='white'` for clean backgrounds.

## Examples

### Example 1: Grouped Bar Chart

```python
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe, get_meta
import matplotlib.pyplot as plt
import numpy as np

df = to_dataframe('/workspace/data/my_table.json')
meta = get_meta('/workspace/data/my_table.json')

fig, ax = plt.subplots(figsize=(10, 6))

x = np.arange(len(df.index))
width = 0.8 / len(df.columns)

for i, col in enumerate(df.columns):
    bars = ax.bar(x + i * width, df[col], width, label=col)
    for bar in bars:
        h = bar.get_height()
        if h > 0:
            ax.text(bar.get_x() + bar.get_width()/2, h + 0.5,
                    f'{h:.0f}%', ha='center', va='bottom', fontsize=8)

ax.set_xlabel(meta.get('side', ''))
ax.set_ylabel('Column %')
ax.set_title(meta.get('top', '') + ' by ' + meta.get('side', ''))
ax.set_xticks(x + width * (len(df.columns) - 1) / 2)
ax.set_xticklabels(df.index, rotation=45, ha='right')
ax.legend()

plt.savefig('/workspace/chart.png', dpi=150, bbox_inches='tight', facecolor='white')
plt.close()
```

### Example 2: Seaborn Heatmap

```python
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe, get_meta
import matplotlib.pyplot as plt
import seaborn as sns

df = to_dataframe('/workspace/data/my_table.json')
meta = get_meta('/workspace/data/my_table.json')

fig, ax = plt.subplots(figsize=(max(8, len(df.columns) * 1.2), max(6, len(df.index) * 0.5)))
sns.heatmap(df, annot=True, fmt='.1f', cmap='YlOrRd', ax=ax,
            linewidths=0.5, cbar_kws={'label': 'Column %'})
ax.set_title(meta.get('top', '') + ' by ' + meta.get('side', ''))

plt.savefig('/workspace/heatmap.png', dpi=150, bbox_inches='tight', facecolor='white')
plt.close()
```

### Example 3: Multi-Panel Figure

```python
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe
import matplotlib.pyplot as plt

df = to_dataframe('/workspace/data/my_table.json')

fig, axes = plt.subplots(2, 2, figsize=(14, 10))

# Top-left: horizontal bar
df.iloc[:, 0].sort_values().plot.barh(ax=axes[0, 0])
axes[0, 0].set_title(df.columns[0])

# Top-right: pie (limit to top 6 categories)
top6 = df.iloc[:, 0].nlargest(6)
top6.plot.pie(ax=axes[0, 1], autopct='%1.0f%%')
axes[0, 1].set_ylabel('')
axes[0, 1].set_title(df.columns[0])

# Bottom-left: line chart across columns
df.T.plot(ax=axes[1, 0], marker='o')
axes[1, 0].set_title('Trends Across Groups')
axes[1, 0].legend(bbox_to_anchor=(1, 1), fontsize=8)

# Bottom-right: stacked bar
df.plot.bar(stacked=True, ax=axes[1, 1])
axes[1, 1].set_title('Stacked Comparison')
axes[1, 1].legend(bbox_to_anchor=(1, 1), fontsize=8)

plt.suptitle('Multi-View Analysis', fontsize=14, fontweight='bold')
plt.tight_layout()
plt.savefig('/workspace/multi_panel.png', dpi=150, bbox_inches='tight', facecolor='white')
plt.close()
```

### Example 4: Plotly (Interactive HTML or Static Image)

Plotly can export interactive HTML or static PNG/SVG via kaleido:

```python
import plotly.express as px
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe

df = to_dataframe('/workspace/data/my_table.json').reset_index()
df_melted = df.melt(id_vars='index', var_name='Group', value_name='Percentage')

fig = px.bar(df_melted, x='index', y='Percentage', color='Group', barmode='group')
fig.write_html('/workspace/plotly_chart.html')        # interactive
fig.write_image('/workspace/plotly_chart.png')         # static PNG
fig.write_image('/workspace/plotly_chart.svg')         # static SVG
```

Register HTML as: `register_artifact(file_path="plotly_chart.html", label="...", artifact_type="file")`
Register images as: `register_artifact(file_path="plotly_chart.png", label="...", artifact_type="image")`

## Rules

1. **Always `plt.savefig()`**, never `plt.show()` — there is no display server
2. **Always `plt.close()`** after saving — prevents memory leaks in long scripts
3. **Register with `artifact_type="image"`** for PNG, SVG, and PDF files
4. **Use `facecolor='white'`** — transparent backgrounds look bad in the UI
5. **Save images to `/workspace/`**, not `/workspace/data/` — `data/` is for source JSON
6. **Never hardcode labels** — derive from DataFrame `df.index`, `df.columns`, or `get_meta()`
7. **Use the `platinum` helper** — never parse PlatinumData JSON manually
8. **For multi-table analysis**, load each table separately and merge with pandas
9. **Always add titles and axis labels** — the user needs context for what they're seeing
10. **For many categories (>15)**, prefer horizontal bars or heatmaps over vertical bar charts
11. **Always validate data before plotting** — `print(df.shape)`, `print(df.head(3))`, `assert len(df) > 0`. Never call `plt.savefig()` on an empty or unverified DataFrame. Empty charts are a critical failure.
