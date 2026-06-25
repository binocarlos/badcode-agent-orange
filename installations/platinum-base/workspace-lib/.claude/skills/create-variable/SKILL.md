---
name: create-variable
description: Create new variables from case-level data analysis and write them back to the dataset
triggers:
  - create a variable
  - new variable
  - derived variable
  - recode variable
  - write variable back
  - construct variable
keywords: [variable, create, construct, derived, case data, write, new variable, recode, segment, index]
---

# Creating Variables

## When to Use

Use this skill when you need to:
- Create a derived variable by combining existing variables (e.g. age + income groups)
- Store classification or segmentation results as a new variable
- Create recoded or collapsed versions of existing variables
- Build index or score variables from multiple inputs
- Persist any per-respondent computed values back into the dataset for cross-tabulation

Do NOT use this for:
- Temporary analysis (use Python DataFrames instead)
- Data that doesn't map 1:1 to survey respondents

## Workflow

### Step 1: Get the Case Count and Discover Source Variables

**Always start by establishing the total case count.** This is the number of respondents in the job and determines exactly how many values your new variable must have.

```bash
# Get the total case count FIRST
pt variable count
# Output: Total cases: 5000

# Check what variables exist
pt variable list

# Find specific variables
pt search "age income"
pt vars Age

# Download raw case data
pt casedata Age Income Region
```

This creates one JSON file per variable in `/workspace/data/` with the column-oriented format where line N = respondent N.

### Step 2: Compute New Values in Python

```python
import sys; sys.path.insert(0, '/workspace/lib')
from casedata import to_dataframe

# Load source variables
df = to_dataframe(
    '/workspace/data/casedata_age.json',
    '/workspace/data/casedata_income.json'
)

total_cases = len(df)
# Verify this matches pt variable count output
print(f"Total cases: {total_cases}")  # Should match pt variable count

# Compute derived values
# CRITICAL: Preserve the respondent order. Do NOT sort, filter, or drop rows.
results = []
for _, row in df.iterrows():
    if row['Age'] is None or row['Income'] is None:
        results.append(None)  # Missing data -> null
    elif row['Age'] <= 2 and row['Income'] >= 3:
        results.append(1)  # Young high earners
    elif row['Age'] >= 4 and row['Income'] >= 3:
        results.append(2)  # Older high earners
    elif row['Income'] <= 2:
        results.append(3)  # Low earners
    else:
        results.append(4)  # Other

# ALWAYS validate before creating
assert len(results) == total_cases, f"Expected {total_cases} values, got {len(results)}"
valid_codes = {1, 2, 3, 4}
for i, v in enumerate(results):
    if v is not None and v not in valid_codes:
        raise ValueError(f"Invalid code {v} at index {i}")
```

### Step 3: Build the Variable JSON and Create

```python
import json

variable = {
    "name": "IncomeAgeGroup",
    "description": "Income-Age demographic groups",
    "folder": "Derived",
    "codes": {
        "1": "Young high earners",
        "2": "Older high earners",
        "3": "Low earners",
        "4": "Other"
    },
    "dataType": "single",
    "values": results,
    "overwrite": False
}

with open('/workspace/data/new_var.json', 'w') as f:
    json.dump(variable, f)
```

Then create the variable:

```bash
pt variable create --from-file /workspace/data/new_var.json
```

Or via stdin:

```bash
cat /workspace/data/new_var.json | pt variable create
```

### Step 4: Verify

Run a frequency table to confirm the variable works. **Always use `count` on top and the variable on side** — never use `Total` alone on an axis (it shows "dummy" for user-created variables):

```
render_table(spec='{"top":"count","side":"incomeagegroup(cwf\\Total\\;*)"}', title="Verify incomeagegroup")
```

**Do NOT run `pt construct`** — variables created via `pt variable create` already have case data and don't need construction. Construction is only for formula-based variables defined in the VTR.

## Variable JSON Format

```json
{
  "name": "VarName",
  "description": "Human-readable description",
  "folder": "FolderPath/SubFolder",
  "codes": {"1": "Label A", "2": "Label B"},
  "dataType": "single",
  "values": [1, null, 2, 1, null],
  "overwrite": false
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `name` | Yes | Variable name (lowercase, no spaces — Carbon always lowercases internally) |
| `description` | No | Human-readable description |
| `folder` | No | VTR folder path (slash-separated for nesting, created if missing) |
| `codes` | Yes | Map of code numbers (as strings) to labels |
| `dataType` | No | `"single"` (default) or `"multi"` |
| `values` | Yes | Array of per-respondent values (length must equal total cases) |
| `overwrite` | No | Set `true` to replace an existing variable |

### Value Format

| Data type | Value format | Example |
|-----------|-------------|---------|
| `single` | Integer code or `null` | `[1, 2, null, 1]` |
| `multi` | Array of integer codes or `null` | `[[1,3], [2], null, [1,2,3]]` |

## Critical Rules

### 1. Case Alignment is Absolute

The `values` array MUST have exactly the same number of entries as the total case count for the job. Line N in your values array = respondent N in the dataset. This is the same column-oriented format used by `pt casedata`.

**How to verify:**
```python
from casedata import to_dataframe

