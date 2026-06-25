---
name: hoist-skill
description: Package what we just built into a reusable skill the user can keep and reuse
triggers:
  - save this as a skill
  - package this up
  - hoist this skill
  - make this reusable
  - turn this into a skill
keywords: [skill, hoist, package, reusable, save, bundle]
---

# Hoisting a Skill

Use this when the user wants to package work you just did into a reusable **skill** for future
sessions. Your job is to run a short interview, agree the details with the user, then call the
`hoist_skill` tool ONCE to write the bundle. Do not call `hoist_skill` until the interview is done.

## Run the interview with `ask_user`

Ask these one at a time using the `ask_user` tool. Propose a sensible default in each question so the
user can confirm with one click; allow free-text where they may want to edit.

1. **Name & purpose.** Propose a kebab-case `name` (lowercase, hyphen-separated) and a one-line
   `description`. Confirm or let them edit.
2. **Triggers & keywords.** Propose the natural-language phrases that should make this skill fire later,
   plus a few keywords. Confirm/edit.
3. **Which files belong.** Look back over this session — the files you wrote or edited and the scripts
   you ran — and propose the list of files that are part of the skill. Let the user add/remove paths
   (free-text). These become the bundle's `bundled_files` (workspace-relative `src` paths).
4. **Install step.** Ask whether anything must be installed for the skill to work (apt/pip/npm). If
   yes, draft an `install.sh` body and show it for review. If nothing is needed, skip it.
5. **Visibility.** Ask `private` (only you) or `organizational` (your whole customer). Default
   `organizational`. (Note to the user: making it **public** is a separate, reviewed step done later —
   you cannot set public here.)

## Hone the prose

Before calling the tool, write a clear, well-structured SKILL.md **body** (the `body` argument): what
the skill does, when it triggers, and step-by-step how to do it — good enough that a future session can
follow it without this conversation. This prose is the main value; make it sharp.

**Reference bundled files at their installed location.** Every `bundled_files` entry is copied into a
`files/` subdirectory of the skill, and when the skill is installed it lives at
`/workspace/.claude/skills/<name>/files/<dest>`. When your body tells a future session to run or read a
bundled file, you MUST use that full installed path — e.g.
`python /workspace/.claude/skills/<name>/files/script.py`. Do NOT reference a bare `/workspace/script.py`
or a path relative to where the file happened to sit in this session; those will not exist after install
and the run will fail with "No such file or directory".

## Commit

Call `hoist_skill` once with: `name`, `description`, `body`, `triggers`, `keywords`, `bundled_files`
(each `{ src, dest? }`), `install_script` (only if needed), and `visibility`. Then tell the user the
skill was packaged and is available as a downloadable bundle.

If the user changes their mind mid-interview, simply stop — nothing is written until you call the tool.
