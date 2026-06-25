# Agent Skills Index

GENERATED FILE — do not edit by hand. Regenerate with:

    python3 scripts/build-skills-index.py

25 skills. Loaded on demand by the sandbox agent via the Claude Agent SDK (`settingSources: ['project']` discovers this directory at `/workspace/.claude/skills/`).

| Skill | Description | Lines | Triggers |
|---|---|---|---|
| `carbon-syntax` | Cross-tabulation axis syntax reference for Carbon table specifications | 122 | yes |
| `casedata` | Raw per-respondent case data access and analysis with pt casedata | 142 | yes |
| `composite-tables` | Combine Carbon cross-tab outputs with external data rows/columns for unified charts and reports | 155 | yes |
| `conversation-history` | Team knowledge base providing access to previous agent conversations | 51 | yes |
| `correspondence-analysis` | Correspondence analysis and perceptual mapping of brands/attributes from cross-tabulation data | 172 | yes |
| `create-variable` | Create new variables from case-level data analysis and write them back to the dataset | 459 | yes |
| `cross-job-analysis` | Analyse and compare data across multiple jobs within a customer, with intelligent job discovery | 117 | yes |
| `data-processing` | Python/pandas data processing with saved PlatinumData JSON files | 184 | yes |
| `data-provenance` | Data provenance workflows ensuring visualisations reference authoritative table sources and track data lineage | 110 | yes |
| `driver-analysis` | Key driver analysis (KDA) identifying which variables most influence a target outcome, with regression, importance ranking, and stakeholder-tailored outputs | 306 | yes |
| `hoist-skill` | Package what we just built into a reusable skill the user can keep and reuse | 57 | yes |
| `platinum-tables` | Default patterns and rules for running cross-tabulations in Carbon/Platinum | 181 | yes |
| `pptx-qa` | Quality assurance for generated PowerPoint reports — batched visual inspection by parallel sub-agents | 166 | yes |
| `pptx-report-learnings` | Data handling rules for PPTX reports — chart type selection, clustered/stacked patterns, data loading, and gotchas that generate_pptx.py cannot catch | 138 | yes |
| `pptx-strategic-report` | Generate branded PowerPoint reports from specs using generate_pptx.py | 214 | yes |
| `pptx-template` | Generate PowerPoint reports with native editable charts using the pptx_template library | 224 | yes |
| `python-visualization` | Static image charts and plots using matplotlib, seaborn, and plotly | 244 | yes |
| `research` | Data discovery guidance using pt search, pt vars, and pt tables | 77 | yes |
| `segmentation` | Respondent segmentation and clustering analysis using factor analysis, K-means, and LCA | 181 | yes |
| `strategic-reports` | Generate observational and strategic reports as interactive webapps and downloadable PowerPoint with native editable charts | 202 | yes |
| `style-extraction` | Extract visual style from reference files (PPTX, images, PDFs) for consistent branding | 137 | yes |
| `table-access` | Access existing table specs, run saved tables, multi-source table discovery, and apply customer design templates | 115 | yes |
| `table-management` | Batch create, copy, and regenerate CBT tables in the TOC using pt toc commands | 325 | yes |
| `table-provenance` | Spec discipline for any generated table — five required components (top, side, filter, weight, caseFilter) and a written spec summary the user must verify before downstream use | 134 | yes |
| `turf-analysis` | TURF (Total Unduplicated Reach and Frequency) analysis for optimising product/brand portfolios | 199 | yes |
