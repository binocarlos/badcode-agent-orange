#!/bin/bash
# Scaffold an additional no-build webapp for web visualizations.
# Usage: bash /workspace/lib/viz/scaffold.sh <project-name>
#
# NOTE: /workspace/webapp/ is the primary no-build webapp (plain HTML + CDN import
# map, no Vite/npm). Use this script only when you need a SECOND, separate webapp.
#
# Creates /workspace/<project-name>/ as a copy of the webapp starter (index.html
# with the import map, app.js, style.css, lib/). Edit app.js, then:
#   screenshot_url(url="/workspace/<project-name>/index.html")
#   register_artifact(file_path="<project-name>/index.html", artifact_type="webapp")

set -euo pipefail

PROJECT_NAME="${1:?Usage: scaffold.sh <project-name>}"
PROJECT_DIR="/workspace/${PROJECT_NAME}"
TEMPLATE_DIR="/workspace/webapp"

if [ -d "$PROJECT_DIR" ]; then
  echo "Error: $PROJECT_DIR already exists" >&2
  exit 1
fi
if [ ! -d "$TEMPLATE_DIR" ]; then
  echo "Error: webapp starter not found at $TEMPLATE_DIR" >&2
  exit 1
fi

cp -r "$TEMPLATE_DIR" "$PROJECT_DIR"

echo "Scaffolded no-build webapp at $PROJECT_DIR"
echo "Edit $PROJECT_DIR/app.js, then screenshot_url + register_artifact ($PROJECT_NAME/index.html, artifact_type=webapp)."
