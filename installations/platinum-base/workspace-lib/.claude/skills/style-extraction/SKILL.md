---
name: style-extraction
description: Extract visual style from reference files (PPTX, images, PDFs) for consistent branding
triggers:
  - match style
  - extract style
  - brand colors
  - reference design
  - style guide
---

# Style Extraction

Extract visual style from reference files to ensure consistent branding across web apps and PowerPoint reports.

## Two-Phase Workflow

### Phase 1 -- Programmatic Extraction (Exact Values)

Run the one-command PPTX processor:
```bash
python3 /workspace/lib/process_pptx.py /workspace/uploads/reference.pptx
```

This outputs JSON with everything you need:
```json
{
  "design": { "colors": {"dk1": "#hex", ...}, "fonts": {...}, "layouts": [...], "slide_size": {...} },
  "style": { "palette": {...}, "fonts": {...}, "layout": "dark"|"light" },
  "slides": ["/workspace/slides/slide_001.png", ...],
  "slide_error": null
}
```

- `design` -- exact theme colors, fonts, layouts from the PPTX XML (always works)
- `style` -- derived palette with primary/accent/background/text/series (always works)
- `slides` -- rendered slide images via LibreOffice (may fail in some environments)
- `slide_error` -- null on success, descriptive error string on failure

Save the style spec for reuse:
```python
import json
# After running process_pptx.py and capturing stdout:
with open('/workspace/style.json', 'w') as f:
    json.dump(result['style'], f, indent=2)
```

For images (no PPTX available):
```python
import sys; sys.path.insert(0, '/workspace/lib')
from style_extract import extract_colors_from_image
colors = extract_colors_from_image('/workspace/uploads/reference.png')
print(colors)  # ['#1a1a2e', '#0072CE', '#f1f5f9', ...]
```

### Phase 2 -- AI Vision Analysis (Design Intent)

**Note:** When a user uploads a PPTX via chat, the slides are automatically rendered as images in the conversation. You can see them directly without running `pptx_to_images()`. Skip to the visual analysis below.

If you need to render slides manually (e.g. from a PPTX already on disk):

```python
from style_extract import pptx_to_images
slide_images = pptx_to_images('/workspace/uploads/reference.pptx')
# Images saved to /workspace/slides/reference/slide_001.png, slide_002.png, ...
```

Then use `view_image` to inspect each slide visually:

```
mcp__ui__view_image(file_path="/workspace/slides/reference/slide_001.png")
mcp__ui__view_image(file_path="/workspace/slides/reference/slide_002.png")
```

Analyze the images to understand:
- Overall design philosophy (corporate/playful/bold)
- Spacing patterns and layout rhythm
- How colors are used (headings vs. accents vs. data)
- Typography hierarchy
- Distinctive decorative elements

Combine vision notes into the style spec:
```python
style = build_style_spec(design, vision_notes='Corporate dark theme with blue accent hierarchy. Charts use accent1 for primary brand, muted tones for competitors.')
```

## Using the Style Spec

### In Web Apps (dashboard-template)
```javascript
import { applyTheme } from './lib/components/index.js'
// Load style.json (save it to /workspace/data/ and load via the data-loader)
applyTheme({ primary: style.palette.primary, accent: style.palette.accent, bg: style.palette.background })
```

### In PowerPoint (pptx-template)
```python
report = ReportTemplate(template_path='/workspace/uploads/reference.pptx')
# Design is automatically inherited from the template
# Use style spec for custom shapes or additional color decisions
```

## Available Functions

| Function | Input | Output |
|----------|-------|--------|
| `extract_from_pptx(path)` | PPTX file | `{colors, fonts, layouts, slide_size, shape_colors}` |
| `extract_colors_from_image(path, n=8)` | Image file | `['#hex', ...]` sorted by frequency |
| `extract_from_pdf(path, page=0)` | PDF file | PIL Image for analysis |
| `pptx_to_images(path, output_dir)` | PPTX file | `['slide_1.png', ...]` paths |
| `build_style_spec(design, vision_notes)` | Design dict | Unified style spec |

## Style Spec Format

```json
{
  "palette": {
    "primary": "#0072CE",
    "accent": "#FF6B00",
    "background": "#0f172a",
    "text": "#f1f5f9",
    "series": ["#0072CE", "#6400AA", "#C8102E", "#22c55e", "#f59e0b", "#ec4899"]
  },
  "fonts": { "heading": "Calibri", "body": "Calibri" },
  "layout": "dark",
  "vision_notes": "...",
  "raw_theme_colors": { "dk1": "#0f172a", "lt1": "#f1f5f9", ... }
}
```

## Best Practices

1. Always extract programmatic values first -- they are exact
2. Use vision analysis for qualitative aspects (spacing, decorative intent, color usage philosophy)
3. PPTX is the richest source -- contains complete design system in theme XML
4. Images give dominant colors but not typography or layout structure
5. Save style.json for reuse across multiple artifacts in the same session
