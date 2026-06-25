#!/bin/bash
# Scaffold an additional Vite project for web visualizations.
# Usage: bash /workspace/lib/viz/scaffold.sh <project-name>
#
# NOTE: /workspace/ itself is already a pre-initialized Vite app with auto-loading
# data-loader. Use this script only when you need a separate sub-project.
#
# Creates /workspace/<project-name>/ with:
#   index.html, vite.config.js, src/main.js
# Symlinks /opt/viz-template/node_modules for instant dependency access.

set -euo pipefail

PROJECT_NAME="${1:?Usage: scaffold.sh <project-name>}"
PROJECT_DIR="/workspace/${PROJECT_NAME}"

if [ -d "$PROJECT_DIR" ]; then
  echo "Error: $PROJECT_DIR already exists" >&2
  exit 1
fi

mkdir -p "$PROJECT_DIR/src/lib"
ln -s /workspace/data "$PROJECT_DIR/data"

# Copy PlatinumData JS helper into project
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cp "$SCRIPT_DIR/platinum-data.js" "$PROJECT_DIR/src/lib/platinum-data.js"

# Symlink pre-installed node_modules (no npm install needed)
ln -s /opt/viz-template/node_modules "$PROJECT_DIR/node_modules"

# package.json
cat > "$PROJECT_DIR/package.json" << 'PKGJSON'
{
  "name": "viz-project",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build"
  }
}
PKGJSON

# vite.config.js — base: './' ensures relative paths in build output
cat > "$PROJECT_DIR/vite.config.js" << 'VITECONFIG'
import { defineConfig } from 'vite'

export default defineConfig({
  base: './',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
VITECONFIG

# index.html — Vite entry point
cat > "$PROJECT_DIR/index.html" << 'INDEXHTML'
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Visualization</title>
  <style>
    * { margin: 0; padding: 0; box-sizing: border-box; }
    body { font-family: system-ui, -apple-system, sans-serif; }
    #app { width: 100vw; height: 100vh; }
  </style>
</head>
<body>
  <div id="app"></div>
  <script type="module" src="./src/main.js"></script>
</body>
</html>
INDEXHTML

# src/main.js — starter file
cat > "$PROJECT_DIR/src/main.js" << 'MAINJS'
// Your visualization code goes here.
// Available libraries: d3, three, chart.js, @observablehq/plot
// Import them as ES modules:
//   import * as d3 from 'd3'
//   import * as THREE from 'three'
//   import Chart from 'chart.js/auto'
//   import * as Plot from '@observablehq/plot'
//
// Load PlatinumData JSON:
//   import rawData from '../data/my-table.json'
//   import { toRecords, toMatrix } from './lib/platinum-data.js'
//   const records = toRecords(rawData)       // [{row, col, value}, ...]
//   const matrix = toMatrix(rawData, 'freq') // {rows, columns, values}

const app = document.getElementById('app')
app.innerHTML = '<h1>Visualization</h1>'
MAINJS

echo "Scaffolded Vite project at $PROJECT_DIR"
echo "Edit src/main.js, then run: cd $PROJECT_DIR && vite build"
