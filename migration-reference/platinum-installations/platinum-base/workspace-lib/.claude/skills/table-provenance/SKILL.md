---
name: table-provenance
description: Spec discipline for any generated table — five required components (top, side, filter, weight, caseFilter) and a written spec summary the user must verify before downstream use
triggers:
  - spec sign-off
  - verify table spec
  - what filter is applied
  - all five spec components
  - spec summary
  - confirm weight and filter
keywords: [provenance, spec, filter, weight, casefilt, caseFilter, all answering, summary, verify, sign-off, table credibility, defaults]
---

# Table Provenance — Spec Discipline

## When to Use
- BEFORE running ANY `render_table`, `pt query`, `pt toc generate`, or other tabulation call
- BEFORE building a webapp, dashboard, PPTX, or report from generated tables
- When the user asks for a chart, KPI, or any number derived from survey data
- After a batch of tables has been generated — to produce the spec summary for sign-off

> Loaded together with `platinum-tables` (axis defaults) and `carbon-syntax` (full reference).
> Webapps must also follow `data-visualization` — which requires a Data Reference Document referencing the spec summary produced here.

## Why This Skill Exists

Numbers presented to the client must be **traceable, reproducible, and consistent with the rest of the project**. Tables that silently drop the filter, use the wrong weight, or include non-respondents have caused real credibility incidents. Every table the agent generates must explicitly account for all five spec components, and the user must be shown the specs before the data is presented as fact.

## The Five Required Components

Every generated table — ad-hoc or from a TOC — has five components. None of them are optional. If the agent does not specify a component, Carbon will pick a default, and that default is rarely what the client expects.

| # | Component | Field name | What it controls |
|---|-----------|------------|------------------|
| 1 | **Top axis** | `top` | The banner / column variable (or `count(1)` for a single column) |
| 2 | **Side axis** | `side` | The stub / row variable (the thing being measured) |
| 3 | **Filter** | `useFilter` (+ job default) or explicit `filter` | Which respondents are included at the survey level (e.g. `prospects`, `category buyers`) |
| 4 | **Weight** | `useWeight` (+ job default) or explicit `weight` | Which weight is applied for representativeness |
| 5 | **Case filter** | `caseFilter` | Per-table case-level filter, layered on top of the survey filter (e.g. "answered the side variable") |

> Five components map directly to a generated PowerPoint / webapp chart. If you cannot describe a chart's number using these five fields, do not present that number.

## Default: Filter by "All Answering"

When the user has not specified a case filter, **default to filtering on responders to the side variable** — known as "all answering". This makes percentages a share of those who actually answered the question, not the entire sample (which silently lowers every percentage when the question was filtered or had drop-outs).

Apply it as the case filter:

```json
{
  "top": "count(1)",
  "side": "promptedawareness(cwf\\Total\\;*;nes),\\Which brands are you aware of?\\",
  "useWeight": true,
  "useFilter": true,
  "caseFilter": "promptedawareness(*)"
}
```

`side_variable(*)` selects every code of the side variable, so the case filter resolves to "respondents who gave any valid answer to that variable".

### When NOT to default to all-answering

| Situation | What to do |
|-----------|-----------|
| The user explicitly asks for a different base ("among prospects", "among 18-24s") | Use that as the case filter and call it out in the spec summary |
| The TOC table you are running already has a `caseFilter` | Keep the original — never silently strip it |
| You are doing penetration analysis ("% of total who…") | Omit the case filter and state "Base = total sample" in the spec summary |
| The variable is multi-coded with NES already shown | All-answering still applies; NES will appear as a diagnostic row |

When you do something other than the all-answering default, **say so** in the spec summary so the user can challenge it.

## Mandatory Workflow

### 1. Choose source

- Search the TOC first (see `table-access` and `data-provenance`). If a curated table exists, prefer it and copy its spec verbatim — do NOT reconstruct the spec.
- If you must generate ad-hoc, set ALL five components deliberately. Never accept Carbon defaults silently.

### 2. Verify variable names

`pt vars <name>` for every variable used on top, side, filter, weight, or caseFilter. Variable names are lowercase. Guessing is not allowed.

### 3. Run the table

Use `render_table` (preferred) or `pt query`. Always set `useWeight: true` and `useFilter: true` unless you have a documented reason not to.

### 4. Write the Spec Summary

After generating a table — or a batch of tables — write a markdown file at `/workspace/data/spec-summary.md` (append if it exists) and register it as an artifact:

```
register_artifact(
  file_path="data/spec-summary.md",
  label="Table Spec Summary",
  description="Specs for every table used in this analysis — please review",
  artifact_type="file"
)
```

Then call `ask_user` with a confirmation question: "Here are the specs for the tables I'm using. Please confirm they're correct, or tell me what to change."

**Do not proceed to the webapp/PPTX/report build until the user responds.**

### 5. Spec Summary Format

One row per table. Use this exact markdown structure so the user can scan it quickly:

```markdown
# Table Spec Summary

_Generated 2026-04-17 for session <session-id>_

| # | Dataset | Top | Side | Filter | Weight | Case filter | Source |
|---|---------|-----|------|--------|--------|-------------|--------|
| 1 | `awareness` | `count(1)` | `promptedawareness(cwf\Total\;*;nes)` | job default (`prospects`) | job default (`weight1`) | `promptedawareness(*)` (all answering) | TOC: Brand/Aided Awareness |
| 2 | `consideration` | `Gender(cwf\Total\;*)` | `consideration(cwf\Total\;*;nes)` | job default | job default | `consideration(*)` (all answering) | ad-hoc |

## Notes
- Table 2 is ad-hoc — no curated TOC version exists. If you have an authoritative version, point me at it and I will swap.
- All percentages are column percentages of those who answered the side variable.
```

If a column would be empty (e.g. no caseFilter), say "(none — base = total sample)" — never leave it blank, since blank looks like a forgotten field.

## Rules

1. **Five components, every time.** Top, side, filter, weight, caseFilter. None of them implicit.
2. **Default caseFilter to `side_variable(*)`** unless the user has asked otherwise or the TOC spec dictates differently.
3. **Use `useWeight: true` and `useFilter: true`** unless you have a documented reason not to. Document the reason in the spec summary.
4. **Preserve TOC specs verbatim.** When running an existing table, do not modify filter/weight/caseFilter without asking the user — and call out the change in the spec summary.
5. **Generate `data/spec-summary.md` for every analysis.** Append to it as you add tables. Register it as an artifact.
6. **Stop and `ask_user` for sign-off** on the spec summary before building any webapp, dashboard, PPTX, or report.
7. **Never present a number you cannot fill into the five-component table.** If you cannot, you do not understand the data — fix that before writing the headline.
8. **No need to export every table to Excel** to verify the percentages — the spec summary plus the rendered cross-tab output is enough for the user to sanity-check.
