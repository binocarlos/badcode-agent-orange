#!/usr/bin/env python3
"""Screenshot a URL or local HTML file using headless Chromium.

Usage:
    python3 screenshot.py <url_or_path> <output_path> [--width W] [--height H] [--wait MS]

Serves local files via a temporary HTTP server (ES modules require http://).
Uses --virtual-time-budget for JS rendering delay.
"""
import argparse
import os
import subprocess
import sys
import threading
from http.server import HTTPServer, SimpleHTTPRequestHandler


def find_chromium():
    """Find the chromium binary."""
    for name in ['chromium-browser', 'chromium', 'google-chrome']:
        try:
            subprocess.run([name, '--version'], capture_output=True, timeout=5)
            return name
        except (FileNotFoundError, subprocess.TimeoutExpired):
            continue
    return None


def screenshot(url, output_path, width=1280, height=800, wait_ms=3000):
    """Take a screenshot of url, save to output_path. Returns output_path."""
    chromium = find_chromium()
    if not chromium:
        print('ERROR: chromium not found', file=sys.stderr)
        sys.exit(1)

    # Serve local files via temporary HTTP server (ES modules need http://)
    http_server = None
    if not url.startswith(('http://', 'https://', 'file://')):
        abs_path = os.path.abspath(url)
        if not os.path.isfile(abs_path):
            print(f'ERROR: file not found: {abs_path}', file=sys.stderr)
            sys.exit(1)
        serve_dir = os.path.dirname(abs_path)
        filename = os.path.basename(abs_path)

        class QuietHandler(SimpleHTTPRequestHandler):
            def __init__(self, *args, **kwargs):
                super().__init__(*args, directory=serve_dir, **kwargs)
            def log_message(self, format, *args):
                pass

        http_server = HTTPServer(('127.0.0.1', 0), QuietHandler)
        port = http_server.server_address[1]
        threading.Thread(target=http_server.serve_forever, daemon=True).start()
        url = f'http://127.0.0.1:{port}/{filename}'

    os.makedirs(os.path.dirname(output_path) or '.', exist_ok=True)

    cmd = [
        chromium,
        '--headless',
        '--no-sandbox',
        '--disable-gpu',
        '--disable-software-rasterizer',
        f'--screenshot={output_path}',
        f'--window-size={width},{height}',
        '--hide-scrollbars',
    ]
    # virtual-time-budget is incompatible with HTTP: Chromium's virtual time
    # doesn't advance during real network I/O, causing a deadlock. Only use
    # it for file:// URLs where everything is local.
    if not http_server:
        cmd.append(f'--virtual-time-budget={wait_ms}')
    cmd.append(url)

    try:
        result = subprocess.run(cmd, capture_output=True, timeout=30)
    finally:
        if http_server:
            http_server.shutdown()

    if result.returncode != 0:
        print(f'ERROR: chromium exited with {result.returncode}', file=sys.stderr)
        if result.stderr:
            print(result.stderr.decode()[:500], file=sys.stderr)
        sys.exit(1)

    if not os.path.isfile(output_path):
        print(f'ERROR: screenshot not created at {output_path}', file=sys.stderr)
        sys.exit(1)

    return output_path


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='Screenshot a URL or local HTML file')
    parser.add_argument('url', help='URL or local file path to screenshot')
    parser.add_argument('output', help='Output PNG path')
    parser.add_argument('--width', type=int, default=1280, help='Viewport width (default: 1280)')
    parser.add_argument('--height', type=int, default=800, help='Viewport height (default: 800)')
    parser.add_argument('--wait', type=int, default=3000, help='JS render budget in ms (default: 3000)')
    args = parser.parse_args()

    out = screenshot(args.url, args.output, args.width, args.height, args.wait)
    print(out)
