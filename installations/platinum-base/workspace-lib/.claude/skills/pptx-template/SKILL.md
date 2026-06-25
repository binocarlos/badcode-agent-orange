---
name: pptx-template
description: Generate PowerPoint reports with native editable charts using the pptx_template library
triggers:
  - powerpoint
  - pptx
  - presentation
  - slide deck
  - export to powerpoint
---

# PowerPoint Template

Generate professional PowerPoint reports with native editable charts using the `pptx_template` library at `/workspace/lib/pptx_template.py`.

## Two Workflows

### Without a Reference Template (Default Platinum Style)

```python
import sys; sys.path.insert(0, '/workspace/lib')
from pptx_template import ReportTemplate
from platinum import to_dataframe

report = ReportTemplate()
report.set_palette({
    'primary': '#0072CE',
    'series': ['#0072CE', '#6400AA', '#C8102E', '#22c55e', '#f59e0b']
})
report.set_dark_theme('#1a1a2e')

# Load data from tabulated results
df = to_dataframe('/workspace/data/awareness.json')

# Build report
report.title_slide('Brand Health Report', subtitle='Q1 2026', date='March 2026')

report.chart_slide('Aided Awareness', df, chart_type='bar',
    narrative='Sky leads at 78%, 12pp ahead of nearest competitor.',
    source='Brand Tracker Q1 2026, n=2,500')

report.kpi_slide('Key Metrics', [
    {'label': 'Aided Awareness', 'value': '78%', 'change': '+3pp'},
    {'label': 'Consideration', 'value': '45%', 'change': '-1pp'},
])

report.text_slide('Key Findings', [
    'Sky leads aided awareness at 78%, 12pp ahead of BT',
    'Consideration stable at 45% across last three waves',
    'NPS improved +5 points to +32',
])

report.save('/workspace/report.pptx')
```

Then register:
```
register_artifact(file_path="report.pptx", artifact_type="file", label="Brand Health Report")
```

### With a Reference Template (User's Branded PPTX)

When the user uploads a PPTX as a style reference, ALWAYS use it as the template:

```python
import sys; sys.path.insert(0, '/workspace/lib')
from pptx_template import ReportTemplate
from platinum import to_dataframe
import json

# Load user's template -- theme, layouts, backgrounds all preserved
report = ReportTemplate(template_path='/workspace/uploads/client_template.pptx')

# Inspect the extracted design (colors, fonts, available layouts)
print(json.dumps(report.design, indent=2))

# Add slides using the template's existing layouts
df = to_dataframe('/workspace/data/awareness.json')
report.title_slide('Brand Health Report', 'Q1 2026')
report.chart_slide('Aided Awareness', df, chart_type='bar',
    narrative='Sky leads at 78%, 12pp ahead of nearest competitor.')
report.save()
```

### Critical: Template Slides Are Auto-Stripped

`ReportTemplate(template_path=...)` automatically removes ALL existing content
slides. Only the theme (colors, fonts) and slide layouts are preserved. This is
correct -- you are creating a NEW report in the template's style.

**Never open the template directly with `Presentation()` and add on top.**
Always use `ReportTemplate(template_path=...)`.

### Combined Workflow: Style Extraction + Template

When recreating a presentation in the style of an uploaded PPTX:

1. **Visual analysis** -- the uploaded PPTX slides are auto-rendered as images in the conversation. Study them.
2. **Extract design** -- run `python3 /workspace/lib/process_pptx.py /workspace/uploads/client_template.pptx` to get exact hex values, fonts, layout names, and slide images in one step
3. **Create template** -- `ReportTemplate(template_path=...)` extracts the theme and **removes all sample/placeholder slides** automatically
4. **Tabulate data** -- use `render_table()` to get the data the user wants
5. **Build slides** -- use `to_dataframe()` then `chart_slide()`, `kpi_slide()`, etc.
6. **Visual check** -- use `view_image` on the saved PPTX (convert first with `pptx_to_images`) to verify the output matches the reference style

## Available Methods

| Method | Purpose |
|--------|---------|
| `ReportTemplate(template_path?)` | Create from reference PPTX or blank |
| `set_palette(palette)` | Set brand colors: `{'primary': '#hex', 'series': ['#hex', ...]}` |
| `set_dark_theme(bg_hex, text_hex)` | Enable dark background |
| `title_slide(title, subtitle, date)` | Title slide |
| `chart_slide(title, df, chart_type, narrative, source)` | Slide with native editable chart + narrative |
| `kpi_slide(title, kpis)` | KPI cards slide |
| `text_slide(title, bullets)` | Bullet points slide |
| `divider_slide(section_title)` | Section divider |
| `save(path, max_slides_per_file)` | Save, auto-split if >15 slides |
| `save_design_spec(path)` | Export design spec as JSON |

