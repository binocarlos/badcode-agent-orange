#!/usr/bin/env bash
#
# Fail if any tracked non-binary file contains a raw NUL byte.
#
# Why this exists (doc 22, RD25 / doc 24, B4): a NUL used as a composite-key
# separator was written as a *literal byte* rather than an escape in five
# web/src files. The consequences were not cosmetic:
#
#   * grep classifies such a file as binary and reports NO MATCHES — so every
#     search anyone ran over web/src, including our own audits, silently lied.
#   * a NUL inside the first 8000 bytes also makes git render the file as
#     "Binary files differ", with a stat line reading 0 insertions, 0 deletions.
#     A PR touching the worker editor showed a reviewer nothing at all.
#   * anything that round-trips the file as text (a formatter, sed -i, a paste
#     through a terminal) drops the NUL, silently collapsing `${a}\0${b}` to
#     `${a}${b}` and making previously-distinct keys collide, with nothing
#     failing at that moment either.
#
# The fix in every case is to write the escape (backslash-u-0000, or
# backslash-u-001f where the separator is ephemeral) instead of the byte; the
# runtime string is unchanged. This check is what stops it recurring.
#
# Detection uses perl, deliberately, NOT grep:
#   * `grep -lP '\x00'` does NOT work — it reports nothing on a file that does
#     contain a NUL (verified on ugrep 7.5.0, 2026-08-06), because the file is
#     classified as binary before the pattern is ever applied.
#   * bash collapses $'\x00' to the empty string, which matches every file. The
#     first attempt at this check reported the entire repo as affected.
# Both failure modes are the shape of defect this repo is trying to kill: a
# confident answer that was never true. Verify a negative twice.
#
# Usage: scripts/check-no-nul-bytes.sh   (run from anywhere in the repo)

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# Files that are legitimately binary. Everything else is treated as source.
# A deny-list, not an allow-list: a new source extension is then covered
# automatically, and a new *binary* extension fails loudly and gets added here —
# which is the direction we want the mistake to point.
is_binary_path() {
  case "${1,,}" in
    *.png|*.jpg|*.jpeg|*.gif|*.ico|*.bmp|*.webp|*.avif) return 0 ;;
    *.pdf|*.zip|*.gz|*.tgz|*.bz2|*.xz|*.7z|*.tar) return 0 ;;
    *.woff|*.woff2|*.ttf|*.otf|*.eot) return 0 ;;
    *.mp3|*.mp4|*.webm|*.mov|*.wav|*.ogg) return 0 ;;
    *.jar|*.class|*.so|*.dylib|*.dll|*.wasm|*.bin) return 0 ;;
    *) return 1 ;;
  esac
}

offenders=()
while IFS= read -r -d '' path; do
  is_binary_path "$path" && continue
  [ -f "$path" ] || continue
  if perl -0777 -ne 'exit(/\x00/ ? 0 : 1)' -- "$path"; then
    count=$(perl -0777 -ne '$n = () = /\x00/g; print $n' -- "$path")
    offenders+=("$path ($count NUL byte(s))")
  fi
done < <(git ls-files -z)

if [ ${#offenders[@]} -gt 0 ]; then
  echo "ERROR: tracked source files contain raw NUL bytes:" >&2
  for o in "${offenders[@]}"; do
    echo "  $o" >&2
  done
  echo >&2
  echo "Write the escape instead of the byte (e.g. '\\u0000' or '\\u001f')." >&2
  echo "The runtime string is identical; the file stops greping as binary and" >&2
  echo "stops diffing as 'Binary files differ'. See doc 22 RD25 / doc 24 B4." >&2
  exit 1
fi

echo "OK: no raw NUL bytes in tracked source files."
