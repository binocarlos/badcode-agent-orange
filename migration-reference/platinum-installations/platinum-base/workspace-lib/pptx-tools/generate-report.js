#!/usr/bin/env node
/**
 * generate-report.js — Assemble a PPTX report from themed HTML slide templates.
 *
 * Reads ordered slide JSON files, populates HTML templates with content and
 * theme CSS, converts them to PowerPoint via html2pptx, and adds native
 * PptxGenJS charts where placeholders are defined.
 *
 * Usage:
 *   node generate-report.js --theme themes/channel4.css --slides ./slides --output report.pptx
 *
 * Slide JSON format:
 *   {
 *     "template": "chart",
 *     "title": "Slide title",
 *     "body": "Optional body text",
 *     "insight": "Key stat callout",
 *     "footer": "Source line",
 *     "chart": {
 *       "type": "bar",
 *       "data_file": "/workspace/data/my_table.json",
 *       "series_columns": ["Total"],
 *       "exclude_rows": ["Average", "Mean", "Total"]
 *     }
 *   }
 */

const fs = require('fs');
const path = require('path');
const os = require('os');
const pptxgen = require('pptxgenjs');

// html2pptx is globally installed in the sandbox environment
const html2pptx = require('./html2pptx');

// ---------------------------------------------------------------------------
// CLI argument parsing
// ---------------------------------------------------------------------------

function parseArgs(argv) {
  const args = { theme: null, slides: null, output: null };
  for (let i = 2; i < argv.length; i++) {
    if (argv[i] === '--theme' && argv[i + 1]) {
      args.theme = argv[++i];
    } else if (argv[i] === '--slides' && argv[i + 1]) {
      args.slides = argv[++i];
    } else if (argv[i] === '--output' && argv[i + 1]) {
      args.output = argv[++i];
    }
  }
  if (!args.theme || !args.slides || !args.output) {
    console.error('Usage: node generate-report.js --theme <theme.css> --slides <dir> --output <file.pptx>');
    process.exit(1);
  }
  return args;
}

// ---------------------------------------------------------------------------
// Theme CSS helpers
// ---------------------------------------------------------------------------

/**
 * Parse accent colours (--accent1 through --accent6) from a CSS file.
 * Returns an array of hex strings WITHOUT the '#' prefix, suitable for PptxGenJS.
 */