## Chart Types

Supported `chart_type` values: `'bar'`, `'column'`, `'line'`, `'pie'`, `'doughnut'`

Charts are **native editable** -- users can modify data, colors, and styles in PowerPoint.

## Brand Themes

Check the system prompt for `## Brand Themes` or run:
```bash
pt brand-themes --format json
```

Apply brand colors via `set_palette()`.

## Design Spec

When using a reference template, `report.design` contains:
```json
{
  "colors": {"dk1": "#hex", "lt1": "#hex", "accent1": "#hex", ...},
  "fonts": {"heading": "Font Name", "body": "Font Name"},
  "layouts": [{"name": "Title Slide", "placeholders": [...]}]
}
```

Use this to understand the template's design system and match your content to available layouts.

## Color Rules

**Never use `RGBColor()` directly in your scripts.** The library handles all color conversion internally.

All colors are hex strings like `'#0072CE'`. Pass them through the library methods:
- `set_palette({'primary': '#0072CE', 'series': ['#0072CE', '#6400AA']})` for chart/brand colors
- `set_dark_theme('#1a1a2e', '#FFFFFF')` for background and text colors

Common mistakes to AVOID:
```python
# WRONG - never import or use RGBColor directly
from pptx.dml.color import RGBColor
shape.fill.fore_color.rgb = RGBColor(0x00, 0x72, 0xCE)

# WRONG - never construct RGBColor from hex manually
run.font.color.rgb = RGBColor(int('00', 16), int('72', 16), int('CE', 16))

# CORRECT - use hex strings with set_palette
report.set_palette({'primary': '#0072CE', 'series': ['#0072CE', '#6400AA', '#C8102E']})

# CORRECT - use hex strings with set_dark_theme
report.set_dark_theme('#1a1a2e', '#FFFFFF')
```

The `ReportTemplate` class converts hex strings to `RGBColor` internally via `_hex_to_rgb()`. Your scripts should only work with hex string values from the design spec.

## When to Use ReportTemplate vs Raw python-pptx

| Scenario | Approach |
|----------|----------|
| Platinum-branded report (no client template) | `ReportTemplate()` — use this skill |
| Client template with simple layouts | `ReportTemplate(template_path=...)` — preserves theme |
| Client template with specific named layouts and placeholders | Raw `Presentation()` — use `pptx-report-learnings` and client skill (e.g. `client-channel4`) |

When a client template has named placeholders (e.g. `Title 1`, `Text Placeholder 9`, `Footer Placeholder 7`), use raw `python-pptx` for precise control. The `ReportTemplate` wrapper adds its own shapes which may conflict with the template's placeholders.

## Chart Density Limits

Charts become unreadable when overcrowded. Follow these limits:

| Chart Type | Max Categories | Max Series | If Over Limit |
|------------|---------------|------------|---------------|
| Pie | 6 | 1 | Group small values as "Other" |
| Bar (single) | 15 | 1 | Split into two slides |
| Clustered bar | 10 | 4 | Split by demographic across slides |
| Stacked bar | 8 | 5 | Split into two slides (e.g. 6 regions each) |

Categories x series > 40 is unreadable. Always split before generating.

## Break/Transition Slides

For slides that pose a question or transition between topics:
- Use `Section title slide` or `Section title slide gradient A-E` — these have a visible title
- Do NOT use `Large text page` layouts for breaks — their title placeholder is positioned off-screen by design
- Only use `Large text page` when you have actual body text for the `Text Placeholder 5` content area

## Post-Generation QA

**Always spawn a QA sub-agent before delivering.** The `pptx-qa` skill renders every slide as a PNG for visual inspection, which consumes too much context for the parent agent. Spawn a sub-agent:

> Load the `pptx-qa` skill. QA the file at `/workspace/report.pptx`. The generation script is at `/workspace/generate_report.py`. Fix any issues, regenerate, and re-check.

Common issues the QA catches:
- Empty slides (placeholders not filled)
- Labels overlapping on dense charts
- Chart behind the sidebar (left < 1.4")
- Wrong layout for content type

## Tips

1. If user provides a reference PPTX, ALWAYS use it as `template_path` -- this preserves all branding automatically
2. Print `report.design` to see available layouts before adding slides
3. Match content to existing layouts when possible
4. For reports with >15 slides, `save()` auto-splits into multiple files
5. Always tabulate data first, then use `to_dataframe()` to load it
6. Register the saved PPTX as an artifact with `artifact_type="file"`
7. After generation, load `pptx-qa` skill and run checks before delivering
