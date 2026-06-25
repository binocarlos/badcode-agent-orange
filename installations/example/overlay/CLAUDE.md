# Project workspace

Baked into the image at `/workspace/CLAUDE.md` from
`installations/example/overlay/`. It's the project's standing memory — the agent
reads it on every session launched from this installation.

When you copy `example` into a real per-project installation, replace this with
the project brief: what it is, where things live, how to run and test it, and
any conventions the agent must follow.

This example adds nothing beyond `core`; it exists to show the layering
(sandbox → core → example) and the `overlay/` workspace-seeding mechanism.
