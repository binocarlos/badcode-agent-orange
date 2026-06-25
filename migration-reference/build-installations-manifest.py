#!/usr/bin/env python3
"""Aggregate installations/*/installation.json into goapi/pkg/installations/manifest.json.

Also supports --set-image <name> <registry> <digest> <commit> to record a pushed
image digest into the per-folder installation.json, then regenerate the aggregate.
"""
import json, sys, os, datetime, glob

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
INSTALLS = os.path.join(ROOT, "installations")
OUT = os.path.join(ROOT, "goapi", "pkg", "installations", "manifest.json")


def _pkgs_after(cmd, marker):
    """Tokens following `marker` up to the next `&&`, dropping flags. Used to pull
    package names out of apt/pip/npm install commands for the base-image info UI."""
    idx = cmd.find(marker)
    if idx < 0:
        return []
    rest = cmd[idx + len(marker):].split("&&")[0]
    out = []
    for tok in rest.split():
        tok = tok.strip()
        if not tok or tok.startswith("-") or tok == ";":
            continue
        out.append(tok)
    return out


def parse_dockerfile(path):
    """Best-effort extraction of base image + installed packages from a Dockerfile.
    Degrades gracefully (empty lists) on unfamiliar syntax; the raw Dockerfile is
    always included so nothing is lost. Powers the 'Base image' info dialog."""
    if not os.path.exists(path):
        return {}
    raw = open(path).read()
    joined = raw.replace("\\\n", " ")  # collapse backslash line continuations
    base_image = None
    apt, pip, npm = [], [], []
    for line in joined.splitlines():
        s = line.strip()
        if s.startswith("ARG BASE_IMAGE="):
            base_image = s[len("ARG BASE_IMAGE="):].strip()
        if "apt-get install" in s:
            apt += _pkgs_after(s, "apt-get install")
        if "pip3 install" in s:
            pip += _pkgs_after(s, "pip3 install")
        elif "pip install" in s:
            pip += _pkgs_after(s, "pip install")
        if "npm install -g" in s:
            npm += _pkgs_after(s, "npm install -g")
    out = {"dockerfile": raw}
    if base_image:
        out["baseImage"] = base_image
    if apt:
        out["aptPackages"] = apt
    if pip:
        out["pipPackages"] = pip
    if npm:
        out["npmPackages"] = npm
    return out


def load_all():
    items = []
    for p in sorted(glob.glob(os.path.join(INSTALLS, "*", "installation.json"))):
        d = json.load(open(p))
        d.update(parse_dockerfile(os.path.join(os.path.dirname(p), "Dockerfile")))
        items.append(d)
    return items

def regenerate():
    items = load_all()
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    open(OUT, "w").write(json.dumps(items, indent=2) + "\n")
    print(f"wrote {OUT} ({len(items)} installations)")

def set_image(name, registry, digest, commit, base_digest=""):
    p = os.path.join(INSTALLS, name, "installation.json")
    d = json.load(open(p))
    img = {
        "registry": registry,
        "digest": "sha256:" + digest if not digest.startswith("sha256:") else digest,
        "builtCommit": commit,
        "builtAt": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }
    if base_digest:
        img["baseDigest"] = base_digest if base_digest.startswith("sha256:") else "sha256:" + base_digest
    d["image"] = img
    open(p, "w").write(json.dumps(d, indent=2) + "\n")
    print(f"set image for {name}")

if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--set-image":
        base_digest = sys.argv[6] if len(sys.argv) > 6 else ""
        set_image(sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5], base_digest)
    regenerate()
