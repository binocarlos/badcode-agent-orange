---
name: cross-job-analysis
description: Analyse and compare data across multiple jobs within a customer, with intelligent job discovery
triggers:
  - compare across jobs
  - cross-job analysis
  - tracker trend
  - multiple waves
  - compare leavers and joiners
  - trend across studies
keywords: [cross-job, multi-job, comparison, tracker, wave, jobs, combine, benchmark]
---

# Cross-Job Analysis

## When to Use
- User wants to compare data across multiple jobs (trackers, waves, surveys)
- User asks about trends across time periods or study waves
- User references multiple jobs by name ("compare leavers and joiners")
- User asks "what does X look like across all our studies?"

> **For single-job analysis**, see the **research** and **platinum-tables** skills.

## Job Discovery

### List and search jobs

```
pt jobs                           # All jobs for this customer
pt search "brand equity"          # Searches across ALL jobs by default (results include job names)
```

**`pt search` searches across all jobs by default** — results include the job name alongside each match.

### Conversational job discovery

When the user mentions job names loosely ("compare leavers and joiners"):
1. Search for matching jobs
2. Present 2-4 matches via `ask_user` with checkboxes — **do NOT assume and proceed**
3. Wait for explicit confirmation before querying

### Expanding scope mid-conversation

Users who start in a single job can expand conversationally: "I wonder what Leavers think about this too?" — confirm the new scope via `ask_user`, then include the additional job.

## Cross-Job Query Pattern

Run the same query against multiple jobs using `--job`:

```bash
pt query --job "brand-equity-ireland" --top "count(1)" --side "Consideration(cwf\\Total\\;*;nes)"
pt query --job "ireland-leavers-2026" --top "count(1)" --side "Consideration(cwf\\Total\\;*;nes)"
```

### Variable harmonisation

Variables may have different names across jobs:
1. Run `pt vars` for each job to list variables
2. Use `pt search` to find equivalents
3. If names differ, map them and **confirm with `ask_user`**:

```python
var_map = {
    'brand-equity-ireland': 'BrandConsideration',
    'ireland-leavers-2026': 'Consideration_Q3',
}
```

### Combining results

```python
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe
import pandas as pd

frames = {
    'Brand Equity': to_dataframe('/workspace/data/consideration_be.json'),
    'Leavers': to_dataframe('/workspace/data/consideration_leavers.json'),
}
combined = pd.concat(frames, axis=1)
```

## Saving Artifacts in Multi-Job Context

- **Tables/variables** → save back to each job individually (specs differ per job)
- **Combined visualisations** → cannot save to one job. Include source metadata:

```python
source_meta = {
    'type': 'composite',
    'sources': [
        {'job': 'brand-equity-ireland', 'variable': 'BrandConsideration', 'table': 'ad-hoc'},
        {'job': 'ireland-leavers-2026', 'variable': 'Consideration_Q3', 'table': 'ad-hoc'},
    ]
}
```

Keep source references in footer/info panel, not cluttering the main display.

## Limitations

> **Multi-job batch query** — not yet available. Must run separate `pt query --job` calls per job.

> **User affinity / personalisation** — not yet available. No user profile system to prioritise preferred jobs. Ask users to specify.

> **Automated variable harmonisation** — not yet available. Manual mapping required.

## Rules

1. Always list available jobs with `pt jobs` before cross-job queries
2. `pt search` searches all jobs by default — use this for cross-job variable discovery
3. Confirm job selection with `ask_user` — never assume which jobs to include
4. Confirm variable mappings with `ask_user` when names differ across jobs
5. Track source job for every data point — label clearly in charts and narratives
6. Note base sizes per job — segments may have very different sample sizes
7. Only within-customer analysis is supported — cannot combine across customers
8. Users can expand from single-job to multi-job scope mid-conversation — always confirm first
