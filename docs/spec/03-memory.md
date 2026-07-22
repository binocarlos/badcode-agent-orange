# Spec — The memory system

**Part of the product spec.** Entry point and binding principles: [`../17-product-spec.md`](../17-product-spec.md).
Append-only labeled memory: data model, selectors, MCP tool surface, relevance contract. Section numbers (§) are kept from the original single-file spec, so cross-references
like §7.6 or §8.8 anywhere in the repo still resolve — the entry point has the full map.

---

## 7. The memory system

The single most load-bearing new substrate. Core provides an *opinion-free* store; all usage
policy lives in prompts (P1).

### 7.1 Data model

Table `memories`:

| column | type | meaning |
| --- | --- | --- |
| `id` | uuid PK | |
| `project` | text, indexed | **hardwired namespace — every query filters on it, enforced in code, not by the caller** |
| `labels` | jsonb, GIN-indexed | flat `map[string]string`, Kubernetes-style (`{"kind":"conversation-summary","worker":"email-answerer","thread":"cust-4711"}`) |
| `content` | text | the memory body — arbitrary text, often large (full transcripts allowed) |
| `content_embedding` | vector(1536), hnsw | optional; for semantic search (pgvector — this is why the image stays) |
| `created_by_worker` | text | provenance |
| `created_by_session` | text | provenance |
| `created_at` | bigint | |

**Memories are append-only and immutable.** A memory — content *and* labels — is a record of a
moment that happened in time; neither is ever updated after creation. There is no update and,
for now, no delete either. If future curation is ever needed (condensing, summarising,
reducing), it will be a *worker* that searches memories and replaces them by writing new ones
and deleting old ones — never by mutating existing rows — and delete gets added to the tool
surface only when that worker is actually being built.

Label keys and values are plain strings, ≤63 chars each (K8s limits, familiar and enough);
at most 32 labels per memory. **No controlled vocabulary is enforced by core.** (The A-MEM
research lesson — curate the vocabulary to avoid tag-soup — is implemented as *prompt guidance*:
a project convention memory, maintained by whichever worker the project appoints, that other
workers are prompted to consult. Mechanism/policy again.)

### 7.2 Label selectors

Adopt Kubernetes selector semantics exactly (they are well understood and already documented):

- equality: `worker=email-answerer`, `kind!=raw-transcript`
- set: `kind in (summary, lesson)`, `thread notin (spam)`, `exists thread`, `!archived`
- conjunction by comma: `worker=email-answerer,kind=summary`

Implement one parser + one SQL translator (jsonb `@>` / `?` / `->>` predicates) in
`go/agentdb/memories.go`, table-tested exhaustively. No OR, no nesting — if a worker needs OR
it runs two searches.

### 7.3 The memory MCP tool (core, granted to every session)

Served by agentd (an HTTP MCP server the sandbox reaches through the existing session-token
auth; project scope comes from the token, so a session physically cannot cross projects):

- `memory_create(content, labels)` → id
- `memory_search(label_selector?, query?, limit?)` →
  `[{id, labels, snippet, score, created_by_worker, created_by_session, session_url, created_at}]`
  - `label_selector` filters first (always ANDed with `project`); `query` then ranks per the
    relevance contract (§7.6); both optional — a bare selector lists newest-first.
  - **Provenance is part of the result, not an extra**: every hit says which worker wrote it,
    in which session, with a clickable permalink to that session in the web UI. This is what
    lets a worker answer "we already worked this out — here's the conversation: <link>" and
    put the human one click from the full original thread.
- `memory_get(id)` → full content + labels + provenance (search returns snippets; get returns
  everything)

That is the whole surface: create, search, get. Deliberately absent (per §7.1 immutability):
`memory_update_content`, `memory_update_labels`, and `memory_delete`. Anything that looks like
"changing" a memory is expressed by appending a newer one — readers that want "current" take
the most recent match, and the history stays honest.

### 7.4 Rolling summaries (convention, not mechanism)

