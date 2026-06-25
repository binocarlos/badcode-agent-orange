---
name: table-access
description: Access existing table specs, run saved tables, multi-source table discovery, and apply customer design templates
triggers:
  - find existing tables
  - run saved table
  - toc
  - reuse a table
  - apply customer template
  - run a table from the folder
keywords: [table access, TOC, spec, cbt, existing tables, reuse, design template, branding, customer template]
---

# Table Access & Design Templates

## When to Use
- When you need to find and reuse existing table specifications
- When running a saved table with modifications (different filter, weight, or banner)
- When applying customer branding or design templates to outputs
- When the user references a specific table by name or folder

> **For syntax reference**, see the **carbon-syntax** skill. **For default patterns**, see the **platinum-tables** skill.

## Multi-Source Table Discovery

The TOC can be **incomplete** — some table folders may be hidden, truncated, or not indexed. **Never assert a table doesn't exist** — only say "I couldn't find it in the sources I checked."

### Discovery workflow (use all three):

1. **`pt tables`** — browse the full TOC hierarchy
2. **`pt search "<topic>"`** — semantic search across tables, variables, and conversations
3. **Directory paths** — if you know a folder path (e.g., `Tables/Exec/PPTX Reports/TV/`), try accessing it directly via `pt table`

If TOC search returns nothing but the user says tables exist, try alternative folder paths or ask the user for the exact location.

## Reading Table Specs

`pt table <path>` returns the full saved specification:

```
pt table "Brand Tracking/Aided Awareness by Region"
```

Returns these fields from the .cbt `[spec]` section:
- `top` — top axis syntax
- `side` — side axis syntax
- `filter` — case filter expression
- `weight` — weight variable (e.g., `WeightCombo()`)
- `caseFilter` — additional per-dimension case filter
- `description` — human-readable description

### Key syntax patterns in saved specs

- `!Variable(code)` — NOT operator (e.g., `!BrandUsage_Total(33)` = non-users of brand 33)
- Per-brand casefilters embedded in the side axis
- Time-restricted entries using `DataProcessingMonth(36/9999)&`
- Filters can differ between tables in the same set

## Running Saved Tables

**Never reconstruct a table spec from scratch when a saved spec exists.** Use the existing spec verbatim:

1. `pt table "<path>"` — read the spec
2. Run it exactly as-is, or with one targeted modification
3. If modifying: **preserve the original filter, weight, and caseFilter by default**
4. Confirm with `ask_user` before changing any inherited settings — default answer is YES (keep them)

## Mandatory User Verification

1. Search for tables → show user exact matches found → **wait for confirmation**
2. Render the table → show summary of what was retrieved → **wait for confirmation**
3. Check weight/filter/caseFilter are correctly applied → surface to user
4. **Only then** build visualisations

## Customer Branding

### Ask for brand guidelines first

Use `ask_user` to request: primary/secondary colours (hex), logo, font preferences.

### Cross-chart colour consistency

When multiple charts show the same brands, each brand must have a **fixed colour** across all charts. The customer's own brand gets the customer's brand colour (e.g., Sky = Sky Blue #0072c9).

```python
BRAND_COLORS = {
    'Sky': '#0072c9',
    'Netflix': '#E50914',
    'Virgin': '#CC0000',
    # ... customer confirms palette
}
```

### Dark backgrounds

Dark backgrounds require: light/white text, no chart gridlines, high-contrast accent colours.

## Common Mistakes

| Wrong | Right | Why |
|-------|-------|-----|
| "This table doesn't exist" | "I couldn't find it in the sources I checked" | TOC can be incomplete |
| Rebuilding a spec from scratch | Use `pt table` to get the existing spec | Saved specs are curated and validated |
| Changing filter/weight without asking | Preserve inherited settings, confirm changes | May invalidate previous analysis |
| Different brand colours per chart | Fixed colour per brand across all charts | Users track brands visually |

## Rules

1. Always use multi-source discovery: `pt tables` + `pt search` + direct paths
2. Never assert a table doesn't exist — only that you couldn't find it
3. Never reconstruct a saved spec — use it verbatim via `pt table`
4. Preserve filter/weight/caseFilter by default when modifying existing tables
5. Confirm every change with `ask_user` before proceeding
6. Wait for explicit user confirmation at each verification step
7. Maintain consistent brand colours across all charts in a report
