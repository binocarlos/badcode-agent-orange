---
name: platinum-tables
description: Default patterns and rules for running cross-tabulations in Carbon/Platinum
triggers:
  - default table structure
  - what goes on the side axis
  - column percentages
  - cwf total
  - base row
  - table defaults
keywords: [tables, cross-tabulation, defaults, side, top, axis, colPct, count, cwf, nes, metric]
---

# Platinum Tables — Default Patterns

## When to Use
- Building any cross-tabulation spec (render_table, render_chart, pt query)
- Deciding what to put on the top or side axis
- Choosing the default metric for display
- Structuring side axis with base, codes, and diagnostics

> **For full syntax reference** (code ranges, nets, filters, nesting), see the **carbon-syntax** skill. This skill covers *defaults* — what to put on each axis and which metric to use.

## Default Table Structure

When a user asks for a table or chart, use these defaults:

```
top: count(1)                                           # Total column (if no banner specified)
side: Variable(cwf\Total\;*;nes),\Question text\        # Base + all codes + NES + label
metric: colPct                                          # Column percentages (not frequencies)
```

## Side Axis Defaults

Always structure the side axis like this:

```
VariableName(cwf\Total\;*;nes),\Full question text here\
```

### Components

| Part | Meaning |
|------|---------|
| `cwf\Total\` | Weighted filtered base, labeled "Total" — shows sample size |
| `*` | All codes of the variable |
| `nes` | "Not Established" — cases without an assigned code (diagnostic) |
| `,\Question text\` | The full question wording as the header label |

### Why include each part

- **`cwf\Total\`** goes FIRST — users need to see the base size for context
- **`*`** shows all answer options
- **`nes`** flags data quality issues: cases that exist but have no code assigned. Could mean missing codes in variable definition, or filter needs adjustment. Always include as a diagnostic signal.

### Example

```
AgeBand1(cwf\Total\;*;nes),\Which of the following age groups do you fall into?\
```

This produces:
- Row 1: Total (base size)
- Rows 2-N: All age band codes
- Final row: NES (if any unassigned cases exist)
- Header: The question text

## Top Axis Defaults

If no banner variable is specified, use `count(1)`.

| User request | Top axis |
|--------------|----------|
| "Show me age group" | `count(1)` |
| "Chart brand awareness" | `count(1)` |
| "Table of gender" | `count(1)` |
| "Age by Gender" | `Gender(cwf\Total\;*)` |
| "Brand by Region" | `Region(cwf\Total\;*)` |

### Why `count(1)` and not `tot` or `cwf`

- `tot` and `cwf` are base statistics — they require a variable to make sense
- You cannot have a "base of nothing"
- `count` is a variable where every case has value `1`
- `count(1)` selects all cases, giving you a single "Total" column

### NEVER use `Total` alone as an axis

**`Total` is not a real variable** — it has no case data file. Using it alone on an axis produces "dummy" labels with zero frequencies. This is the #1 cause of empty/broken tables. Always use `count` or `count(1)` instead:

```
# WRONG — produces "dummy" with 0 frequencies:
render_table(spec='{"top":"myvar","side":"Total"}')

# CORRECT — shows actual frequency counts:
render_table(spec='{"top":"count","side":"myvar(cwf\\Total\\;*)"}')
```

### When a banner IS specified

Include base in the top axis too:

```
top: Gender(cwf\Total\;*)
side: AgeBand1(cwf\Total\;*;nes),\Which of the following age groups do you fall into?\
```

## Default Metric: Column Percentages

Always default to column percentages (`colPct`), not frequencies.

Column % shows distribution within each segment — makes cross-column comparisons meaningful ("25% of males vs 32% of females"). Raw frequencies are hard to interpret when base sizes differ.

| User says | Metric to use |
|-----------|---------------|
| (nothing specified) | `colPct` — **default** |
| "row percentages", "% of each row" | `rowPct` |
| "frequencies", "counts", "raw numbers", "how many" | `freq` |

### In tool calls

**render_chart:** `"data_type": "colPct"`

**Python (to_dataframe):** `df = to_dataframe(path, metric='colpc')`

**JavaScript (getRecords/getMatrix):** `const records = getRecords(name, { metric: 'colpc' });`

## Complete Examples

### Single-variable table ("Show me the age distribution")

```json
{
  "top": "count(1)",
  "side": "AgeBand1(cwf\\Total\\;*;nes),\\Which of the following age groups do you fall into?\\",
  "useWeight": true,
  "useFilter": true
}
```

### Cross-tabulation with banner ("Age by gender")

```json
{
  "top": "Gender(cwf\\Total\\;*)",
  "side": "AgeBand1(cwf\\Total\\;*;nes),\\Which of the following age groups do you fall into?\\",
  "useWeight": true,
  "useFilter": true
}
```

### Chart request ("Chart brand awareness")

```json
{
  "top": "count(1)",
  "side": "BrandAwareness(cwf\\Total\\;*;nes),\\Which brands are you aware of?\\",
  "chart_type": "bar",
  "data_type": "colPct"
}
```

## Common Mistakes

| Wrong | Right | Why |
|-------|-------|-----|
| `"top": "tot"` or `"top": "cwf"` alone | `"top": "count(1)"` | `tot`/`cwf` are base stats that require a variable |
| `"side": "Age(*)"` | `"side": "Age(cwf\\Total\\;*;nes)"` | Missing base — user won't see sample size |
| `"data_type": "freq"` as default | `"data_type": "colPct"` | Only use freq if user explicitly asks for counts |
| Omitting `nes` | Include `nes` at end of side codes | NES is a diagnostic signal for data quality |

## Rules
- Always include `cwf\Total\` as the first element in side axis code lists
- Always include `nes` as the last element in side axis code lists
- Always include the question text label after the side variable
- Default to `count(1)` for top axis when no banner variable is specified
- Default to `colPct` metric unless the user explicitly requests something else
- When a banner variable IS specified, include `cwf\Total\` in the top axis too
- Always set `useWeight: true` and `useFilter: true` unless there is a reason not to
- Verify variable names with `pt vars` or `pt search` before building specs
