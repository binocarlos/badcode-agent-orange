#!/usr/bin/env python3
"""Render PPTX slides to PNG images. Used by agent-service for auto-conversion."""
import sys, json, logging
logging.basicConfig(level=logging.WARNING, stream=sys.stderr)
sys.path.insert(0, '/workspace/lib')
from style_extract import pptx_to_images
print(json.dumps(pptx_to_images(sys.argv[1])))
