---
name: research
description: Data discovery guidance using pt search, pt vars, and pt tables
triggers:
  - explore the data
  - find variables
  - what variables are available
  - data discovery
  - search the dataset
  - browse variable tree
keywords: [search, discover, variables, browse, explore, data, variable tree, tables]
---

# Data Discovery

## When to Use
- When exploring a dataset for the first time
- When investigating a new topic or looking for relevant variables
- When you need to find variable names, code frames, or existing tables before building specs

## Workflow

When exploring data for the first time or investigating a new topic, **always combine both approaches**:

### 1. Search by Meaning (pt search)

Searches a semantic index of ALL variables — question text, code labels, descriptions, nets, and folder structure. Use natural language queries.

```
pt search "brand awareness"       # find variables related to brand awareness
pt search "demographics"          # find demographic variables
pt search "satisfaction rating"   # find satisfaction measures
pt search "<topic>" --limit 20    # increase results for broad topics
```

The search index contains rich metadata: survey question text, answer code labels, net definitions, folder hierarchy, and derivation info. You can search by topic, answer options, or question wording.

### 2. Browse the Variable Tree (pt vars)

Shows the full project structure — folders, sections, and all variables with their labels.

```
pt vars                # see the full variable tree (folders + variables)
pt vars <name>         # see a variable's codeframe (answer codes/labels)
```

Browsing reveals the overall shape of the dataset: what sections exist, how variables are organized, and what's available beyond what search might surface.

### Combined Discovery Workflow

**Always use search, browse, and tables when exploring a new topic.** Each source reveals different information — together they give a complete picture.

1. **Search**: Use `pt search "<topic>"` — this returns matching variables, tables, AND previous conversations from your team in one call
2. **Browse the variable tree**: Use `pt vars` to see the full project structure — search finds what's relevant, browsing reveals what's available
3. **Check existing tables**: Use `pt tables` / `pt table <path>` to see if relevant tables already exist and inspect their specs
4. **Explore codes**: Use `pt vars <name>` on promising variables to see answer options
5. **Cross-reference**: Search from different angles — by topic, by methodology, by demographic concept
6. **Access raw data**: For custom analysis, download per-respondent data: `pt casedata <var1> [var2...]`
7. **Verify before querying**: Confirm variable names and code values before building cross-tabulations

### Search Tips

- Use short, specific terms (1-3 words): `pt search "demographics"` not `pt search "interesting cross-tabulation demographic analysis"`
- Search multiple times with different angles: `pt search "age"`, `pt search "gender"`, `pt search "income"`
- If a search returns 0 results, try simpler/broader terms or single words
- Combine searches to build understanding rather than trying one perfect query

### Browse Tables (pt tables / pt table)

Use `pt tables` to list pre-built tables (table of contents).
Use `pt table <path>` to preview an existing table's specification and structure.

## Rules
- Always combine search and browse — neither alone gives the full picture
- Use short, specific search terms (1-3 words)
- Always verify variable names with `pt vars <name>` before building cross-tabulations
- Check existing tables before creating new ones from scratch