df = to_dataframe('/workspace/data/casedata_age.json')
total_cases = len(df)
assert len(results) == total_cases, f"Expected {total_cases} values, got {len(results)}"
```

**Never filter, sort, or reorder** the values array. If a respondent should not receive a code, set their value to `null`.

### 2. Codes Must Be Sequential Integers (as strings)

Code keys must be string-encoded integers starting from 1:
- Correct: `{"1": "Yes", "2": "No", "3": "Maybe"}`
- Wrong: `{"0": "Yes", "1": "No"}` (don't start from 0)
- Wrong: `{1: "Yes", 2: "No"}` (keys must be strings in JSON)

### 3. Variable Names

- **Always lowercase** — Carbon lowercases all variable names internally. The server enforces this.
- No spaces or special characters
- Check for conflicts first: `pt variable list`
- Use descriptive names: `incomeagegroup`, not `derived1`

### 4. Use `overwrite: true` Carefully

Overwriting a variable replaces its metadata, case data, and VTR entry. This is permanent. Always validate your data in Python before using `overwrite: true`.

### 5. After Creation

The variable is immediately available for cross-tabulation. Carbon's cache is automatically evicted when a variable is created. You can immediately run:
```
render_table(spec='{"top":"NewVar(cwf\\Total\\;*)","side":"","useWeight":true}')
```

## Multi-Response Variables

For variables where respondents can select multiple answers:

```python
variable = {
    "name": "BrandsUsed",
    "description": "Which brands have you used?",
    "codes": {"1": "Brand A", "2": "Brand B", "3": "Brand C"},
    "dataType": "multi",
    "values": [
        [1, 2],       # Respondent 1: Brand A and B
        [3],           # Respondent 2: Brand C only
        None,          # Respondent 3: missing
        [1, 2, 3],     # Respondent 4: all three
    ]
}
```

## Common Patterns

### Recoded Variable (collapse codes)
```python
# Collapse Age into broader groups
recode_map = {1: 1, 2: 1, 3: 2, 4: 2, 5: 3, 6: 3}  # original -> new
results = [recode_map.get(v) if v is not None else None for v in age_values]
codes = {"1": "Young (18-34)", "2": "Middle (35-54)", "3": "Older (55+)"}
```

### Binary Flag Variable
```python
# Flag respondents who meet a criterion
results = []
for v in source_values:
    if v is None:
        results.append(None)
    elif v in {1, 2}:
        results.append(1)
    else:
        results.append(2)
codes = {"1": "Yes", "2": "No"}
```

### Score/Index Variable
```python
# Combine multiple variables into a score
# Scores are stored as integer codes (bands/buckets), not raw numbers
score_bands = []
for i in range(total_cases):
    score = compute_score(df.iloc[i])
    if score is None:
        score_bands.append(None)
    elif score <= 3:
        score_bands.append(1)
    elif score <= 6:
        score_bands.append(2)
    else:
        score_bands.append(3)
codes = {"1": "Low (1-3)", "2": "Medium (4-6)", "3": "High (7-10)"}
```

### Segmentation Clusters
```python
# After running K-means or similar clustering
from sklearn.cluster import KMeans

# ... clustering code ...
labels = kmeans.labels_  # 0-indexed numpy array

# Convert to 1-indexed codes
results = [int(label) + 1 for label in labels]
codes = {str(i+1): f"Segment {i+1}" for i in range(n_clusters)}
```

### Derived from Multi-Response Source Variable

When the source variable is multi-response (values are lists like `[1,3,5]`), handle the list type:

```python
# Example: create a binary flag "uses any premium brand" from a multi-select brands variable
# Multi-response values are lists: [1,3], [2], None, [1,2,3]
premium_codes = {3, 5, 7}  # codes for premium brands

results = []
for v in brands_values:
    if v is None:
        results.append(None)
    elif isinstance(v, list):
        # Check if ANY of the selected codes are premium
        if any(code in premium_codes for code in v):
            results.append(1)  # Uses premium
        else:
            results.append(2)  # No premium
    else:
        # Single value (shouldn't happen for multi but handle gracefully)
        results.append(1 if v in premium_codes else 2)
codes = {"1": "Uses premium brand", "2": "No premium brand"}
```

### Net/Combined Variable from Multiple Sources

```python
# Combine awareness across multiple variables into a single "any awareness" flag
df = to_dataframe(
    '/workspace/data/casedata_sponaware.json',
    '/workspace/data/casedata_promptedaware.json'
)

