#!/usr/bin/env python3
"""Standalone PPTX pipeline test for running inside the sandbox container.

NOT part of the test suite -- this is a diagnostic tool for iterating on
LibreOffice configuration inside the sandbox Docker container.

Usage:
    python3 /workspace/lib/test_pptx_pipeline.py /tmp/test.pptx

Or via ./stack:
    ./stack test_sandbox_pptx
"""
import json
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

passed = 0
failed = 0


def report(name, ok, detail=""):
    global passed, failed
    status = "PASS" if ok else "FAIL"
    if ok:
        passed += 1
    else:
        failed += 1
    print(f"  [{status}] {name}")
    if detail:
        for line in detail.strip().split("\n"):
            print(f"         {line}")


def main():
    if len(sys.argv) < 2:
        print("Usage: test_pptx_pipeline.py <path-to-pptx>")
        sys.exit(1)

    pptx_path = sys.argv[1]
    if not os.path.isfile(pptx_path):
        print(f"Error: file not found: {pptx_path}")
        sys.exit(1)

    print(f"\n=== PPTX Pipeline Test ===")
    print(f"Input: {pptx_path} ({os.path.getsize(pptx_path)} bytes)\n")

    # 1. LibreOffice health check
    try:
        r = subprocess.run(["soffice", "--version"], capture_output=True, timeout=10)
        version = (r.stdout or b"").decode().strip()
        report("LibreOffice version", r.returncode == 0, version or f"rc={r.returncode}")
    except FileNotFoundError:
        report("LibreOffice version", False, "soffice not found in PATH")
    except subprocess.TimeoutExpired:
        report("LibreOffice version", False, "timed out after 10s")

    # 2. PPTX -> PDF via LibreOffice
    pdf_path = None
    try:
        r = subprocess.run(
            ["soffice", "--headless", "--convert-to", "pdf", "--outdir", "/tmp", pptx_path],
            capture_output=True, timeout=120,
        )
        base = os.path.splitext(os.path.basename(pptx_path))[0]
        pdf_path = f"/tmp/{base}.pdf"
        ok = r.returncode == 0 and os.path.isfile(pdf_path)
        detail = ""
        if not ok:
            stderr = (r.stderr or b"").decode()[:500]
            detail = f"rc={r.returncode}\nstderr: {stderr}"
        else:
            detail = f"{os.path.getsize(pdf_path)} bytes"
        report("PPTX -> PDF (LibreOffice)", ok, detail)
    except FileNotFoundError:
        report("PPTX -> PDF (LibreOffice)", False, "soffice not found")
    except subprocess.TimeoutExpired:
        report("PPTX -> PDF (LibreOffice)", False, "timed out after 120s")

    # 3. PDF -> PNGs via pypdfium2
    png_paths = []
    if pdf_path and os.path.isfile(pdf_path):
        try:
            import pypdfium2 as pdfium
            pdf = pdfium.PdfDocument(pdf_path)
            out_dir = "/tmp/test_slides"
            os.makedirs(out_dir, exist_ok=True)
            for i in range(len(pdf)):
                page = pdf[i]
                bmp = page.render(scale=2)
                img = bmp.to_pil()
                p = os.path.join(out_dir, f"slide_{i+1:03d}.png")
                img.save(p, "PNG")
                png_paths.append(p)
            pdf.close()
            ok = len(png_paths) > 0
            sizes = [f"{os.path.getsize(p)} bytes" for p in png_paths[:3]]
            detail = f"{len(png_paths)} slides: {', '.join(sizes)}"
            report("PDF -> PNGs (pypdfium2)", ok, detail)
        except ImportError:
            report("PDF -> PNGs (pypdfium2)", False, "pypdfium2 not installed")
        except Exception as exc:
            report("PDF -> PNGs (pypdfium2)", False, str(exc))
    else:
        report("PDF -> PNGs (pypdfium2)", False, "skipped (no PDF from step 2)")

    # 4. Full pptx_to_images()
    try:
        from style_extract import pptx_to_images
        slides = pptx_to_images(pptx_path, output_dir="/tmp/test_pptx_to_images")
        ok = len(slides) > 0
        detail = f"{len(slides)} slides" if ok else "returned empty list"
        report("pptx_to_images()", ok, detail)
    except Exception as exc:
        report("pptx_to_images()", False, str(exc))

    # 5. extract_from_pptx()
    try:
        from style_extract import extract_from_pptx
        design = extract_from_pptx(pptx_path)
        ok = design is not None and "colors" in design and "fonts" in design
        detail = ""
        if ok:
            n_colors = len(design.get("colors", {}))
            n_layouts = len(design.get("layouts", []))
            detail = f"{n_colors} theme colors, {n_layouts} layouts, fonts: {design['fonts']}"
        else:
            detail = f"returned: {design}"
        report("extract_from_pptx()", ok, detail)
    except Exception as exc:
        report("extract_from_pptx()", False, str(exc))

    # 6. process_pptx.py CLI
    try:
        script = os.path.join(os.path.dirname(os.path.abspath(__file__)), "process_pptx.py")
        r = subprocess.run(
            [sys.executable, script, pptx_path],
            capture_output=True, timeout=180,
        )
        if r.returncode == 0:
            data = json.loads(r.stdout.decode())
            has_design = "design" in data and data["design"]
            has_style = "style" in data
            has_slides = "slides" in data
            ok = has_design and has_style and has_slides
            detail = f"design={'yes' if has_design else 'no'}, style={'yes' if has_style else 'no'}, slides={len(data.get('slides', []))}"
            if data.get("slide_error"):
                detail += f"\nslide_error: {data['slide_error']}"
            report("process_pptx.py CLI", ok, detail)
        else:
            stderr = (r.stderr or b"").decode()[:500]
            report("process_pptx.py CLI", False, f"rc={r.returncode}\n{stderr}")
    except Exception as exc:
        report("process_pptx.py CLI", False, str(exc))

    # Summary
    total = passed + failed
    print(f"\n{'='*40}")
    print(f"Results: {passed}/{total} passed, {failed}/{total} failed")
    if failed > 0:
        print("Some tests failed -- see details above")
    print()
    sys.exit(1 if failed > 0 else 0)


if __name__ == "__main__":
    main()
