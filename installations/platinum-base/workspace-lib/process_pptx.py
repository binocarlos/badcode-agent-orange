#!/usr/bin/env python3
"""One-command PPTX processing: extract design + render slides.

Usage:
    python3 /workspace/lib/process_pptx.py /workspace/uploads/template.pptx

Outputs JSON to stdout with design spec, style spec, and slide image paths.
When LibreOffice fails, design/style data is still returned with a slide_error.
"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from style_extract import extract_from_pptx, pptx_to_images, build_style_spec


def main():
    if len(sys.argv) < 2:
        print(json.dumps({"error": "Usage: process_pptx.py <path-to-pptx>"}))
        sys.exit(1)

    pptx_path = sys.argv[1]
    if not os.path.isfile(pptx_path):
        print(json.dumps({"error": f"File not found: {pptx_path}"}))
        sys.exit(1)

    # Phase 1: Extract design (always works, pure python-pptx)
    design = extract_from_pptx(pptx_path)
    if design is None:
        design = {}

    style = build_style_spec(design)

    # Phase 2: Render slides (requires LibreOffice)
    slides = []
    slide_error = None
    try:
        slides = pptx_to_images(pptx_path)
        if not slides:
            slide_error = "pptx_to_images returned empty list (LibreOffice may have failed)"
    except Exception as exc:
        slide_error = str(exc)

    result = {
        "design": design,
        "style": style,
        "slides": slides,
        "slide_error": slide_error,
    }

    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
