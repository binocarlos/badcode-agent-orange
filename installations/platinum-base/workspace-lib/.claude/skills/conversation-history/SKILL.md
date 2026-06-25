---
name: conversation-history
description: Team knowledge base providing access to previous agent conversations
triggers:
  - what have colleagues looked at
  - previous conversations
  - team knowledge base
  - has anyone looked at this before
  - search past sessions
keywords: [conversations, history, team, knowledge, previous, search, colleagues]
---

# Team Knowledge Base

## When to Use
- When exploring a new topic — previous conversations appear automatically in `pt search` results
- When a user asks about specific data that colleagues may have already investigated
- When discovering unfamiliar variables or tables that someone may have previously documented

## Reference

Previous agent conversations from your entire team are automatically included in `pt search` results. When you run `pt search "<query>"`, the output includes a "Previous Conversations" section showing what colleagues have already explored on that topic.

### Commands (these are the ONLY conversation commands)
- `pt chats list` — browse recent conversations (title, date, and the `pt chats show` command for each)
- `pt chats search "<query>"` — find conversations by topic (semantic + keyword)
- `pt chats show <sessionId>` — load ONE conversation's full transcript

**To load a past conversation, the recipe is: `pt chats search "<topic>"` (or `pt chats list`) → copy the `pt chats show <id>` line it prints → run it.** Do NOT use `pt search` to find a conversation — that searches dataset variables and tables, not conversations. There is no `pt chats list --format ...` filtering beyond `--limit`/`--job`; if you need a specific chat, search for it.

Search results show only a short **summary** of each past conversation, not its full content. When a surfaced conversation looks relevant — or the user confirms it's the one they mean — load the **entire** transcript with `pt chats show <sessionId>` (the session ID is printed in every search result) and read it before answering. This gives you the full back-and-forth, the exact figures, and any conclusions, so you can build on that work rather than starting over. Search is semantic: it matches on meaning, so a question worded differently from the original still surfaces relevant past chats.

### What to Look For
- **Previous conversations appear automatically** in every `pt search` — check the "Previous Conversations" section at the bottom for relevant prior work
- **When a user asks about specific data**: Previous conversations often contain the answer already — the right variables, useful cross-tabs, or insights a colleague found
- **When discovering unfamiliar variables or tables**: Check if someone has previously investigated them and documented what they learned

### How to Report Findings
- If relevant previous work exists, proactively tell the user: mention who explored it, when, and what they found
- Suggest building on previous work rather than starting from scratch
- If no relevant conversations exist, proceed normally without mentioning the search

### Tips
- Keep queries short (1-3 words): `pt chats search "brand awareness"`
- Search from multiple angles for broad topics

## Rules
- Always check the "Previous Conversations" section in `pt search` results before starting fresh analysis
- When a relevant conversation is found (or the user confirms one), load it in full with `pt chats show <sessionId>` and digest it before answering — do not rely on the summary alone
- Proactively report relevant previous work to the user
- Do not mention the search if no relevant conversations are found
