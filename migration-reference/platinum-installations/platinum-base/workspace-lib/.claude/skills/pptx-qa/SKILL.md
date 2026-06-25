---
name: pptx-qa
description: Quality assurance for generated PowerPoint reports — batched visual inspection by parallel sub-agents
triggers:
  - qa powerpoint
  - check pptx
  - review slides
  - quality check report
keywords: [qa, quality, review, pptx, powerpoint, check, fix, slides]
---

# PowerPoint QA

Quality assurance for generated PPTX reports using **parallel sub-agents** that each check a batch of slides.

## When to Use
- After generating any PPTX report
- When asked to "check", "review", or "QA" a PowerPoint file

## How to Invoke — Batched Parallel QA

The parent agent should:

1. **Run the programmatic checks** (fast, inline — no sub-agent needed)
2. **Render slides to images** — one PNG per slide
3. **Spawn parallel sub-agents** — each reviews 5 slides and fixes issues in the generation script
4. **Regenerate** after all sub-agents complete
5. **Final check** — run programmatic checks again

### Step 1: Programmatic Checks (Inline)

Run this BEFORE spawning sub-agents to catch obvious issues:

```python
from pptx import Presentation
from pptx.util import Inches

prs = Presentation('/workspace/report.pptx')
issues = []

for i, slide in enumerate(prs.slides):
    layout = slide.slide_layout.name if slide.slide_layout else ''
    sn = i + 1
    title_shape = slide.shapes.title
    title_name = title_shape.name if title_shape else None

    # Empty title
    if title_shape and not title_shape.text_frame.text.strip():
        issues.append(f'Slide {sn}: Empty title')

    # Title off-screen
    for shape in slide.shapes:
        if 'Title' in shape.name and shape.left is not None and shape.left < 0:
            issues.append(f'Slide {sn}: Title off-screen')

    # Empty body on text slides
    if 'column' in layout.lower():
        has_body = any(s.has_text_frame and s.text_frame.text.strip() and s.name != title_name for s in slide.shapes)
        if not has_body:
            issues.append(f'Slide {sn}: Empty body')

    # Graph slide without chart
    if 'graph' in layout.lower() and not any(s.has_chart for s in slide.shapes):
        issues.append(f'Slide {sn}: No chart on graph slide')

    # Chart issues
    for shape in slide.shapes:
        if shape.has_chart:
            try:
                plot = shape.chart.plots[0]
                cats = len(plot.categories)
                series = len(shape.chart.series)
                ct = str(shape.chart.chart_type)

                # Dense charts
                if 'STACKED' in ct and cats * series > 40:
                    issues.append(f'Slide {sn}: Dense stacked {cats}x{series}')
                if 'CLUSTERED' in ct and series > 4:
                    issues.append(f'Slide {sn}: Clustered {series} series')

                # Chart too high
                if shape.top and shape.top < 2377440:  # < 2.6"
                    issues.append(f'Slide {sn}: Chart too high (top={shape.top/914400:.1f}")')
            except:
                pass

    # Wrong layout for breaks
    if 'large text' in layout.lower():
        issues.append(f'Slide {sn}: Large text slide used')

print(f'Issues: {len(issues)}')
for iss in issues:
    print(f'  {iss}')
```

### Step 2: Render Slides to Images

```bash
python3 /workspace/lib/render_slides.py /workspace/report.pptx
# Returns JSON array of PNG paths: ["/workspace/slides/report/slide_001.png", ...]
```

### Step 3: Spawn Parallel Sub-Agents (Batches of 5)

For a 70-slide report, spawn ~14 sub-agents. Each reviews 5 slides visually and reports issues.

**Sub-agent prompt template:**

> You are reviewing slides {start} to {end} of a PowerPoint report. The slide images are at:
> - /workspace/slides/report/slide_{start:03d}.png
> - /workspace/slides/report/slide_{start+1:03d}.png
> - ... through slide_{end:03d}.png
>
> View each image. For each slide, check:
> 1. Is the title visible and not truncated?
> 2. Is all body text readable (not cut off, not overlapping)?
> 3. If there's a chart: are labels readable? Is the chart the right type? Are percentages showing correctly?
> 4. Is the sidebar/branding consistent?
> 5. Are there any empty areas that should have content?
>
> Report issues as a JSON array: [{"slide": N, "issue": "description", "fix": "suggested fix"}]
> If no issues, return: []

**How to batch:**
```python
import json
total_slides = len(prs.slides)
batch_size = 5
batches = []
for start in range(1, total_slides + 1, batch_size):
    end = min(start + batch_size - 1, total_slides)
    batches.append((start, end))
# Spawn one sub-agent per batch
```

### Step 4: Collect and Fix

After all sub-agents return:
1. Merge all issue lists
2. Group by issue type (positioning, content, chart formatting)
3. Fix the generation script for each issue category
4. Regenerate the PPTX

### Step 5: Final Check

Run the programmatic checks again on the regenerated PPTX. If clean, register as artifact.

## Issue Fix Patterns

| Issue | Fix in Generation Script |
|-------|------------------------|
| Text truncated | Shorten text or use smaller font size |
| Chart labels overlapping | Split into 2 slides or remove labels |
| Chart too high | `CHART_TOP = Inches(2.6)` |
| Empty body on text slide | Write content into `slide.placeholders[17]` |
| Wrong number format | Use `'0"%"'` with `to_dataframe()` values |
| Sidebar missing | Wrong template/layout used |
| Title empty | Write into `slide.shapes.title.text` |

## Rules

1. **Programmatic checks first** — catches 80% of issues without images
2. **Batch sub-agents at 5 slides each** — manageable context per agent
3. **Fix in the script, not the PPTX** — regenerate after fixes
4. **Max 2 fix cycles** — if issues persist, flag to user
5. **Report format**: each sub-agent returns JSON array of issues
