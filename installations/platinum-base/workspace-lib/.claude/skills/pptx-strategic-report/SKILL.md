---
name: pptx-strategic-report
description: Generate branded PowerPoint reports from specs using generate_pptx.py
triggers:
  - strategic report
  - report from excel
  - report from spec
  - generate slides from spec
keywords: [strategic, report, excel, spec, pptx, powerpoint]
---

# Strategic Report Generation

Your job: build the manifest, gather data, write insights. The `generate_pptx.py` script handles all chart formatting and layout.

```
1. Parse spec → manifest.json
2. Run all tables → /workspace/data/
3. Write AI insights → update manifest
4. Run generate_pptx.py
5. QA with thumbnails, fix manifest, rerun
```

## Phase 1: Parse the Spec

If no spec is provided, propose an outline via `ask_user` and iterate until approved.

If an Excel spec is provided, parse it:

```python
import openpyxl, json, os

os.makedirs('/workspace/slides', exist_ok=True)
wb = openpyxl.load_workbook('/workspace/uploads/spec.xlsx')
ws = wb.active

slides = []
for row in ws.iter_rows(min_row=2, values_only=True):
    section, slide_type, question, table, chart_type, notes, insight = row[:7]
    if not slide_type:
        continue
    slides.append({
        'section': (section or '').strip(),
        'type': (slide_type or '').strip(),
        'title': (question or '').strip(),
        'table': (table or '').strip(),
        'chart_type': (chart_type or '').strip().lower(),
        'notes': (notes or '').strip(),
        'insight_guidance': (insight or '').strip(),
    })

with open('/workspace/slides/manifest.json', 'w') as f:
    json.dump(slides, f, indent=2)
```

## Phase 2: Gather Data

Search for tables and run them all before generating:

```bash
pt tables
pt tables --search "NATREP Q6"
```

Use `render_table(path="Tables/Exec/UK NATREP/NATREP Q6")` with the full TOC path. After running, update the manifest with `data_file` entries and add `footer` fields:

```python
for slide in manifest:
    if slide.get('table'):
        slide['data_file'] = f"{slide['table'].replace(' ', '_').lower()}.json"
        slide['footer'] = f"{slide['table']} | n=1,000"
```

Verify every `data_file` exists in `/workspace/data/`.

## Phase 3: Write Insights

The spec's `insight_guidance` is DIRECTION ("Talk about top 3"), not the actual insight. **You must write real insights with real numbers from the data.**

Work in batches of 5. Print the data, read it, write the insight yourself:

```python
from platinum import to_dataframe
df = to_dataframe(f"/workspace/data/{slide['data_file']}")
print(df.to_string())
# Now WRITE the insight based on what you see
manifest[i]['insight'] = "29% of UK adults view regionality as important, strongest in the North-West (41%) and weakest in East Midlands (18%)."
```

For 10+ chart slides, use the API script approach:

```python
import anthropic, json, os, re, sys
sys.path.insert(0, '/workspace/lib')
from platinum import to_dataframe

client = anthropic.Anthropic(
    base_url=os.environ.get('ANTHROPIC_BASE_URL', 'http://localhost:3080')
)

with open('/workspace/slides/manifest.json') as f:
    manifest = json.load(f)

chart_slides = [(i, s) for i, s in enumerate(manifest) if s.get('data_file')]

for batch_start in range(0, len(chart_slides), 5):
    batch = chart_slides[batch_start:batch_start + 5]
    context = []
    for idx, slide in batch:
        df = to_dataframe(f"/workspace/data/{slide['data_file']}")
        context.append(f"Slide {idx}: {slide['title']}\nGuidance: {slide.get('insight_guidance', '')}\nData:\n{df.head(10).to_string()}\n")

    response = client.messages.create(
        model="claude-sonnet-4-5", max_tokens=2000,
        messages=[{"role": "user", "content": f"Write 1-2 sentence data insights for these slides. Use actual percentages. British English, active voice. Return as JSON: [{{\"slide_index\": N, \"insight\": \"...\"}}]\n\n{chr(10).join(context)}"}]
    )

    match = re.search(r'\[.*\]', response.content[0].text, re.DOTALL)
    if match:
        for item in json.loads(match.group()):
            manifest[item['slide_index']]['insight'] = item['insight']

with open('/workspace/slides/manifest.json', 'w') as f:
    json.dump(manifest, f, indent=2)
```

