---
name: casedata
description: Raw per-respondent case data access and analysis with pt casedata
triggers:
  - raw data
  - case data
  - respondent level
  - per respondent
  - download raw data
  - pt casedata
keywords: [casedata, raw data, respondent, per-case, chi-squared, correlation, segmentation, weight]
---

# Raw Case Data (Per-Respondent Data Access)

## When to Use

Use **raw case data** (`pt casedata`) when you need:
- Custom statistical tests (chi-squared, correlation, regression)
- Respondent-level clustering or segmentation
- Combining variables in ways Carbon syntax doesn't support
- Bespoke cross-tabulations with custom logic
- Building novel data processing scripts
- Exporting per-respondent data for external tools

Use **Carbon cross-tabs** (`render_table`, `pt query`) instead when you need:
- Standard frequency tables and percentages
- Pre-built table specifications
- Quick data exploration

## Downloading Raw Data

```
pt casedata <var1> [var2...]           # Creates one file per variable
pt casedata Gender --job survey2024    # Explicit job
pt casedata Gender Age -o combined.json  # Single combined file (old format)
```

The command creates one JSON file per variable in `/workspace/data/`:
- `pt casedata Gender Age` → `casedata_gender.json` + `casedata_age.json`
- `pt casedata Gender` → `casedata_gender.json`

## Data Format (Per-Variable File)

Each file contains one variable:
```json
{
  "variable": "Gender",
  "description": "Gender of respondent",
  "codes": {"1": "Male", "2": "Female"},
  "data_type": "single",
  "total_cases": 5000,
  "customer": "acme",
  "job": "survey2024",
  "values": [1, 2, 1, null, 2]
}
```

**Key concepts:**
- `values` is **column-oriented**: line N across all variable files = same respondent
- `values` contains: `int` for single codes, `[int]` for multi-select, `null` for missing, `float` for weights
- `codes` maps numeric codes to human-readable labels
- `data_type`: `"single"` (one answer), `"multi"` (multiple answers), `"grid"` (grid/matrix), `"weight"` (numeric weight)

## Python Helper

```python
import sys; sys.path.insert(0, '/workspace/lib')
from casedata import to_dataframe, cross_tabulate, filter_cases, get_variable_info

# Single variable → single-column DataFrame
df = to_dataframe('/workspace/data/casedata_gender.json')              # labeled
df = to_dataframe('/workspace/data/casedata_gender.json', labeled=False)  # raw codes

# Multiple variables → multi-column DataFrame
df = to_dataframe('/workspace/data/casedata_gender.json', '/workspace/data/casedata_age.json')

# Inspect a variable (no varname needed for per-variable files)
info = get_variable_info('/workspace/data/casedata_gender.json')
print(info)  # {'description': '...', 'codes': {...}, 'data_type': 'single', 'n_cases': 5000}

# Cross-tabulate two variable files
xtab = cross_tabulate('/workspace/data/casedata_gender.json', '/workspace/data/casedata_age.json')
xtab = cross_tabulate('/workspace/data/casedata_gender.json', '/workspace/data/casedata_age.json', normalize='col')
xtab = cross_tabulate('/workspace/data/casedata_gender.json', '/workspace/data/casedata_age.json', normalize='row')
xtab = cross_tabulate('/workspace/data/casedata_gender.json', '/workspace/data/casedata_age.json',
                       weight_var='/workspace/data/casedata_weight.json')

# Filter respondents across multiple variable files
young_males = filter_cases('/workspace/data/casedata_gender.json',
                           '/workspace/data/casedata_age.json',
                           Gender=1, Age=[1, 2, 3])
```

## Example Workflows

#### Custom cross-tab with chi-squared test
```python
from scipy import stats
from casedata import cross_tabulate

xtab = cross_tabulate('/workspace/data/casedata_gender.json',
                       '/workspace/data/casedata_satisfaction.json')
chi2, p, dof, expected = stats.chi2_contingency(xtab.values)
print(f"Chi-squared: {chi2:.2f}, p-value: {p:.4f}")
```

#### Respondent-level analysis
```python
from casedata import to_dataframe

df = to_dataframe('/workspace/data/casedata_age.json',
                  '/workspace/data/casedata_income.json',
                  '/workspace/data/casedata_region.json',
                  labeled=False)
# Group by region, compute mean income code
region_means = df.groupby('Region')['Income'].mean()
# Correlation between age and income
r = df[['Age', 'Income']].dropna().corr().iloc[0, 1]
```

#### Weighted analysis
```python
from casedata import cross_tabulate

weighted = cross_tabulate('/workspace/data/casedata_gender.json',
                           '/workspace/data/casedata_age.json',
                           weight_var='/workspace/data/casedata_weight.json',
                           normalize='col')
print(weighted)  # weighted column percentages
```

## Rules

- **Data consistency**: If the cross-tab engine says 9% consider Sky, the case data must show exactly 9% with Sky=1. Always validate case data results against a cross-tab of the same variable to confirm consistency. If numbers diverge, the case data may be using a different filter or data path — investigate before proceeding.
- **Case alignment**: Line N across ALL variable files represents the same respondent. This is guaranteed by the column-oriented storage format.
- **Missing data**: `null` values mean the respondent didn't answer that variable. Use `df.dropna()` or handle explicitly.
- **Multi-select variables**: Values are lists of codes (e.g., `[1, 3, 5]`). The `cross_tabulate` function handles these automatically by expanding each selected code.
- **No `--filter` flag**: `pt casedata` downloads all cases. To filter, read the table spec first (`pt table <path>`) for the correct filter/caseFilter, download all cases, then filter in Python.
  **Selecting the correct sample from the data**: There are often times when a user will ask you to run analysis on case level data 'from a table', or using the table as a base for further analysis - which you need access to case level data for.  In this instance, you need to find the filter logic from the table and IMPORTANTLY any casefilter logic too.  You would need to combine that logic of filter + casefilter, in order to replicate the case level sample in the table.  Be careful with you sytax when combining table filter syntax with casefilter.  Would need to wrap filter in ().
- **Variable discovery**: Use `pt vars` and `pt search` to find variable names before downloading. Verify with `pt vars <name>` to see available codes.
- **Combined file format**: The `-o` flag writes all variables to a single file (old multi-variable format). The helper functions support both formats transparently.