results = []
for _, row in df.iterrows():
    spon = row.get('sponaware')
    prompted = row.get('promptedaware')
    if spon is None and prompted is None:
        results.append(None)
    elif (spon is not None and spon == 1) or (prompted is not None and prompted == 1):
        results.append(1)  # Aware (spontaneous or prompted)
    else:
        results.append(2)  # Not aware
codes = {"1": "Any awareness", "2": "Not aware"}
```

## Weighting Considerations

Created variables **do not carry their own weight**. When cross-tabulated, they use the job's existing weight variable (if `useWeight: true` is set in the table spec). This means:

- Your derivation logic should use **unweighted** case data (which is what `pt casedata` provides)
- The resulting variable will be weighted automatically when tabulated
- If the derivation logic itself needs to be weighted (e.g. computing a weighted mean to set thresholds), download the weight variable too and use it in your computation, but still write one value per respondent

```python
# Example: derive groups based on weighted thresholds
df = to_dataframe('/workspace/data/casedata_score.json',
                  '/workspace/data/casedata_weight.json')

# Compute weighted median for threshold
import numpy as np
valid = df.dropna(subset=['score', 'weight'])
weighted_median = np.average(valid['score'], weights=valid['weight'])

# But write unweighted per-respondent values
results = []
for _, row in df.iterrows():
    if row['score'] is None:
        results.append(None)
    elif row['score'] <= weighted_median:
        results.append(1)
    else:
        results.append(2)
codes = {"1": "Below median", "2": "Above median"}
```

## Working with Filtered Data

When deriving a variable from a filtered subset of respondents (e.g. "only people who answered Yes to Q1"), the values array must still cover ALL respondents. Set `null` for respondents outside your filter.

```python
# WRONG: filtered DataFrame has fewer rows than total cases
df_filtered = df[df['Q1'] == 1]  # 2000 rows out of 5000
results = [compute(row) for _, row in df_filtered.iterrows()]
# results has 2000 entries -- server will REJECT this (case count mismatch)

# CORRECT: iterate ALL respondents, null for non-matching
total_cases = len(df)
results = []
for _, row in df.iterrows():
    if row['Q1'] is None or row['Q1'] != 1:
        results.append(None)  # Outside filter -> null
    else:
        results.append(compute(row))
# results has 5000 entries -- correct alignment
assert len(results) == total_cases
```

### Deriving from an Existing Table's Filter

If creating a variable based on analysis "from a table", extract the table's filter and caseFilter first:

```bash
pt table "Tables/My Table" --format json
# Look for "filter" and "caseFilter" fields in the spec
```

Then download the filter variables and apply the same logic in Python:
1. Download case data for the filter variable(s)
2. Apply the filter + caseFilter logic to identify which respondents are in scope
3. Compute values for in-scope respondents, `null` for out-of-scope
4. The result array must have `total_cases` entries

## Verification After Creation

**Always verify after creating a variable.** Run a count-based frequency table through Carbon:

```
render_table(spec='{"top":"count","side":"newvar(cwf\\Total\\;*)"}', title="Verify newvar")
```

Check three things:
1. **Total base** matches the expected case count (from `pt variable count`)
2. **Code frequencies** are plausible (no unexpected zeros, percentages make sense)
3. **NES row** shows a reasonable number of missing cases

If the table shows unexpected results, the variable data is misaligned. See Troubleshooting below.

## Troubleshooting

### "values array has N entries but job has M cases"

The server validates that `len(values)` matches the job's total case count. Fix:
1. Run `pt variable count` to get the correct count
2. Ensure your computation produces exactly that many values
3. Common cause: filtering the DataFrame before computing values (see "Working with Filtered Data" above)

### Variable created but cross-tab shows wrong numbers

The variable was written with misaligned data (from before server-side validation was added, or from an overwrite). Fix:
1. Download the source variables again with `pt casedata`
2. Recompute the values, verifying alignment
3. Recreate with `overwrite: true`

### Variable created but cross-tab returns an error

Carbon can't find the variable. Possible causes:
- Variable name is case-sensitive -- verify exact name with `pt variable list`
- The MET or CD file is malformed -- check with `pt variable info <name>`
- Carbon cache wasn't evicted properly -- this is automatic but try creating the variable again with `overwrite: true`

### 409 Conflict: variable already exists

A variable with that name already exists. Either:
- Choose a different name
- Set `overwrite: true` to replace it (verify your data first)

### Multi-response values appearing as single codes

If your multi-response variable shows single codes instead of multiple selections, check:
- `dataType` must be `"multi"` (not `"single"`)
- Values must be arrays: `[[1,2], [3]]` not `[1, 3]`

## PT Commands Reference

```bash
pt variable count                   # Total case count for the job (use before creating)
pt variable list                    # List all variable names in the job
pt variable info <name>             # Show variable metadata (codes, description, cases)
pt variable create                  # Create variable from JSON on stdin
pt variable create --from-file f    # Create variable from JSON file
pt construct <var1,var2,...>         # Construct variables from constructor definitions
pt construct --all                  # Construct all constructable variables
```
