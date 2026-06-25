---
name: strategic-reports
description: Generate observational and strategic reports as interactive webapps and downloadable PowerPoint with native editable charts
triggers:
  - strategic report
  - executive summary
  - observational report
  - write up findings
  - narrative report
  - download powerpoint report
keywords: [report, strategic, observational, executive, powerpoint, pptx, webapp, narrative, insights, download]
---

# Strategic & Observational Reports

## When to Use
- User asks for a report, executive summary, strategic analysis, or observational write-up
- User wants a downloadable PowerPoint with editable charts
- User wants a polished webapp presenting findings with narrative and charts

> **For chart/viz specifics**, see the **data-visualization** or **python-visualization** skills. This skill covers *report structure, narrative flow, and output formats*.

## Report Types

| Type | Headline Style | Example |
|------|---------------|---------|
| **Observational** | States what data shows, no interpretation | "TV dominates: 95% watch men's football on TV" |
| **Strategic** | Interprets data with actionable recommendations | "Shift digital budget 15% to streaming — consideration gap is closing" |

## Workflow

### 1. Discovery — understand the brief

Use `ask_user` to clarify: topic, audience (C-suite vs insights team), output format (webapp, PPTX, both), any branding/template requirements, and whether they have context documents (research briefs, strategy docs) to inform the narrative.

### 2. Data gathering

Check existing tables first (see **table-access** skill), then tabulate as needed. Use `render_table` to show findings as you go. For many tables, note that sequential fetching is slow (~3-4 mins for 40+ tables) — batch where possible.

### 3. Analysis — find the story

For each table: identify the top-line finding, note significant segment differences, flag data quality issues (high NES, low bases), connect findings into a narrative arc.

### 4. Build the output

#### Interactive Webapp (preferred for strategic reports)

Build using the no-build webapp template (see **data-visualization** skill). Structure as:
- Hero section with title, subtitle, date
- KPI cards for headline numbers
- Chart sections with narrative text alongside
- Source references for each chart
- Footer with methodology notes

Apply client branding: ask for primary/secondary colours, apply as CSS variables. Dark backgrounds require light text and no chart gridlines.

> **Recommended: Use the PPTX Template Library**
> For PowerPoint reports, use the `pptx-template` skill and the `pptx_template.py` library at `/workspace/lib/pptx_template.py`. It handles slide layout, native editable charts, dark themes, and template reuse automatically. See the `pptx-template` skill for the complete workflow.

#### PowerPoint (for client handoff — editable charts required)

**Charts MUST be native editable PowerPoint chart objects**, not embedded images. Users need to right-click → "Edit Data" in PowerPoint.

```bash
cd /workspace && pip install python-pptx
```

```python
from pptx import Presentation
from pptx.util import Inches, Pt, Emu
from pptx.chart.data import CategoryChartData
from pptx.enum.chart import XL_CHART_TYPE
from pptx.dml.color import RGBColor
import sys; sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe

prs = Presentation()
prs.slide_width = Inches(13.333)  # Widescreen 16:9
prs.slide_height = Inches(7.5)

# --- Native editable chart ---
df = to_dataframe('/workspace/data/awareness.json')
chart_data = CategoryChartData()
chart_data.categories = list(df.index)  # Row labels as categories
for col in df.columns:
    chart_data.add_series(col, list(df[col]))  # Each column is a series

slide = prs.slides.add_slide(prs.slide_layouts[6])  # Blank
chart_frame = slide.shapes.add_chart(
    XL_CHART_TYPE.BAR_CLUSTERED,
    Inches(0.5), Inches(1.2), Inches(8), Inches(5.5),
    chart_data
)
chart = chart_frame.chart
chart.has_legend = True

# Add narrative text beside chart
txBox = slide.shapes.add_textbox(Inches(8.8), Inches(1.2), Inches(4), Inches(5.5))
tf = txBox.text_frame
tf.word_wrap = True
tf.paragraphs[0].text = "Key finding: Brand X leads at 45%, 12pp ahead."
tf.paragraphs[0].font.size = Pt(14)

prs.save('/workspace/report.pptx')
```

### Chart type selection

