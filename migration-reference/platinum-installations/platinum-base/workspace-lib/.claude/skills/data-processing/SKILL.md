---
name: data-processing
description: Python/pandas data processing with saved PlatinumData JSON files
triggers:
  - process data with pandas
  - statistical test
  - chi-squared
  - combine tables in python
  - export to csv
  - t-test on survey data
keywords: [python, pandas, dataframe, processing, statistics, scipy, csv, excel, export]
---

# Working with Saved Data

## When to Use
- When processing or transforming cross-tab data with Python/pandas
- When performing statistical analysis (chi-squared, correlation, t-test)
- When combining multiple tables or computing derived metrics
- When exporting data to CSV, Excel, or JSON formats
- When preparing data for visualization pipelines

## Workflow

**Python is the primary tool for data processing.** Use `to_dataframe()` to get a ready-to-use pandas DataFrame from any PlatinumData JSON — it handles parsing, percentage scaling, and label extraction automatically. Use `scipy.stats` for any statistical tests. Never manually parse PlatinumData JSON or reimplement statistics.

`render_table` and `render_chart` automatically save raw PlatinumData JSON to `/workspace/data/`. When the tool result shows a saved file path, use that file directly in any scripts — do not re-fetch the same data.

For data-only queries (no UI rendering):
```
pt query --top X --side Y --format json > /workspace/data/filename.json
```

### Python Helper

```python
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe, get_base_sizes, get_meta

df = to_dataframe('/workspace/data/<filename>.json')           # column %s (default)
df = to_dataframe('/workspace/data/<filename>.json', 'freq')   # raw counts
df = to_dataframe('/workspace/data/<filename>.json', 'rowpc')  # row percentages
bases = get_base_sizes('/workspace/data/<filename>.json')
meta = get_meta('/workspace/data/<filename>.json')
```

### DataFrame Structure

`to_dataframe()` returns a pandas DataFrame where:
- **Index** = side axis labels (row names from the cross-tab)
- **Columns** = top axis labels (column names from the cross-tab)
- **Values** = the chosen metric: 0-100 for percentages (`colpc`, `rowpc`), raw numbers for `freq`
- Base/Spacer rows are excluded by default (pass `include_base=True` to keep them)

```python
# Example output of df:
#              Male  Female  Total
# 18-24        15.2    18.1   16.7
# 25-34        22.4    20.3   21.3
# 35-44        19.8    21.5   20.7
```

### Common Pandas Operations

#### Selecting and Filtering
```python
# Select specific rows/columns
subset = df.loc[['18-24', '25-34'], ['Male', 'Female']]

# Drop a column (e.g., Total)
df_no_total = df.drop(columns=['Total'], errors='ignore')

# Boolean filtering
high_values = df[df['Male'] > 20]

# Filter rows by partial string match
youth = df[df.index.str.contains('18|25', regex=True)]
```

#### Aggregation
```python
# Mean across columns for each row
df['avg'] = df.mean(axis=1)

# Rank within each column
ranked = df.rank(ascending=False)

# Normalize rows to sum to 100
row_normed = df.div(df.sum(axis=1), axis=0) * 100

# Top N rows by a column
top5 = df.nlargest(5, 'Total')
```

#### Combining Multiple Tables
```python
# Load two tables
df1 = to_dataframe('/workspace/data/wave1.json')
df2 = to_dataframe('/workspace/data/wave2.json')

# Stack with labels
combined = pd.concat([df1, df2], keys=['Wave 1', 'Wave 2'])

# Side-by-side merge (shared index)
merged = df1.join(df2, lsuffix='_w1', rsuffix='_w2')

# Compute differences
diff = df2 - df1  # change between waves
```

#### Reshape
```python
# Melt wide to long (useful for plotting)
long = df.reset_index().melt(id_vars='index', var_name='Group', value_name='Percentage')

# Pivot long back to wide
wide = long.pivot(index='index', columns='Group', values='Percentage')
```

### Statistical Analysis with scipy

```python
from scipy import stats

# Correlation between two columns
r, p = stats.pearsonr(df['Male'], df['Female'])

# T-test between two groups
t, p = stats.ttest_ind(df['18-24'], df['35-44'])

# Chi-squared test (on frequency data)
df_freq = to_dataframe('/workspace/data/my_table.json', 'freq')
chi2, p, dof, expected = stats.chi2_contingency(df_freq.values)

# Descriptive statistics
desc = df.describe()  # count, mean, std, min, 25%, 50%, 75%, max
```

### Exporting Results

```python
# CSV
df.to_csv('/workspace/results.csv')

# Excel (single sheet)
df.to_excel('/workspace/results.xlsx', sheet_name='Data')

# Excel (multiple sheets)
with pd.ExcelWriter('/workspace/results.xlsx') as writer:
    df1.to_excel(writer, sheet_name='Wave 1')
    df2.to_excel(writer, sheet_name='Wave 2')
    diff.to_excel(writer, sheet_name='Change')

# JSON (for web app consumption)
df.to_json('/workspace/data/processed.json', orient='records')
```

Always register exported files: `register_artifact(file_path="results.xlsx", label="...", artifact_type="file")`

### Raw Case Data (Per-Respondent)
For respondent-level analysis, custom statistical tests, or combining variables in ways
cross-tabs don't support, download raw case data with `pt casedata`.
See `cat /workspace/lib/skills/casedata.md` for the full guide.

### When to Preprocess for Web App Visualization
For complex aggregations (merging multiple tables, computing derived metrics, statistical transforms), it's often easier to do the heavy lifting in Python and save the result as JSON, then build a simpler web app on top:

```python
# Heavy processing in Python
df = to_dataframe('/workspace/data/raw.json')
result = df.groupby(level=0).mean().round(1)
result.reset_index().to_json('/workspace/data/processed.json', orient='records')
```

Then the webapp can load the pre-processed JSON via the data-loader without needing complex transforms in JavaScript.

## Rules
1. Use `render_table` / `render_chart` to show data (auto-saved to `/workspace/data/`)
2. Write scripts that read from saved JSON using the `platinum` helper
3. Validate: `print(df.shape)`, `print(df.head(3))`, verify `len(df) > 0` before further processing
4. Save outputs (charts, CSVs, reports) to `/workspace`
5. Use `register_artifact` so the user can download them
6. Never manually parse PlatinumData JSON — always use `to_dataframe()`
7. Use `scipy.stats` for statistical tests — never reimplement statistics
