---
name: carbon-syntax
description: Cross-tabulation axis syntax reference for Carbon table specifications
triggers:
  - carbon syntax
  - axis syntax
  - how do i write a filter
  - code range syntax
  - nesting in carbon
  - net syntax
keywords: [carbon, syntax, axis, filter, cross-tab, tabulation, spec, codes, variables, nets]
---

# Carbon Cross-Tabulation Syntax

## When to Use
- When building cross-tabulation specs (top/side/filter expressions)
- When constructing axis expressions with variables, codes, nets, or statistics
- When you need to recall Carbon syntax for ranges, exclusions, nests, or filters

> **For default table patterns** (what to put on top/side axis, which metric to use), see the **platinum-tables** skill. This skill covers *syntax* — how to write expressions.

## Syntax Reference

### Axis Syntax (top/side expressions)

**Variables and codes:**
- `Gender` — all codes of the variable
- `Gender(1)` — single code
- `Gender(1/2)` — code range 1 through 2 (expands to codes 1, 2)
- `Age(1/5)` — code range 1 through 5 (expands to codes 1, 2, 3, 4, 5)
- `Age(1;3;7)` — specific individual codes 1, 3, and 7 (semicolons inside parentheses)
- `Variable(*)` — all codes explicitly
- `Variable(*~99)` — all codes except 99 (exclude "Don't know" / "Refused")

**Combining variables on an axis:**
- `Gender,Age` — comma: both variables listed sequentially on the axis
- `Gender*Age` — asterisk: cross/nest the two variables (every combination)

> **Important — delimiters:**
> - **Commas** separate items on an axis: `Gender,Age,-Spacer,Variable(1/3)`
> - **Semicolons** separate codes inside parentheses: `Age(1;3;7)`
> - **Slash** is the range operator: `Age(1/5)` means codes 1 through 5
> - Do NOT use semicolons between variables — they will not be parsed correctly
> - Do NOT use hyphens as ranges — `Age(1-5)` does not work, use `Age(1/5)`

**Labels and spacers:**
- `Gender(1\Male\)` — override label for code 1
- `Gender\Custom Header\` — override variable header label
- `-Section Title` — spacer row/column with title text

**Statistics pseudocodes (use on an axis like variables):**
- `tot` — total/unweighted base
- `cwf` — weighted filtered base (use inside a variable's code list for a base row: `Gender(cwf;1;2;3)`)
- `avg` — mean
- `med` — median
- `std` — standard deviation
- `min` / `max` — minimum / maximum

**Nets and arithmetic:**
- `Variable(_netname)` — specific net by name
- `Variable(_)` — all defined nets
- `Variable(#c1+c2)` — arithmetic: sum of codes (also `-`, `*`, `/`)
- `Variable(#)` — all defined arithmetic expressions

**Hierarchic variables (colon-separated levels):**
- `Product(1/3:A/C:X/Z)` — level 1 codes 1-3, level 2 A-C, level 3 X-Z

**Inline filter/weight overrides:**
- `{Region(1)},Gender,{},Age` — filter Gender by Region=1, then unfilter for Age
- `[WeightVar],Gender` — apply weight to subsequent items

**NOT operator (filter to exclusions / prospects):**
- `!BrandUsage(33)` — NOT: cases who do NOT have code 33 (e.g., non-users of brand 33)
- `!Variable(code)` — the `!` prefix negates the condition

**Per-dimension casefilters (embedded in side axis):**
Saved table specs may embed a casefilter per row in the side axis. Each brand row uses a different filter:
```
!BrandUsage from all questions & categories(Sky)  → filters to Sky prospects
!BrandUsage from all questions & categories(Netflix)  → filters to Netflix prospects
```
These are part of the `caseFilter` field in the .cbt spec, not the main `filter`.

**Time-restricted entries:**
- `Data Processing calendar month(36/9999)&` — restricts to data from month 36 onwards (for brands not tracked from the start)
- The `&` at the end chains with the next condition

### Filter Syntax (filter field)

- `Gender(1)` — single condition
- `Gender(1)&Age(1/3)` — AND: both conditions must hold
- `Gender(1)|Age(1)` — OR: either condition
- `!Variable(code)` — NOT: exclude cases matching this condition

### CarbonSpec JSON

```json
{
  "top": "AxisExpr",
  "side": "AxisExpr",
  "filter": "FilterExpr",
  "weight": "WeightVar",
  "useFilter": true,
  "useWeight": true
}
```

## Analysis Strategy

1. **Start broad**: run key variables with all codes to see the distribution
2. **Compare segments**: cross demographics (Gender, Age, Region) against metrics
3. **Exclude noise**: use `*~99` to drop "Don't know" / "Refused" / "N/A" codes
4. **Check bases**: include `cwf` or `tot` to verify base sizes (need >30 for reliable %)
5. **Aggregate with nets**: use `Variable(_)` to collapse small codes into meaningful groups
6. **Drill down**: interesting broad finding → filter or select specific codes for detail
7. **Always verify variable names** with `pt vars` / `pt search` before building specs

## Rules
- Do NOT use semicolons between variables — only inside parentheses to separate codes
- Do NOT use hyphens as ranges — use slash (`Age(1/5)` not `Age(1-5)`)
- Always verify variable names with `pt vars` / `pt search` before building specs