| Data shape | Chart type | `XL_CHART_TYPE` |
|-----------|-----------|-----------------|
| Comparing segments side-by-side | Grouped horizontal bar | `BAR_CLUSTERED` |
| Single series category comparison | Horizontal bar | `BAR_CLUSTERED` (1 series) |
| Frequency distribution / ordinal | Vertical column | `COLUMN_CLUSTERED` |
| Trend over time | Line | `LINE` |
| Part of whole | Pie/doughnut | `PIE` or `DOUGHNUT` |

### File size limit

Reports over ~15 slides may hit HTTP 413 (payload too large). Auto-split into parts:

```python
MAX_SLIDES = 15
if len(slides_data) > MAX_SLIDES:
    for i in range(0, len(slides_data), MAX_SLIDES):
        part = slides_data[i:i+MAX_SLIDES]
        build_pptx(part, f'/workspace/report_part{i//MAX_SLIDES + 1}.pptx')
```

Register each part as a separate artifact.

## Narrative Writing Guidelines

- **Lead with the insight**: "Brand X leads consideration at 45%" not "The table shows code 1 = 45%"
- **Observational headlines**: state what data shows, no interpretation
- **Strategic headlines**: interpret what data means with actionable recommendations
- **Quantify differences**: "12 percentage points ahead" not "significantly higher"
- **Flag base sizes**: note when bases are <100
- **No variable names, code numbers, or syntax** in client-facing output
- **Use active voice**: "Customers prefer X" not "X is preferred by customers"

## PowerPoint Contrast Rules

**Dark slide backgrounds require explicit light-coloured text and labels everywhere:**

```python
from pptx.dml.color import RGBColor

WHITE = RGBColor(0xFF, 0xFF, 0xFF)
LIGHT_GREY = RGBColor(0xCC, 0xCC, 0xCC)

# Set ALL text to white on dark backgrounds
for paragraph in text_frame.paragraphs:
    for run in paragraph.runs:
        run.font.color.rgb = WHITE

# Chart axis labels and tick marks
chart.category_axis.tick_labels.font.color.rgb = WHITE
chart.value_axis.tick_labels.font.color.rgb = WHITE
chart.category_axis.tick_labels.font.size = Pt(10)
chart.value_axis.tick_labels.font.size = Pt(10)

# Chart legend
chart.legend.font.color.rgb = WHITE

# Remove gridlines on dark backgrounds
chart.value_axis.major_gridlines.format.line.fill.background()

# Table cells — set font colour explicitly
for row in table.rows:
    for cell in row.cells:
        for paragraph in cell.text_frame.paragraphs:
            for run in paragraph.runs:
                run.font.color.rgb = WHITE
```

**Always check:** if the slide background is dark (navy, black, dark grey), ALL text, labels, axis ticks, legends, and table cell text must be white or light-coloured. Black text on dark backgrounds is unreadable.

## Common Mistakes

| Wrong | Right | Why |
|-------|-------|-----|
| Embedding charts as PNG images | Use `CategoryChartData` + `add_chart()` | Users must be able to edit chart data in PowerPoint |
| Black text on dark slide backgrounds | Set all text/labels to white explicitly | Unreadable otherwise |
| Generic titles ("Chart 1") | Insight-led titles ("Sky leads at 45%") | Titles tell the story |
| Including NES/Total rows in charts | Filter these out | They confuse the narrative |
| 20+ data points on one chart | Split or use top-N | Overcrowded charts lose the message |
| Single huge PPTX file | Split at ~15 slides per file | Avoids HTTP 413 payload limit |
| Chart gridlines on dark backgrounds | Remove gridlines | They clash with dark themes |

## Rules

1. Always run data and verify findings before writing narrative — never fabricate numbers
2. PowerPoint charts MUST be native editable objects (`add_chart` + `CategoryChartData`), NEVER embedded PNG images
3. **Dark backgrounds: ALL text, labels, axis ticks, legends, and table cells must be white/light** — black text on dark slides is unreadable
4. Every chart must have a source reference (which table produced it)
5. Always offer both webapp and PPTX unless user specifies one
6. Keep executive summaries to 3-5 bullet points maximum
7. Note base sizes — flag any base under 100
8. Never include variable names, code numbers, or syntax in client-facing output
9. Ask user to review findings before generating final output
10. For branded reports, ask for colours upfront — dark backgrounds need light text and no gridlines
11. Split reports over 15 slides into multiple files