### Insight rules
- Real numbers ("29%"), not vague language
- Lead with the finding, not the question
- 1-2 sentences max
- Never copy spec guidance verbatim

## Phase 4: Generate PPTX

**Use `generate_pptx.py`. Do NOT write a custom python-pptx script.**

```bash
cd /workspace/lib/pptx-tools && python generate_pptx.py \
    --template /workspace/uploads/template.pptx \
    --manifest /workspace/slides/manifest.json \
    --data-dir /workspace/data/ \
    --output /workspace/report.pptx \
    --validate --thumbnails -v
```

Check the loaded **client skill** for `--colors` and `--single-color` flags specific to the brand.

The script handles: reverse_order, legend hiding, number format, adaptive layout, insight positioning, empty data, gradient cycling, auto-fit text. You do not need to implement any of this.

### Useful flags

| Flag | Purpose |
|------|---------|
| `--validate` | Check for formatting issues after generation |
| `--thumbnails` | Generate QA grid images |
| `--dry-run` | Show plan without generating |
| `--list-layouts` | Discover template layout names |
| `-v` | Show data details per slide |

## Phase 5: QA

1. View thumbnail grids with `view_image`
2. Run `generate_pptx.py --validate-only /workspace/report.pptx`
3. Fix issues in the manifest (not the PPTX)
4. Rerun generate_pptx.py

## Text Slide Rules

These cause the most visible bugs. Follow exactly.

**Title vs body:** The title placeholder is for a SHORT heading (max ~10 words). The body placeholder (idx=17) is for the content. Never put the same text in both.

**Spec instructions are not titles.** If the spec says "Bring together some key findings from this section as text and figures" — that is a direction to YOU. Write a proper title like "Key Findings: Regional Identity" and put the findings in the body.

**Research objective slides:** Each gets a short numbered title (e.g. "1. What makes a core regional show work?") and the body should contain your analysis answering that question using data from earlier slides. Do not leave the body empty. Do not repeat the title in the body.

**Summary slides:** Write an actual summary of the preceding section's data. Bullet points with real numbers. The spec's instruction text tells you what to summarise — it is not the content itself.

**Body text length:** Keep body text to 4-6 bullet points max. If you have more, split across slides or prioritise the most important findings.

## Manifest Format

```json
[
  {"type": "title", "title": "Report Title"},
  {"type": "text", "title": "Short Heading", "body": "Content goes here..."},
  {"type": "section", "title": "Section Name"},
  {"type": "chart", "title": "Chart Question?", "chart_type": "bar",
   "data_file": "table.json", "insight": "AI insight text", "footer": "Source | n=1,000"},
  {"type": "thankyou", "title": "Thank You"}
]
```

| Type | Chart types | Series default | Auto-transpose? |
|------|-------------|---------------|-----------------|
| `bar` | Horizontal bar | Total only | No |
| `bar` (clustered) | Side-by-side horizontal | Explicit columns | No |
| `stacked bar` | 100% stacked horizontal | All except Total | **Yes** |
| `pie` | Pie | Total only | No |
| `column` | Vertical bar | Total only | No |
| `stacked column` | 100% stacked vertical | All except Total | **Yes** |

Override series with `"series_columns": ["Male", "Female"]` or `"all_except_total"`.

**Stacked charts are auto-transposed** (equivalent to PowerPoint "Switch Row/Column"). Demographics end up on Y-axis, answer options stack as series. See pptx-report-learnings for details. Add `"transpose": true` to force transpose on non-stacked charts if needed.

## Rules

1. **Use generate_pptx.py** — never write custom python-pptx scripts
2. **Template is the design system** — never override backgrounds, fonts, colours
3. **Insights must be AI-generated** — never copy spec guidance
4. **Title is a short heading** — never put body content or spec instructions in the title
5. **Fix issues in the manifest** — never edit the PPTX directly
6. **Validate after every generation** — always use `--validate`