Each worker *should* have a standing briefing injected into its prompt (§6.2 step 2.4). The
mechanism: at job-composition time, core runs one fixed query —
`labels: kind=rolling-summary, worker=<name>` (most recent match) — and injects its content
verbatim under a "Your memory briefing" heading. That is the *only* memory read core ever
performs.

Producing and maintaining that summary is a *worker's* job: the canonical arrangement is an
archivist worker subscribed to `worker.finished` whose prompt says "read the transcript, store
whatever is worth keeping with sensible labels, and append a fresh `kind=rolling-summary`
memory for the subject worker — a short paragraph giving the flavour of everything it has been
up to" (last-10 vs all-time weighting is that prompt's business, tweakable without touching
code). Append-only fits perfectly here: the composer takes the most recent summary, and the
superseded ones remain as an honest record of how the worker's self-picture evolved. No archivist wired ⇒ no summary ⇒ workers simply run without a briefing. Core never
auto-archives conversations (explicit decision — the naive "store every transcript" loop is
exactly the kind of opinion that must stay out of core).

### 7.5 Embeddings

`memory_create` computes `content_embedding` synchronously via the configured provider
(embedding endpoint config on agentd; absent config ⇒ column stays NULL and search degrades to
keyword-only per §7.6). Mock mode uses a deterministic fake embedder so e2e stays offline-green.

### 7.6 Retrieval and ranking — the relevance contract

The dominant consumer of search is an *agent under instruction* ("search your memory for X
before deciding"). It supplies words and labels; it must never need to supply weights, modes,
or tuning. So the backend's interpretation of "given plain text, return the most relevant
results" is a **fixed, documented contract** — priority is handled internally, and prompts can
rely on the behaviour being stable:

1. **Hard filter:** `project` (always, in code), then the label selector if given.
2. **No `query` text** ⇒ results are the filtered set, **newest first**. (A bare
   `kind=rolling-summary,worker=x` lookup is a recency question, not a relevance one.)
3. **With `query` text** ⇒ **hybrid retrieval, fused**: two legs run over the filtered set —
   - *keyword*: Postgres full-text (`tsvector`/`ts_rank`) over `content` — catches exact and
     custom terms an embedding model has no vocabulary for (project jargon, "BadCode", worker
     names, ticket ids);
   - *semantic*: pgvector cosine over `content_embedding` — catches paraphrase and "about-ness"
     where no words overlap;
   and the two rankings are combined with **Reciprocal Rank Fusion**
   (`score = Σ_legs 1/(60 + rank_leg)`), top-`limit` returned. RRF is deliberately chosen over
   weighted score blending: ranks are comparable when raw scores are not, it needs no tuning,
   and it is robust when one leg is empty or degenerate.
4. **Recency as tiebreak only**: equal fused scores order newest-first. No decay factor in v1 —
   importance/recency weighting schemes (Generative-Agents-style scoring) are a later
   experiment, and would land *inside* the contract, not as caller-visible knobs.
5. **Degradation**: no embeddings configured (or NULL-embedding rows) ⇒ the semantic leg is
   skipped and the contract silently becomes keyword+recency; the result shape never changes.

One SQL query implements this (two CTE legs + RRF join); it is the single most heavily
table-tested query in the system, including proofs that project filtering binds before
everything else.

### 7.7 Build on Postgres — the buy-vs-build decision

Question asked and answered: *is pgvector enough — should we outsource the memory system?*
**Decision: build on Postgres; do not adopt a memory framework.** pgvector alone is
vector-only, but we are not using pgvector alone: Postgres already provides the keyword leg
(full-text search), the label store (jsonb + GIN), the provenance joins (memories →
sessions), and transactions across all of them — hybrid search per §7.6 is one query in the
database we already run, operate, and back up. Dedicated memory systems (mem0, Zep/Graphiti,
Letta and kin) were surveyed during the research arc: they bring their own opinionated
schemas — auto-summarisation, entity graphs, fixed memory types — which is exactly the
opinion P1 keeps *out* of core, and none of them speak our label/selector model. Revisit only
with evidence (retrieval quality or scale failing in practice), and even then prefer stealing
their ranking ideas into §7.6 over adopting their stores.
