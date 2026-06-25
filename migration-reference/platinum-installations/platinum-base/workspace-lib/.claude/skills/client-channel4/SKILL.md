---
name: client-channel4
description: Channel 4 brand, template, and generate_pptx.py flags for branded report generation
triggers:
  - channel 4
  - c4
  - bprchannel4
keywords: [channel4, c4, brand, template, writing style, regional, nations]
---

# Channel 4 Client Context

## Who They Are

Channel 4 is a publicly-owned, commercially-funded UK public service broadcaster. Identity: "Altogether Different". Committed to unheard voices, diversity, and regional representation. Commissions content but does not produce it directly.

Channels: Channel 4, E4, More4, Film4, 4seven, Channel 4 Streaming.

## Brand Colours

| Colour | Hex | Use |
|--------|-----|-----|
| **C4 Green** | `#8CF57E` | Primary brand |
| Cyan | `#99FFED` | Chart accent 1 |
| Blue | `#89CDFF` | Chart accent 2 |
| Purple | `#CA88FF` | Chart accent 3 |
| Pink | `#FF99DD` | Chart accent 4 |
| Orange | `#FFB288` | Chart accent 5 |
| Yellow | `#FFE588` | Chart accent 6 |

Single-series bar charts: use `#AAFF89` (C4 Green variant).

## Fonts

- **4 Headline** — titles, large text
- **4 Text** — body copy, captions

## generate_pptx.py Command

```bash
python /workspace/lib/pptx-tools/generate_pptx.py \
    --template /workspace/uploads/template.pptx \
    --manifest /workspace/slides/manifest.json \
    --data-dir /workspace/data/ \
    --output /workspace/report.pptx \
    --colors '#99FFED,#89CDFF,#CA88FF,#FF99DD,#FFB288,#FFE588' \
    --single-color '#AAFF89' \
    --sidebar-width 1.5 \
    --validate --thumbnails -v
```

## Template

Download from Files: `pt files download "Docs/Templates/Channel 4 PowerPoint template (empty).pptx"`

### Slide Dimensions
- 13.33" x 7.50" (16:9)
- Green sidebar ~1.3" on left — all content starts at 1.4"+

### Layouts

| Layout | Use |
|--------|-----|
| `Title slide` | Report title |
| `One column slide` | Text: About, Methodology, Summary, Research Objectives |
| `Graph slide` | All chart slides |
| `Section title slide gradient A-E` | Section dividers (cycle colours) |
| `Appendix slide` | Technical details |
| `Thank you slide` | Final slide |

### Key Placeholders

| Layout | Body idx | Footer idx |
|--------|----------|------------|
| One column slide | 17 | 18 |
| Graph slide | -- | 22 |
| Section gradient A-E | -- | 18 |

## Writing Style

- **British English**: organise, colour, programme (not program), per cent (two words in text, % in charts)
- **Channel 4** (always a space, always numerical 4). Film4, More4, E4 (no space). 4seven (lowercase s)
- **Active voice**, clear and concise
- **Dates**: Tuesday 6 October 2025 (no commas, no th/st/nd/rd)
- **Numbers**: spell out one to nine, figures for 10+
- **Inclusive language**: "people with disabilities" not "the disabled"
- **No ALL CAPS** for emphasis
- **Insight-led headlines**: "Sky leads at 45%" not "Chart 1"
- **Quantify precisely**: "12 percentage points ahead" not "significantly higher"
- **No variable names or codes** in client-facing output

## Rules

1. Always use the official C4 template
2. Respect the green sidebar — content at 1.4"+ from left
3. Cycle section divider colours (gradient A through E)
4. Template is the design system — never override backgrounds, fonts, colours
