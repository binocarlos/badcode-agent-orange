---
name: table-management
description: Batch create, copy, and regenerate CBT tables in the TOC using pt toc commands
triggers:
  - batch generate tables
  - copy tables
  - regenerate tables
  - create tables for all variables
  - toc generate
  - batch create cross-tabs
keywords: [batch generate, batch copy, regenerate, TOC, CBT, create tables, copy tables, table management, folder, toc generate, new tables]
---

# Table Management -- Batch Generate and Copy

## When to Use
- User asks to create multiple tables at once ("generate tables for X with Y on top")
- User asks to copy tables from one folder to another with changes
- User asks to regenerate tables with a different top axis, filter, or weight
- User references batch table operations or TOC folder management
- User wants to create a new set of cross-tabs for a set of variables

> **For running existing tables**, see the **table-access** skill.
> **For syntax reference**, see the **carbon-syntax** skill.
> **For default table patterns**, see the **platinum-tables** skill.

## Key Concepts

### TOC Structure
Tables live in the TOC (Table of Contents) under two roots:
- **Tables/Exec/** -- shared tables visible to all users (requires Manager role)
- **Tables/User/<email>/** -- personal tables for a specific user

Each table is a `.cbt` file containing a `[Spec]` section (top, side, filter, weight) and a `[Props]` section (display settings, description).

### Batch Generate vs. Batch Copy
- **Batch Generate**: Create new tables from scratch. You provide a shared top axis and pick individual side variables. Each side variable becomes one table.
- **Batch Copy/Regenerate**: Read existing tables from a source folder, optionally change the top/filter/weight, and save as new tables in a destination folder.

## Workflow 1: Batch Generate Tables

Use when the user says things like "generate awareness tables for all brands with Gender on top".

### Step 1: Discover Variables

```bash
pt search "awareness"
pt vars brandawareness
```

Verify every variable name exists before proceeding. Never guess variable names.

### Step 2: Plan the Tables

Determine:
- **Top axis**: The shared banner variable (e.g., `Gender(cwf\Total\;*)`)
- **Side variables**: One per table (e.g., `brandawareness`, `brandconsideration`)
- **Filter** (optional): Case filter expression
- **Weight** (optional): Weight variable
- **Naming**: Table names (default: variable name)
- **Destination folder**: Folder path within exec or user

### Step 3: Preview and Confirm

Before generating, show the user exactly what will be created using `ask_user`:

```
I'll generate 5 tables in Tables/Exec/Gender Breaks:

| # | Table Name         | Side Variable       |
|---|--------------------|---------------------|
| 1 | Gender_BrandA      | brandawareness_a    |
| 2 | Gender_BrandB      | brandawareness_b    |
| 3 | Gender_BrandC      | brandawareness_c    |
| 4 | Gender_BrandD      | brandawareness_d    |
| 5 | Gender_BrandE      | brandawareness_e    |

Shared settings:
- Top: Gender(cwf\Total\;*)
- Weight: WeightCombo()
- Filter: (none)

Shall I proceed?
```

**Always wait for explicit confirmation before generating.**

### Step 4: Generate

**Simple flag mode** (one table per variable name):
```bash
pt toc generate --top 'Gender(cwf\Total\;*)' --side 'brandaware,consider,usage' \
  --folder "Gender Breaks" --weight "WeightCombo()" --use-weight
```

**JSON mode** (full control over names, descriptions, subfolders):
```bash
echo '{
  "tables": [
    {"name": "Gender_BrandA", "side": "brandawareness_a(cwf\\Total\\;*;nes)", "description": "Brand A Awareness"},
    {"name": "Gender_BrandB", "side": "brandawareness_b(cwf\\Total\\;*;nes)", "description": "Brand B Awareness"}
  ],
  "top": "Gender(cwf\\Total\\;*)",
  "weight": "WeightCombo()",
  "useWeight": true,
  "destination": "exec",
  "execFolder": "Gender Breaks"
}' | pt toc generate
```

**JSON escaping**: Backslashes in axis syntax must be double-escaped in JSON strings. `cwf\Total\;*` becomes `cwf\\Total\\;*` in JSON.

**Side axis formatting**: Each side variable should use the standard pattern `variable(cwf\Total\;*;nes)` to include base, all codes, and NES diagnostic. If the user specifies custom side syntax (e.g., specific codes only), use that instead.

### Step 5: Verify

Check the response for generated and failed tables:
```json
{
  "generated": [{"name": "Gender_BrandA", "path": "Tables/Exec/Gender Breaks/Gender_BrandA.cbt"}],
  "failed": [],
  "count": 2
}
```

If any tables failed, report the failures to the user and offer to retry.

Optionally verify a sample table renders correctly:
```bash
pt table "Tables/Exec/Gender Breaks/Gender_BrandA"
```

## Workflow 2: Batch Copy / Regenerate Tables

Use when the user says things like "copy the Awareness folder but change the top to AgeGroup".

### Step 1: Read Source Folder Specs

```bash
pt toc folder-specs "Awareness"
# or with full path:
pt toc folder-specs "Tables/Exec/Awareness"
# include subfolders:
pt toc folder-specs "Awareness" --recursive
```

For JSON output (needed for programmatic manipulation):
```bash
pt toc folder-specs "Awareness" --format json
```

This returns all CBT specs from the folder, including top, side, filter, weight, and description for each table.

### Step 2: Determine Overrides

For each spec field, decide the override mode:

| Field | Modes | Description |
|-------|-------|-------------|
| **top** | `keep` / `replace` / `remove` | Keep original, replace with new value, or clear |
| **filter** | `keep` / `replace` / `append` / `remove` | Keep, replace, AND-append, or clear |
| **weight** | `keep` / `replace` / `remove` | Keep original, replace with new value, or clear |
| **caseFilter** | `keep` / `replace` / `remove` | Keep original, replace with new value, or clear |

**Override resolution:**
- `keep` -- use original value unchanged
- `replace` -- substitute entirely with new value
- `remove` -- set to empty string
- `append` (filter only) -- if both exist: `(original) & (newValue)`, otherwise whichever is non-empty

**Side axis is always kept from the source.** The side is what makes each table unique.

### Step 3: Build Resolved Specs

For each source table, resolve the final spec:
```python
# Example resolution logic (write as Python script for complex batches)
import json, sys

# Load source specs
specs = json.loads(subprocess.check_output(['pt', 'toc', 'folder-specs', 'Awareness', '--format', 'json']))

new_top = "ageband1(cwf\\Total\\;*)"
tables = []
for t in specs['tables']:
    tables.append({
        "name": f"Age_{t['name']}",
        "side": t['spec']['side'],
        "description": t.get('description', ''),
        "subFolder": t.get('relativePath', '')
    })

payload = {
    "tables": tables,
    "top": new_top,
    "filter": specs['tables'][0]['spec'].get('filter', ''),
    "weight": specs['tables'][0]['spec'].get('weight', ''),
    "useFilter": bool(specs['tables'][0]['spec'].get('filter')),
    "useWeight": bool(specs['tables'][0]['spec'].get('weight')),
    "destination": "exec",
    "execFolder": "Age Breaks"
}

json.dump(payload, sys.stdout)
```

Then pipe to pt toc generate:
```bash
python3 build_specs.py | pt toc generate
```

### Step 4: Preview and Confirm

Show the user the resolved specs before generating:
```
I'll copy 5 tables from Tables/Exec/Awareness to Tables/Exec/Age Breaks:

| # | Source              | New Name            | Top (changed)          |
|---|---------------------|---------------------|------------------------|
| 1 | BrandA_Awareness    | Age_BrandA_Awareness | ageband1(cwf\Total\;*) |
| 2 | BrandB_Awareness    | Age_BrandB_Awareness | ageband1(cwf\Total\;*) |

Changed: top replaced with ageband1(cwf\Total\;*)
Kept: filter, weight, caseFilter, side

Shall I proceed?
```

### Step 5: Generate and Verify

Same as Workflow 1 Steps 4-5. Use `pt toc generate` with the resolved payload.

## Command Reference

### pt toc list [folder]
List TOC folders and tables at a specific path.

```bash
pt toc list                          # root-level items
pt toc list "Brand Health"           # children of Brand Health
pt toc list "Brand Health/Awareness" # nested folder
pt toc list --format json            # JSON output
```

### pt toc read <path>
Read a CBT file's settings (top, side, filter, weight, visibility, description).

```bash
pt toc read "Brand Health/Awareness"                     # shorthand
pt toc read "Tables/Exec/Brand Health/Awareness.cbt"     # full path
pt toc read "Brand Health/Awareness" --format json       # JSON output
```

### pt toc folder-specs <folder>
Get all table specs from a TOC folder. Critical for copy/regenerate workflows.

```bash
pt toc folder-specs "Awareness"              # shorthand
pt toc folder-specs "Awareness" --recursive  # include subfolders
pt toc folder-specs "Awareness" --format json
```

### pt toc generate
Batch create CBT tables and register in TOC.

**Flag mode:**
```bash
pt toc generate --top "Total" --side "var1,var2,var3" \
  --folder "My Folder" --destination exec
```

**JSON mode (stdin):**
```bash
echo '{"tables":[...],"top":"...","destination":"exec","execFolder":"..."}' | pt toc generate
```

See Workflow 1 Step 4 for full JSON format.

### pt toc save
Save a single CBT file and register it in the TOC (pre-existing command).

```bash
echo '{"content":"[Spec]\nTop=...","folder":"Charts","name":"My Table"}' | pt toc save
```

### pt toc delete <path>
Delete a table from the TOC and remove the CBT blob.

```bash
pt toc delete "Old Folder/OldTable"
pt toc delete "Tables/Exec/Old Folder/OldTable.cbt"
```

## Error Handling

### Partial Batch Failures
The batch-generate endpoint processes tables individually. Some may succeed while others fail. The response includes both `generated` and `failed` arrays.

When failures occur:
1. Report which tables succeeded and which failed
2. Show the error message for each failure
3. Offer to retry just the failed tables

### Variable Not Found
Generation only writes CBT files -- it does not validate axis syntax against the variable tree. Invalid variables are caught when the table is first run, not at generation time.

**Prevention:** Always verify variables exist with `pt vars <name>` before building specs.

### Permission Denied for Exec
Saving to `destination: "exec"` requires Manager, Admin, or God role. If 403, inform the user and suggest `"user"` instead.

### Name Collisions
If a CBT with the same name already exists, batch-generate overwrites it silently. Warn the user if generating into a folder that already has tables.

## Rules

1. **Always verify variables** exist with `pt vars` or `pt search` before generation
2. **Always preview and confirm** with `ask_user` before executing batch operations
3. **Use standard side syntax** `variable(cwf\Total\;*;nes)` unless user specifies otherwise
4. **Report partial failures** explicitly -- never ignore failed tables
5. **Set useFilter/useWeight correctly** -- only `true` when the value is non-empty
6. **Preserve side and subFolder** when copying/regenerating tables
7. **Warn before overwriting** existing tables at the destination
8. **Double-escape backslashes** in JSON strings: `cwf\Total\;*` becomes `cwf\\Total\\;*`
9. **Use Python for complex batches** -- for 10+ tables with varying overrides, write a Python script to build the JSON payload rather than constructing it inline in bash