function parseAccentColours(cssText) {
  const colours = [];
  for (let i = 1; i <= 6; i++) {
    const re = new RegExp(`--accent${i}\\s*:\\s*([^;]+);`);
    const m = cssText.match(re);
    if (m) {
      colours.push(m[1].trim().replace(/^#/, ''));
    }
  }
  return colours;
}

/**
 * Merge theme CSS into an HTML template's <style> block.
 *
 * The theme CSS contains :root variables that override the defaults baked into
 * each template. We inject the theme CSS right after the opening <style> tag so
 * theme values win via specificity/order.
 */
function injectThemeCSS(html, themeCSS) {
  // If the HTML has an existing <style> tag, inject theme CSS at the top of it
  const styleTagIdx = html.indexOf('<style>');
  if (styleTagIdx !== -1) {
    const insertPos = styleTagIdx + '<style>'.length;
    return html.slice(0, insertPos) + '\n/* --- theme --- */\n' + themeCSS + '\n/* --- end theme --- */\n' + html.slice(insertPos);
  }
  // No style tag — wrap it in a <style> inside <head>
  const headClose = html.indexOf('</head>');
  if (headClose !== -1) {
    return html.slice(0, headClose) + '<style>\n' + themeCSS + '\n</style>\n' + html.slice(headClose);
  }
  // Fallback — prepend
  return '<style>\n' + themeCSS + '\n</style>\n' + html;
}

// ---------------------------------------------------------------------------
// Template placeholder replacement
// ---------------------------------------------------------------------------

const PLACEHOLDER_KEYS = ['title', 'body', 'insight', 'footer'];

/**
 * Replace {{key}} placeholders in HTML with actual slide content.
 * Missing keys are replaced with empty strings so the template renders cleanly.
 */
function populateTemplate(html, slideData) {
  let result = html;
  for (const key of PLACEHOLDER_KEYS) {
    const value = slideData[key] != null ? String(slideData[key]) : '';
    // Escape HTML entities in the value
    const escaped = value
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
    result = result.replace(new RegExp(`\\{\\{${key}\\}\\}`, 'g'), escaped);
  }
  return result;
}

// ---------------------------------------------------------------------------
// PlatinumData chart helpers
// ---------------------------------------------------------------------------

const SKIP_TYPES = new Set(['Base', 'Spacer', 'Empty']);

/**
 * Parse a PlatinumData JSON file into chart-ready data.
 *
 * Returns { categories: string[], seriesData: { name, values }[] } where
 * values are 0-100 percentages (colpc * 100).
 *
 * @param {string} filePath - Path to the PlatinumData JSON file
 * @param {string[]} seriesColumns - Column labels to include (e.g. ["Total"])
 * @param {string[]} excludeRows - Row labels to exclude (e.g. ["Average", "Total"])
 * @returns {{ categories: string[], seriesData: Array<{ name: string, values: number[] }> }}
 */
function parsePlatinumData(filePath, seriesColumns, excludeRows) {
  const raw = JSON.parse(fs.readFileSync(filePath, 'utf8'));

  const topVecs = raw.top?.vecs || [];
  const sideVecs = raw.side?.vecs || [];
  const cellRows = raw.cells?.rows || [];

  // Build column index map: label -> array index (skip Base/Spacer/Empty)
  const colMap = {};
  topVecs.forEach((v, i) => {
    if (!SKIP_TYPES.has(v.type)) {
      colMap[v.label] = i;
    }
  });

  // Determine which columns to include
  let selectedCols;
  if (seriesColumns && seriesColumns.length > 0) {
    selectedCols = seriesColumns
      .filter(name => colMap[name] !== undefined)
      .map(name => ({ name, idx: colMap[name] }));
  } else {
    // Use all non-skipped columns
    selectedCols = topVecs
      .map((v, i) => ({ name: v.label, idx: i }))
      .filter(c => !SKIP_TYPES.has(topVecs[c.idx].type));
  }

  if (selectedCols.length === 0) {
    // Fallback: use all columns
    selectedCols = topVecs
      .map((v, i) => ({ name: v.label, idx: i }))
      .filter(c => !SKIP_TYPES.has(topVecs[c.idx].type));
  }

  // Build exclude set (case-insensitive)
  const excludeSet = new Set((excludeRows || []).map(r => r.toLowerCase()));

  // Collect categories and values
  const categories = [];
  const seriesValues = selectedCols.map(() => []);

  for (let i = 0; i < sideVecs.length; i++) {
    const sv = sideVecs[i];
    if (SKIP_TYPES.has(sv.type)) continue;
    if (excludeSet.has((sv.label || '').toLowerCase())) continue;

    categories.push(sv.label || `Row ${i + 1}`);
    const cells = (i < cellRows.length ? cellRows[i].cell : []) || [];

    for (let s = 0; s < selectedCols.length; s++) {
      const colIdx = selectedCols[s].idx;
      const cell = colIdx < cells.length ? cells[colIdx] : {};
      // colpc is 0-1 proportion; multiply by 100 for percentage
      const value = (cell.colpc || 0) * 100;
      seriesValues[s].push(Math.round(value * 10) / 10);
    }
  }

  const seriesData = selectedCols.map((col, s) => ({
    name: col.name,
    labels: categories,
    values: seriesValues[s],
  }));

  return { categories, seriesData };
}

// ---------------------------------------------------------------------------
// Chart type mapping
// ---------------------------------------------------------------------------

/**
 * Map a human-readable chart type string to the PptxGenJS chart enum value
 * and any default options that should be applied.
 */
function resolveChartType(pptx, typeStr) {
  const t = (typeStr || 'bar').toLowerCase().replace(/[\s_-]/g, '');
  switch (t) {
    case 'bar':
      return { chartType: pptx.charts.BAR, defaults: { barDir: 'bar' } };
    case 'column':
      return { chartType: pptx.charts.BAR, defaults: { barDir: 'col' } };
    case 'stackedbar':
    case 'barstacked':
      return { chartType: pptx.charts.BAR, defaults: { barDir: 'bar', barGrouping: 'stacked' } };
    case 'stackedcolumn':
    case 'columnstacked':
      return { chartType: pptx.charts.BAR, defaults: { barDir: 'col', barGrouping: 'stacked' } };
    case 'pie':
      return { chartType: pptx.charts.PIE, defaults: {} };
    case 'line':
      return { chartType: pptx.charts.LINE, defaults: {} };
    case 'doughnut':
    case 'donut':
      return { chartType: pptx.charts.DOUGHNUT, defaults: {} };
    default:
      return { chartType: pptx.charts.BAR, defaults: { barDir: 'bar' } };
  }
}

// ---------------------------------------------------------------------------
// Slide file discovery
// ---------------------------------------------------------------------------

/**
 * Find all slide_NN.json files in a directory, sorted by their numeric index.
 */
function discoverSlideFiles(slidesDir) {
  const entries = fs.readdirSync(slidesDir)
    .filter(f => /^slide_\d+\.json$/i.test(f))
    .sort((a, b) => {
      const numA = parseInt(a.match(/\d+/)[0], 10);
      const numB = parseInt(b.match(/\d+/)[0], 10);
      return numA - numB;
    });
  return entries.map(f => path.join(slidesDir, f));
}

// ---------------------------------------------------------------------------
// Temp file management
// ---------------------------------------------------------------------------

function makeTempDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'pptx-report-'));
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main() {
  const args = parseArgs(process.argv);

  // Resolve paths relative to cwd
  const themePath = path.resolve(args.theme);
  const slidesDir = path.resolve(args.slides);
  const outputPath = path.resolve(args.output);
  const toolsDir = path.dirname(path.resolve(__filename));
  const templatesDir = path.join(toolsDir, 'slide-templates');

  // Read theme CSS
  if (!fs.existsSync(themePath)) {
    console.error(`Theme file not found: ${themePath}`);
    process.exit(1);
  }
  const themeCSS = fs.readFileSync(themePath, 'utf8');
  const accentColours = parseAccentColours(themeCSS);
  console.log(`Theme: ${path.basename(themePath)} (${accentColours.length} accent colours)`);

  // Discover slides
  if (!fs.existsSync(slidesDir)) {
    console.error(`Slides directory not found: ${slidesDir}`);
    process.exit(1);
  }
  const slideFiles = discoverSlideFiles(slidesDir);
  if (slideFiles.length === 0) {
    console.error(`No slide_NN.json files found in ${slidesDir}`);
    process.exit(1);
  }
  console.log(`Found ${slideFiles.length} slide(s)`);

  // Create presentation
  const pptx = new pptxgen();
  pptx.layout = 'LAYOUT_16x9';

  // Temp directory for intermediate HTML files
  const tmpDir = makeTempDir();

  for (let idx = 0; idx < slideFiles.length; idx++) {
    const slideFile = slideFiles[idx];
    const slideNum = idx + 1;
    let slideData;

    try {
      slideData = JSON.parse(fs.readFileSync(slideFile, 'utf8'));
    } catch (err) {
      console.error(`[Slide ${slideNum}] Failed to parse ${path.basename(slideFile)}: ${err.message}`);
      continue;
    }

    const templateName = slideData.template || 'title';
    const templatePath = path.join(templatesDir, `${templateName}.html`);

    if (!fs.existsSync(templatePath)) {
      console.error(`[Slide ${slideNum}] Template not found: ${templateName}.html — skipping`);
      continue;
    }

    // Read template, inject theme, populate placeholders
    let html = fs.readFileSync(templatePath, 'utf8');
    html = injectThemeCSS(html, themeCSS);
    html = populateTemplate(html, slideData);

    // Write temp HTML file
    const tempHtml = path.join(tmpDir, `slide_${String(slideNum).padStart(2, '0')}.html`);
    fs.writeFileSync(tempHtml, html, 'utf8');

    // Convert HTML to PPTX slide
    let result;
    try {
      result = await html2pptx(tempHtml, pptx, { tmpDir });
    } catch (err) {
      console.error(`[Slide ${slideNum}] html2pptx failed: ${err.message}`);
      continue;
    }

    const { slide, placeholders } = result;
    console.log(`[Slide ${slideNum}] ${templateName} — "${slideData.title || '(no title)'}" — ${placeholders.length} placeholder(s)`);

    // Add chart if the slide defines one and html2pptx returned placeholders
    if (slideData.chart && placeholders.length > 0) {
      const chartDef = slideData.chart;
      const dataFile = chartDef.data_file;

      if (!dataFile || !fs.existsSync(dataFile)) {
        console.warn(`[Slide ${slideNum}] Chart data file not found: ${dataFile || '(none)'} — skipping chart`);
        continue;
      }

      let parsed;
      try {
        parsed = parsePlatinumData(
          dataFile,
          chartDef.series_columns || [],
          chartDef.exclude_rows || []
        );
      } catch (err) {
        console.warn(`[Slide ${slideNum}] Failed to parse chart data: ${err.message} — skipping chart`);
        continue;
      }

      if (parsed.categories.length === 0 || parsed.seriesData.length === 0) {
        console.warn(`[Slide ${slideNum}] Chart data is empty after filtering — skipping chart`);
        continue;
      }

      const { chartType, defaults } = resolveChartType(pptx, chartDef.type);
      const placeholder = placeholders[0];

      // Build chart options
      const chartOpts = {
        ...placeholder,
        ...defaults,
        showTitle: false,
        showLegend: parsed.seriesData.length > 1,
        legendPos: 'b',
        dataLabelFormatCode: '0"%"',
        chartColors: accentColours.length > 0
          ? accentColours.slice(0, parsed.seriesData.length || 1)
          : undefined,
      };

      // Pie/doughnut get extra options
      const isPie = chartDef.type && /pie|doughnut|donut/i.test(chartDef.type);
      if (isPie) {
        chartOpts.showPercent = true;
        chartOpts.showLegend = true;
        chartOpts.legendPos = 'r';
        delete chartOpts.dataLabelFormatCode;
      }

      try {
        slide.addChart(chartType, parsed.seriesData, chartOpts);
        console.log(`[Slide ${slideNum}] Added ${chartDef.type} chart (${parsed.categories.length} categories, ${parsed.seriesData.length} series)`);
      } catch (err) {
        console.warn(`[Slide ${slideNum}] Failed to add chart: ${err.message}`);
      }
    }
  }

  // Ensure output directory exists
  const outputDir = path.dirname(outputPath);
  if (!fs.existsSync(outputDir)) {
    fs.mkdirSync(outputDir, { recursive: true });
  }

  // Save presentation
  await pptx.writeFile({ fileName: outputPath });
  console.log(`\nSaved: ${outputPath}`);

  // Clean up temp files
  try {
    const tmpFiles = fs.readdirSync(tmpDir);
    for (const f of tmpFiles) {
      fs.unlinkSync(path.join(tmpDir, f));
    }
    fs.rmdirSync(tmpDir);
  } catch (_) {
    // non-critical
  }
}

main().catch(err => {
  console.error('Fatal error:', err.message);
  process.exit(1);
});
