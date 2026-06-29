<!--
Generated 2026-06-26 by an exhaustive discovery workflow (memory-hr-landscape):
14 parallel search angles -> fetch + per-system extraction (lists exploded) ->
completeness-critic round -> synthesis. ~227 raw records (deduped in the report).
Companion docs: AGENTS_ORCHESTRATION_LANDSCAPE.md, AGENTS_STACK_DECISION.md,
AGENTS_RESEARCH.md.
NOTE: benchmark %s, star counts, "SOTA" claims are vendor/author-reported, NOT independently verified.
Treat as a map to investigate, not gospel — re-check a system's primary repo before adopting.
-->

# Agent Memory, Self-Organizing Labels, Skill & Prompt Management — Landscape & Design Guidance

> Scope note: the raw catalog contains ~130 records but many are duplicate entries for the same system (Mem0 ×4, Voyager ×5, Letta/MemGPT ×6, Zep ×4, ExpeL ×3, Cognee ×3, Generative Agents ×3, etc.). The tables below **dedupe to one row per distinct system** and merge the duplicates' fields; every distinct system in the catalog is present. Where a system legitimately appears under two functions (e.g. Letta as memory-framework *and* self-organizing-memory), it is listed under its primary category and cross-referenced.
> Skeptical posture: LongMemEval / LoCoMo percentages, GitHub star counts, and "SOTA" claims below are vendor- or author-reported and are **not** independently verified. Treat them as marketing until reproduced.

---

## 1. Executive read

We have seven needs. Here is the blunt buy-vs-build verdict for each, with the single best candidate.

- **Labeled shared memory store (search + label + query over MCP):** Mature prior art exists — **adopt**. Best candidate: **mcp-memory-service** (self-hostable, MCP-native, hybrid BM25+vector, tag browsing, 76-endpoint REST, Apache-2.0) for a build-it-yourself stance, or **Mem0** if you want the most-adopted engine with a consolidation pipeline. For a *governed, fleet-scoped* store the closest is **MemClaw (Caura)**.
- **Self-organizing / auto-label taxonomy (without tag-soup):** No product to buy; the **pattern** is mature — **steal from A-MEM** (Zettelkasten note generation + link evolution) and **SwiftMem** (embedding-tag co-consolidation). You must build the *governed* version because every self-organizing system in the catalog grows labels bottom-up with no curator; that is exactly the tag-soup you fear.
- **Memory-as-MCP:** Solved many times over — **adopt**. Best turnkey: **mcp-memory-service** / **Basic Memory** (Markdown-as-graph) / **Memorizer** (.NET, Postgres+pgvector, versioned). Pick one; do not write an MCP memory server from scratch.
- **Learning-from-finished-sessions (summarize→label→store):** "Consolidation" is table-stakes in every memory framework; the *experience-abstraction* layer you actually want is research-grade — **steal patterns**. Best candidates: **Agent Workflow Memory** and **Memp** (induce reusable procedures from trajectories), **ReasoningBank** (distill from successes *and* failures into titled items), **ExpeL** (contrastive rules-of-thumb).
- **Skill/tool management per worker (dynamic provisioning):** Ecosystem is rich but fragmented — **adopt the SKILL.md standard + a loader, build the manager logic**. Best candidates: **OpenSkills** (cross-runtime SKILL.md loader) + **eagle-eye** (5-stage skill routing) + **Voyager**'s validate-before-store discipline. No system does "manager assigns skills per worker by role" end-to-end.
- **Dynamic prompt management (compose system prompts on the fly):** Optimizers exist (**GEPA, DSPy, OPRO, PromptBreeder, ACE**) but they tune prompts *offline against a metric*; runtime per-worker composition-as-HR does not exist — **build, steal from ACE** (incremental delta updates, anti-collapse) and **AutoGuide** (context-keyed guideline injection).
- **Agent capability registry / manager-as-HR:** This is the genuinely thin area. Closest prior art: **AgentStore** (specialist registry + learned meta-controller), **IntentKit** (intent→skill routing), **MemClaw** (permission-tagged fleet memory), **mission-control** (fleet dispatch dashboard). None integrates capability registry + memory governance + prompt composition over a pluggable runtime — **build**.

Bottom line: **buy the memory substrate and the MCP surface; steal the consolidation, auto-labeling, and skill-validation patterns; build the HR governance loop and the verification harness.** The integrated "manager-as-HR" is the novel surface — nobody ships it.

---

## 2. Full catalog by category

Legend: **Mem** = memory model · **Lbl** = labeling · **Retr** = retrieval · **MCP** = is-MCP-server · **Learn** = learns-from-experience · **S/P** = skill/prompt mgmt · **SH** = self-host · **Rel** = relevance.

### 2.1 Memory frameworks (general-purpose stores)

| System | What it is | Mem | Lbl | Retr | MCP | Learn | S/P | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **Mem0** | Most-adopted standalone memory layer; vector+graph+KV hybrid, auto-extracts salient facts | vec+graph+kv | metadata | hybrid | yes (server) | consolidation | no | yes/no(cloud) | ~48K★, YC, $24M A, SOC2/HIPAA; 49% LongMemEval (unimpressive) | high |
| **Zep / Graphiti** | Temporal KG; every fact carries validity windows, point-in-time queries | bi-temporal graph | auto+meta | graph+hybrid | yes | consolidation | no | yes (Graphiti); Zep CE deprecated | 63.8% LongMemEval, peer-reviewed | high |
| **LangMem** | LangGraph memory lib; hot-path tools + background managers; prompt-optimization feature | tiered/epi+sem+proc | meta/manual | vector | unknown | consolidation | desc | yes | ~1.3K★, MIT, coupled to LangGraph, cadence slowed | high |
| **LlamaIndex Memory** | Composable memory blocks (FIFO, summary buffer, vector) | tiered/vec | meta/manual | hybrid | unknown | consolidation | no/desc | yes | mature ecosystem, no temporal/graph | med |
| **MemoryOS** (2506.06326) | Memory-as-OS-service: storage/index/retrieve/GC APIs decoupled from reasoning | tiered (short/mid/long) | metadata | hybrid | unknown | consolidation | no | yes | paper 2025 | high |
| **MemOS** (2507.03724) | Memory OS with process-style isolation + memory-bus + consolidation scheduling | tiered | metadata | metadata-filtered | unknown | consolidation | no | unknown | paper | med |
| **MemoryBank** | Long-term store with Ebbinghaus forgetting curve | vec+meta | auto/meta | vector | no | consolidation | no | unknown | paper | med |
| **ChatDB** | DB-as-symbolic-memory; SQL read/write of structured state | structured/none | metadata | metadata-filtered | no | none | no | unknown | paper | low |
| **HippoRAG** | Neurobio: KG "neocortex" + Personalized-PageRank "hippocampal index" for multi-hop | graph+PPR | auto (OpenIE) | graph | unknown | none | no | yes | paper, popular | med |
| **AriGraph** | Learns KG world-models grounded in episodic memory | graph+epi | auto | graph | unknown | consolidation | no | unknown | paper | low |
| **MAGMA** | Multi-graph (temporal/semantic/causal) with weighted traversal | multi-graph | auto | graph | unknown | consolidation | no | unknown | paper | low |
| **HiMem** | Hierarchical summaries episode→session→theme, cross-level consolidation | tiered | metadata | hybrid | unknown | consolidation | no | unknown | paper | low |
| **GraphCogEnt** | Cognitive-entity KG updating relationships as info arrives | graph | auto | graph | no | consolidation | no | unknown | paper | med |
| **Optimus-1** | Hierarchical directed KG + abstracted multimodal experience pool | graph+pool | auto | hybrid | no | workflow-induction | desc | unknown | NeurIPS'24 | med |
| **RAP** | Retrieval-augmented planning; retrieves past experiences to condition plans | episodic | metadata | hybrid | no | none | desc | unknown | paper | med |
| **RET-LLM** | Writes structured triplets at write-time, NL queries | graph | manual | hybrid | no | none | no | unknown | paper | med |
| **RecMind** | Recommendation agent; personalized/world memory, self-inspiring planning | epi+sem+proc | manual | hybrid | no | consolidation | no | unknown | paper | low |
| **MIRIX** | Multi-agent, type-partitioned memory across specialist memory-manager agents | epi+sem+proc | auto | hybrid | unknown | consolidation | desc | yes | paper | med |
| **Memary** | Human-memory-emulating layer: KG + memory stream + entity store on ReAct | graph | auto | hybrid | no | consolidation | desc | yes | ~2.6K★, last rel Oct'24 | med |
| **Memobase** | User-profile memory; ingests blobs → structured evolving profile + timeline | tiered+epi | auto (config topics) | hybrid | yes | consolidation | no | yes | ~2.8K★, dockerized | med |
| **Memori** (GibsonAI) | Agent-native: captures tool calls/decisions/outcomes into structured state | tiered | metadata | hybrid | yes | consolidation | desc | yes | ~15.4K★, commercial | high |
| **Honcho** | User-modeling; per-user profiles/representations | vector | auto | metadata-filtered | yes | consolidation | no | yes | company | med |
| **mem9** | Persistent cloud memory for coding/agent stacks, hybrid recall | tiered | metadata | hybrid | unknown | none | no | yes | early-stage | med |
| **SuperMemory** | All-in-one memory+RAG+profiles+connectors, `container_tag` scoping | graph | metadata | hybrid | yes | consolidation | no | no | $3M, 81.6% LongMemEval, closed | high/med |
| **Membase** | Universal personal memory shared across tools; personal graph + reference wiki | graph | metadata | graph | unknown | consolidation | desc (wiki) | unknown | newer | med |
| **Semantic Kernel** (MS) | Model-agnostic SDK; plugins/skills, prompt templates, planners, vector memory | vector | metadata | vector | no | none | yes | yes | ~28K★, MIT | med |
| **SuperAGI** | Autonomous-agent platform; toolkit marketplace, memory storage, GUI | vector | unknown | vector | no | none | yes | yes | ~17.6K★, slowed | med |
| **MemoRAG** | Long-context LLM as global "memory model" generating retrieval clues | semantic | none | hybrid | no | none | desc | yes | ~2.2K★, TheWebConf'25 | low |
| **Amazon Bedrock AgentCore Memory** | Managed short+long-term; configurable extraction/consolidation strategies | epi+sem+proc | metadata | vector | unknown | consolidation | desc (custom extract prompts) | no | GA managed | high |
| **Vertex AI Memory Bank** (GCP) | Managed long-term user memories; async Gemini extraction + contradiction resolution | vector | metadata | hybrid | no | consolidation | no | no | public preview | med |
| **Utilizing Metadata for RAG** | Study of metadata-aware retrieval (prefix/suffix/unified/late-fusion) | vector | metadata | metadata-filtered | no | none | no | unknown | paper | med |
| **CoALA** | Conceptual blueprint: working/episodic/semantic/procedural memory taxonomy | epi+sem+proc+working | manual (typed) | n/a | no | none | desc | n/a | Princeton, widely cited | high |
| **"From Storage to Experience"** (survey) | Formalizes Storage→Reflection→Experience stages, ~40 systems | epi+sem+proc | metadata | hybrid | no | reflection | desc | n/a | 2026 survey | high |
| **"Memory for Autonomous LLM Agents"** (Du survey) | write-manage-read loop; 3-D taxonomy, 5 mechanism families | epi+sem+proc | metadata | hybrid | no | reflection | desc | n/a | survey | high |
| **"Memory in LLM-MAS"** (survey) | First systematic multi-agent-memory survey; local/blackboard/hybrid, access-control, transactive "who-knows-what" | tiered/shared | metadata | hybrid | no | reflection/consolidation | desc | n/a | TechRxiv'25 | high |
| **"Rethinking Memory Mechanisms…2nd Half"** | 3-axis (substrate/cognitive/subject), decay & cross-task generalization | epi+sem+proc | self-org | hybrid | no | consolidation | desc | n/a | arXiv 2602.06052 | high |

### 2.2 Self-organizing memory (the auto-label core)

| System | What it is | Mem | Lbl | Retr | MCP | Learn | S/P | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **A-MEM** | Zettelkasten notes: auto-generates keywords/tags + links, then **evolves** old notes as new ones arrive — the canonical anti-tag-soup mechanism | graph (self-linked notes) | **self-organizing** | hybrid (vec+links) | unknown | consolidation | no | yes | paper, active repo | high |
| **Letta / MemGPT** | OS-style tiered memory (RAM/disk); agent self-edits core+archival via tools | tiered+epi | self-org/manual | hybrid | yes | self-edit (no consolidation learning) | desc (memory-edit tools) | yes | ~21K★, $10M, peer-reviewed; 93.4% LongMemEval claimed | high |
| **Nemori** | Cognitive-science-inspired; blends episodic+semantic without manual tags | epi+sem+proc | self-organizing | hybrid | unknown | consolidation | unknown | unknown | paper | high |
| **Generative Agents** | Memory stream scored recency/importance/relevance + periodic reflection synthesis | epi+sem | auto (timestamps+importance) | hybrid | no | reflection | no | yes | landmark | high/med |
| **SEDM** | Scalable self-evolving distributed memory; agents restructure + share | unknown | self-organizing | unknown | unknown | consolidation | unknown | unknown | paper | high |
| **G-Memory** | Hierarchical graph (insight/query/interaction tiers) for multi-agent reuse | hierarchical graph | auto | graph | unknown | consolidation | desc | yes | paper | high |
| **Kumiho** | Graph-native cognitive memory; immutable revisions, typed reasoning edges, auditability | graph | metadata | graph | unknown | consolidation | desc | unknown | emerging | med |
| **yantrikdb-hermes-plugin** | Self-maintaining: `think()` canonicalizes, `conflicts()` surfaces contradictions, `recall()` returns `why_retrieved` | semantic | self-organizing | metadata-filtered | unknown | consolidation | no | yes | beta | high |
| **Editable Memory Graphs** | Personalized RAG over editable graph; insert/update/traverse per-user | graph | self-org | graph | no | consolidation | no | unknown | EMNLP'24 | high |
| **SwiftMem** | Sub-linear retrieval via temporal+semantic **DAG-Tag** indexing with embedding-tag **co-consolidation** | tiered | auto | hybrid | no | consolidation | no | unknown | paper | high |
| **Grounding Memory in Contextual Intent** | Indexes steps with structured intent cues; retrieves by intent compatibility | epi+sem+proc | metadata | metadata-filtered | no | none | no | unknown | paper | high |
| **Learning How to Remember** | Memory abstraction as learnable skill; "memory copilot" trained via DPO | epi+sem+proc | self-org | unknown | no | reflection | no | unknown | paper | high |
| **AtomMem** | Atomic CRUD memory ops; learned management policy via SFT+RL | epi+sem+proc | self-org | unknown | no | reflection | no | unknown | paper | high |
| **Membox** | Topic Loom groups same-topic turns into memory boxes on a long-range timeline | tiered | auto | metadata-filtered | no | consolidation | no | unknown | paper | med |
| **Amory** | Builds episodic narratives, consolidates with momentum, semanticizes offline | epi+sem+proc | auto | unknown | no | consolidation | no | unknown | paper | med |
| **SimpleMem** | Lossless compression + online semantic synthesis + intent-aware retrieval | tiered | auto | metadata-filtered | no | consolidation | no | unknown | paper | med |
| **Beyond Dialogue Time** | Organizes by actual occurrence time; durative-memory consolidation | epi+sem+proc | metadata | metadata-filtered | no | consolidation | no | unknown | paper | med |
| **FadeMem** | Dual-layer with adaptive exponential decay, conflict resolution, fusion | tiered | auto | unknown | no | consolidation | no | unknown | paper | med |
| **SAGE** | Self-evolving MAS; reflective self-correction + Ebbinghaus forgetting | tiered | auto | vector | no | reflection+consolidation | desc | unknown | arXiv'24 | high |
| **Mnemosyne** | Local-first SQLite+sqlite-vec hybrid (50/30/20), BEAM tiers, TripleStore graph | tiered | metadata | hybrid | unknown | consolidation | no | yes | beta | med |

### 2.3 Memory-as-MCP servers (the surface you'd expose)

| System | What it is | Mem | Lbl | Retr | MCP | Learn | S/P | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **mcp-memory-service** (doobidoo) | Semantic memory backend; MCP + 76-endpoint REST + dashboard, tag browse, quality scoring | vector | metadata | hybrid | yes | consolidation | no | yes | v11.3.1, 1,547+ tests, Apache-2.0 | high |
| **Basic Memory** (basicmachines) | MCP-native KM; Markdown files → knowledge graph (entities/observations/wikilinks) | graph | metadata (frontmatter+tags) | hybrid | yes | none | desc (skills dir, plugin) | yes | ~3.3K★, AGPL-3.0 | high |
| **Memorizer** (petabridge) | .NET + Postgres/pgvector; store/search/version/relate, native MCP | vector+rel | manual/metadata (workspaces>projects) | hybrid | yes | none (versioning, no consolidation) | desc (recommended system-prompt) | yes | 173★, v2.1.0, company | high |
| **Memory Store** (memory.store) | MCP-native, runs inside Claude/ChatGPT/Cursor/Slack; checkin/record/recall | epi+sem+proc | metadata | metadata-filtered | yes | none | no | no | new | high |
| **MemMachine** | Interoperable memory; pluggable backends, **configurable taxonomy**, MCP | vector | manual | hybrid | yes | none | no | yes | unknown | med |
| **Cipher / ByteRover CLI** | Version-controlled "context tree" synced + shared across 22+ coding agents | epi+sem+proc | self-organizing | hybrid | yes | consolidation | yes | yes | ~4.9K★, LoCoMo 96.1% claimed | high |
| **mcp-mem0** (coleam00) | Python MCP server wrapping Mem0; also a scaffold/template | vector | metadata | vector | yes | consolidation | no | yes | ~677★, MIT | high |
| **agent-memory-mcp** (adamrdrew) | MCP server, LanceDB + local ONNX embeddings; content/category/tags/timestamps | vector | metadata | hybrid (BM25+vec) | yes | none | no | yes | 4★ (tiny) | high |
| **Memento MCP** (gannonh) | MCP server, persistent KG memory, traverse relationships | graph | unknown | graph | yes | unknown | unknown | yes | reference impl | med |
| **mcp-neo4j-agent-memory** | Neo4j-backed; **flexible manual labels (no enforced schema)** — LLM picks any lowercase label | graph | manual (unconstrained) | hybrid | yes | none | no | yes | ~69★, early | high |
| **MemoryGraph** | Graph-DB MCP for coding agents; typed nodes/edges, bi-temporal, 8 backends, "Memory Protocol" via CLAUDE.md | graph | manual/metadata (typed) | graph | yes | workflow-induction (prompted) | desc (CLAUDE.md template) | yes | ~210★, v0.12.4, 1,200+ tests | high |
| **mnemo-hermes** | pgvector over Hermes FTS5; 5 tools (remember/recall/learn/predict…) | vector | metadata | hybrid | unknown | consolidation | no | yes | beta | med |

### 2.4 Reflection / experience-learning (the "learn-from-session" engine)

| System | What it is | Mem | Lbl | Retr | MCP | Learn | S/P | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **Reflexion** | Verbal RL: writes NL self-critique to episodic buffer; next attempt conditions on it | episodic | none/meta | none/filtered | no | reflection | no | yes | NeurIPS'23, foundational | high/med |
| **ExpeL** | Contrasts success/fail trajectories → reusable NL insights/rules registry | epi+sem+proc | auto | hybrid | no | reflection/workflow-induction | desc/yes | yes | paper | high |
| **ReasoningBank** (Google) | Distills strategies from **successes AND failures** into Title/Description/Content items, closed retrieve-extract-consolidate loop | sem+epi | auto (titled) | vector | no | reflection+consolidation | no | unknown | Google Research'26 | high |
| **Hindsight** (Vectorize) | Experience-acquisition engine; retain/recall/**reflect**; re-ranks by usefulness | tiered/episodic | auto/metadata | hybrid/vector | yes | reflection | unknown | yes | ~4K★, 94.6% LongMemEval claimed, peer-reviewed | high |
| **CLIN** | Continual learner; integrates corrected trajectories into causal-abstraction memory | epi+sem | auto | metadata-filtered | unknown | consolidation | no | unknown | paper | med |
| **Memento** | "Fine-tuning agents without fine-tuning"; stateful reflective episode memory | episodic | auto | hybrid | unknown | reflection | desc | unknown | paper | med |
| **Retroformer** | Policy-gradient over a retrospective model to improve future reflections | episodic | none | unknown | unknown | reflection | no | unknown | paper | med |
| **AutoGuide** | Generates context-aware guidelines offline; injects only state-relevant ones | semantic | metadata (state-keyed) | metadata-filtered | no | consolidation | yes | unknown | NeurIPS'24 | high |
| **MSI-Agent** | Multi-scale insights (task/scenario/action), selectively injected | semantic | metadata (scale) | metadata-filtered | no | consolidation | desc | unknown | EMNLP'24 | med |
| **Agent-Pro** | Policy-level reflection; updates belief + behavioral policy/prompt | episodic+belief | none | unknown | no | reflection | desc | unknown | ACL'24 | med |
| **MetaReflection** | Consolidates past reflections into semantic "lessons" augmenting future prompts | semantic | auto | vector | no | reflection+consolidation | yes | unknown | EMNLP'24 | high |
| **Self-Refine** | Single LLM generate→self-critique→refine loop, no training | none | none | n/a | no | none | no | yes | NeurIPS'23 | low |
| **hermes-dojo** | Monitors performance, identifies weak skills, iterates automatically | procedural | unknown | unknown | unknown | reflection | yes | yes | beta | high |
| **hermes-curator-evolver** | Evidence-driven companion to Hermes Curator; evidence-backed notes/reports | procedural | auto | unknown | unknown | reflection | yes | yes | beta | high |
| **SkillsBench** | Benchmark measuring skill performance on real workflows (eval signal) | none | none | n/a | no | skill-induction | desc | yes | activity | med |
| **Learn-by-interact** | Synthesizes interaction trajectories from docs, "backward construction" to instructions | epi+sem+proc | unknown | hybrid | no | workflow-induction | no | unknown | ICML'25 | med |
| **Titans** | Neural long-term memory MLP updated at inference via surprise/gradient | parametric | none | unknown | no | reflection | no | unknown | Google'24 | low |

### 2.5 Workflow / procedural memory (episodes → reusable procedures)

| System | What it is | Mem | Lbl | Retr | MCP | Learn | S/P | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **Agent Workflow Memory (AWM)** | Extracts reusable workflows from successful trajectories; retrieves as templates (~24% fewer web-nav errors) | epi+sem+proc (workflows) | auto | vector | no | workflow-induction | yes | yes | CMU'24 | high |
| **Memp** | Distills updatable procedural "scripts" from trajectories; build/retrieve/update | procedural+epi+sem | auto | hybrid/vector | no | workflow-induction+reflection | desc | yes | ZJUNLP, repo open | high |
| **AutoManual** | Distills exploration into a structured instruction **manual** of categorized rules | procedural (rules) | auto | unknown | no | workflow-induction | desc | unknown | NeurIPS'24 | high |
| **LEGOMem** | Modular procedural memory for MAS; reusable workflow procedures allocated across agents | procedural | metadata | metadata-filtered | unknown | workflow-induction | yes | unknown | paper | high |
| **ProcMEM / Skill-Pro** | Saves step-by-step procedural skills, reuses without retraining (non-parametric PPO) | epi+sem+proc | self-organizing | vector | no | skill-induction | desc/yes | unknown | paper | high |
| **Synapse** | Trajectory-as-exemplar prompting; stores abstracted trajectories as few-shot | procedural | metadata | hybrid | unknown | workflow-induction | desc | unknown | paper | med |
| **Corpus2Skill** | Compiles a corpus into a hierarchical **tree of skills** navigated at query time | epi+sem+proc | auto | graph | no | skill-induction | desc | unknown | paper | high |
| **hermes-motif** | Mines execution traces for repeated sub-sequences → micro-skills (n-gram + arg abstraction + dedup) | procedural | auto | unknown | unknown | workflow-induction | yes | yes | beta | high |
| **LangGraph** | Orchestration framework with namespaced memory primitives + workflow induction | tiered | manual | metadata-filtered | unknown | workflow-induction | desc | yes | established | high |
| **LangChain Deep Agents** | Skills-oriented harness (planning, sub-agents, FS memory) for long-horizon tasks | epi+sem+proc | manual | unknown | unknown | workflow-induction | yes | yes | activity | high |
| **OpenAI Agents SDK Sandbox Memory** | Distills prior-run info into durable workspace memory | tiered | manual | metadata-filtered | no | reflection | desc | no | product direction | med |
| **Planning with Files** | Persistent Markdown state files (task_plan.md) as episodic memory | episodic | manual | metadata-filtered | no | none | no | yes | activity | med |
| **BabyAGI / functionz** | Task loop (archived) → DB-backed function/skill mgmt for self-building agent | vector | auto | vector | no | skill-induction | yes | yes | ~22K★, experimental | med |
| **AFTER Benchmark** | 382 tasks/6 roles/22 skills measuring procedural-skill transfer across roles/models via versioned SKILL.md | epi+sem+proc | metadata | metadata-filtered | unknown | reflection | yes | unknown | paper | high |
| **voltagent-best-practices** | Reference skill documenting VoltAgent memory/workflow patterns | unknown | none | unknown | no | none | desc | yes | skill | low |

### 2.6 Skill management & induction (the "skills/tools" half of HR)

| System | What it is | Mem | Lbl | Retr | MCP | Learn | S/P | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **Voyager** | Minecraft agent; stores only **verified** executable code skills, NL-indexed, composes them | procedural (code) | auto/self-org | vector | no | skill-induction (verify-before-store) | yes | yes | NVIDIA, landmark ~5K★ | high/med |
| **OS-Copilot / FRIDAY** | Self-improving skill library (Python fns + docstrings); generate→execute→store-on-success | procedural | auto (docstrings) | vector | no | skill-induction | yes | yes | paper, active | med |
| **JARVIS-1** | Open-world agent; visual/textual/procedural memory + growing skill registry | epi+sem+proc | metadata | hybrid | no | skill-induction | yes | unknown | paper | high |
| **MemOS** (MemTensor) | Memory OS + **skill store**; crystallizes recurring patterns into skills | tiered | auto | hybrid | yes | skill-induction | yes | yes | active | high |
| **PowerMem** (OceanBase) | Vector/graph memory + Ebbinghaus decay + experience/skill tagging; distills skills | tiered | metadata | hybrid | yes | skill-induction | yes | yes | company | high |
| **Suyi (溯忆)** | Dual-temporal SQLite, decay, fact/skill tagging, crystallizes patterns | tiered | metadata | metadata-filtered | yes | skill-induction | yes | yes | unknown | med |
| **Agent Knowledge Cycle** | Six-phase spec turning experience into skills/rules via ADR + JSON schemas | epi+sem+proc | manual | metadata-filtered | yes | skill-induction | yes | yes | spec | high |
| **AutoSkill** | Lifelong learning; autonomously discover/refine/evolve skills | skill-indexed epi | auto | vector | unknown | skill-induction+consolidation | yes | unknown | paper | high |
| **Alita** | Self-evolving generalist; constructs + reuses its own **MCP modules/tools** | procedural | auto | unknown | yes (generates MCP) | skill-induction | yes | yes | paper | high |
| **CRAFT** | Builds specialized toolsets offline, retrieves relevant tools at inference | tool-indexed proc | auto | vector/filtered | no | skill-induction | yes | yes | paper | med |
| **Toolformer** | Self-supervised learning of when/which API to call | procedural | none | unknown | no | skill-induction | yes | yes | foundational | med |
| **AgentOptimizer** | Treats tools/functions as **learnable weights**: add/revise/remove fns by measured perf | procedural | auto | unknown | no | skill-induction | yes | unknown | ICML'24 | high |
| **Upskill** (HF) | Auto-generate + evaluate agent skills (generate→evaluate loop) | none | unknown | unknown | unknown | skill-induction | yes | yes | activity | high |
| **OpenSkills** | Universal SKILL.md loader integrating skills across multiple runtimes | none | metadata | metadata-filtered | unknown | none | yes | yes | activity | high |
| **eagle-eye** | 5-layer skill router: hard triggers → FTS5 BM25 → synonyms → embeddings → RRF fusion (50→top-5) | none | metadata | hybrid | unknown | none | yes | yes | beta | high |
| **SkillClaw** | Auto-evolves skills from session data post-task; dedup + improve library | procedural | unknown | unknown | unknown | skill-induction | yes | yes | 705★ | high |
| **hermes-skill-factory** | Meta-skill: auto-generates reusable skills from repeated workflows | none | unknown | unknown | unknown | skill-induction | yes | yes | beta | high |
| **hermes-skill-marketplace** | Agent that writes/tests/**publishes** new skills autonomously | none | unknown | unknown | unknown | skill-induction | yes | yes | experimental | med |
| **hermes-eval** | Skill **regression testing** + trajectory scoring to catch drift before propagation | procedural | unknown | unknown | unknown | reflection | yes | yes | beta | high |
| **SkillCheck** | Scanner for security/quality risks in skill packages before load | none | none | unknown | no | none | desc | yes | activity | med |
| **SkillNet** (paper 2603.04448) | Create/evaluate/connect skills; registry + composition graphs | network | auto | graph | unknown | skill-induction | yes | unknown | paper | high |
| **SkillNet** (openkg.cn) | 300K+ skill repo platform; dynamic ontology, relation graph, multi-dim eval | graph | self-organizing | hybrid | unknown | consolidation | yes | unknown | platform | high |
| **SkillWeaver** | Web agents discover/create/refine **API-level** skills; community-shared library | procedural (API) | auto | filtered/vector | no | skill-induction | yes | yes | OSU'25 | high |
| **Manus** | Autonomous platform with agent skills for task automation | unknown | manual | unknown | unknown | unknown | yes | no | company | med |
| **Goose** (Block) | Extensible agent; capabilities via extensions/skills incl. MCP | unknown | manual | unknown | yes | none | yes | yes | company | high |
| **Anthropic Skills** | Curated SKILL.md library; progressive-disclosure reference format | procedural | manual | metadata-filtered | no | none | yes | yes | company | high |
| **skill-creator** (Anthropic/Microsoft/Apollo) | Meta-skills that scaffold standardized SKILL.md packages | procedural | none | n/a | no | skill-induction | yes | yes | official | high/med |
| **mcp-builder** (Anthropic/Microsoft) | Meta-skills guiding MCP-server creation | none | none | unknown | no | none | yes | yes | official | med |
| **AgentSkills.io** | Open SKILL.md **standard** hub (format, dirs, naming, eval-driven design) | procedural | metadata | unknown | unknown | none | desc | unknown | standard | high |
| **Claude Code plugin framework** | Authoring skills/commands/agents/hooks/MCP via plugin.json + YAML frontmatter | procedural | metadata | metadata-filtered | yes | none | yes | yes | company | high |
| **Skill Seekers** | Converts doc websites → SKILL.md packages | procedural | metadata | n/a | no | none | yes | yes | activity | med |
| **obra/superpowers** | Curated process/procedural skills (TDD, RCA, git-worktrees…) | procedural | metadata | metadata-filtered | no | reflection | yes | yes | activity | high |
| **context-engineering-kit** (NeoLab) | Prompt-engineering + architecture + subagent skills | procedural | metadata | metadata-filtered | no | none | yes | yes | activity | high |
| **Mind-Cloning-Engineering** | "Mind cloning" reusable skill/knowledge artifacts | semantic | manual | unknown | no | skill-induction | yes | yes | activity | med |
| **Agent-Skills-for-Context-Engineering** | Anthropic-origin context-engineering skills collection | none | manual | metadata-filtered | no | none | yes | yes | activity | med |
| **hermeshub / skilldock.io** | Skill registries to browse/share/install community skills | none | manual | metadata-filtered | unknown | none | yes | yes | beta/prod | med |
| **LangChain Multi-Agent Skills** | Framework feature to implement/dispatch skills across a MAS | unknown | manual | unknown | unknown | none | yes | yes | company | high |
| **Agent Skills for LLMs** (survey) | Taxonomy: define/store/retrieve/compose/acquire/secure skills | epi+sem+proc | unknown | unknown | no | skill-induction | desc | unknown | survey | high |

### 2.7 Shared knowledge bases / multi-agent memory

| System | What it is | Mem | Lbl | Retr | MCP | Learn | S/P | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **Graphlit** | Context platform; ingests 15+ sources, extracts entities/facts, **provenance + permissions** | graph | auto | hybrid | yes | consolidation | desc | no | company | high |
| **Agent KB** | Cross-domain experience KB; reason-retrieve-refine pipeline | semantic | metadata | hybrid | unknown | consolidation | desc | unknown | paper | high |
| **Collaborative Memory** | Multi-user/agent shared memory with **bipartite asymmetric access control + full provenance** | tiered (private+shared) | metadata (perms) | metadata-filtered | unknown | consolidation | no/desc | unknown | NeurIPS'25 | high |
| **DAMCS** | Decentralized MAS cooperation; hierarchical KG, dynamic team formation, private memories | graph+private | metadata | graph | no | reflection | desc | unknown | paper'25 | high |
| **IoA** | Internet-of-Agents protocol; discover/team/coordinate, semantic "shared context" | shared/blackboard | metadata | unknown | no | none | desc | yes | paper+code | high |
| **MS / MemorySharing** | Real-time shared Prompt-Answer pool; autonomous retriever | vector (PA pairs) | none (flat) | vector | no | consolidation | desc | unknown | paper | med |
| **Learning to Share** | Shared memory bank + learned controller deciding what to pass between teams | tiered | self-organizing | unknown | no | reflection | no | unknown | paper | high |
| **MetaGPT** | SW-company MAS; standardized docs (PRD/spec/code) tagged by type/version + code-module registry | epi+sem+proc | metadata | metadata-filtered | no | consolidation | yes | yes | popular OSS | high |
| **ChatDev** | Role-playing dev agents; phase-tracked shared memory + ADR/code-pattern library | epi+sem+proc | metadata | metadata-filtered | no | consolidation | yes | yes | OSS | med |
| **consciousness-server** | Shared memory server for local agents; note/task tagging + semantic+structured retrieval | vector | manual | hybrid | yes | consolidation | no | yes | unknown | med |
| **CommonGround Kernel** | Postgres-backed shared work-record; causal lineage tagging + recovery for handoffs | epi+sem+proc | metadata | metadata-filtered | no | consolidation | no | yes | unknown | med |
| **plur** | Shared memory with open engram format (YAML) | epi+sem+proc | metadata | unknown | unknown | none | no | yes | beta | med |
| **Tapestry** | Skill-set interlinking/summarizing docs into a knowledge network | graph | auto | graph | no | reflection | no | yes | activity | med |
| **ClawHub** | 40K+ skill ecosystem platform (internals undisclosed) | unknown | unknown | unknown | unknown | unknown | desc | unknown | platform | med |

### 2.8 Capability registry / manager-as-HR (the thin frontier)

| System | What it is | Mem | Lbl | Retr | MCP | Learn | S/P | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **MemClaw** (Caura) | Governed, **permission-tagged** shared memory graph for agent fleets; access-controlled retrieval — closest to HR governance | graph | metadata | metadata-filtered | yes | consolidation | no | yes | unknown | high |
| **AgentStore** | Registry of specialist agents-as-skills + learned **meta-controller** selecting/composing them; dynamic registration | none (registry) | metadata | metadata-filtered | no | none (controller) | yes | unknown | paper | high |
| **IntentKit** | Intent-driven; routes user intents to a given agent's provisioned skills | unknown | metadata | metadata-filtered | unknown | none | yes | yes | activity | high |
| **mission-control** | Fleet dashboard: orchestration, task dispatch, cost tracking over Hermes agents | unknown | unknown | unknown | unknown | none | desc | yes | 3.7K★ | high |
| **NemoHermes** | NVIDIA capability registry + Spark-aware routing for Hermes | none | metadata | metadata-filtered | unknown | none | desc | yes | experimental | med |
| **Composio** | Connects agents to 1000+ apps with managed auth; managed capability/tool registry | none | metadata | unknown | unknown | none | yes | unknown | widely used | med |
| **SkillNet** (capability-registry variant) | Create/evaluate/connect skills; composition graphs | network | auto | graph | unknown | skill-induction | yes | unknown | paper | high |
| **claude-code-templates** (davila7) | Large CLI catalog of installable skills/agents/components pulled on demand | procedural | metadata | metadata-filtered | no | none | yes | yes | activity | high |
| **wshobson/agents** | Plugin/skill marketplace organized into domain plugins for fleet reuse | procedural | metadata | metadata-filtered | no | none | yes | yes | activity | high |
| **claude-skills-marketplace** (mhattingpete) | Plugin-grouped skill marketplace | procedural | metadata | metadata-filtered | no | none | yes | yes | activity | med |
| **ComposioHQ awesome-claude-skills** | Curated catalog of ready-made Claude skills | procedural | metadata | metadata-filtered | no | none | yes | yes | activity | med |
| **VoltAgent / awesome-agent-skills** | 1000+ skills/agents/MCP discovery registry (the list itself) | none | manual | unknown | no | none | yes | yes | community list | high |
| **agentskill.sh** | 44K+ skill directory with security scanning + curated discovery | none | metadata | metadata-filtered | unknown | none | desc | no | activity | med |
| **skills.sh** | Skill directory + leaderboard (ranked discovery) | none | metadata | metadata-filtered | no | none | desc | no | activity | med |

### 2.9 Prompt management / optimization

| System | What it is | Mem | Lbl | Retr | MCP | Learn | S/P | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **GEPA** | Evolves prompts via NL **reflection on traces** + Pareto selection; beats RL on some tasks | none | none | unknown | no | reflection | yes | yes | paper, in DSPy | high |
| **DSPy** | Program-not-prompt; declare typed pipelines, optimizers compile/tune prompts vs metric | none | none | unknown | no | none | yes | yes | ~35K★, Stanford | med |
| **ACE (Agentic Context Engineering)** | Treats context/playbook as evolving artifact; **incremental delta updates** avoid context collapse | semantic playbook | metadata (bullets) | metadata-filtered | no | reflection+consolidation | yes | unknown | paper | high |
| **OPRO** | LLM-as-optimizer iteratively proposes prompts scored by metric | none (opt trajectory) | none | none | no | none | yes | yes | ICLR'24 | high |
| **PromptBreeder** (DeepMind) | Self-referential evolutionary prompt mutation (incl. mutation-prompts) | none | none | unknown | no | reflection | yes | unknown | paper | med |
| **TextGrad** | "Autodiff via text"; backprops NL feedback to optimize prompts/code | none | none | unknown | no | none | yes | yes | Nature'25 | low |
| **ADAS** | Meta-agent proposes/evaluates/refines prompts+tool configs+control flows (2nd-order) | none (design archive) | auto | unknown | no | consolidation | yes | yes | paper | high |
| **Sculptor** | Active context management; priority-ranked context layers curated separately | tiered context | metadata | metadata-filtered | unknown | reflection | desc | unknown | paper | med |
| **Anthropic Context Management** | Active context editing + file-based memory tools | tiered | manual | metadata-filtered | unknown | reflection | desc | no | product direction | high |
| **super-hermes** | Meta-reasoning layer: agent writes better analytical prompts for itself | none | unknown | unknown | unknown | reflection | yes | yes | experimental | med |
| **hermes-agent-self-evolution** (Nous) | Evolutionary self-improvement using DSPy + GEPA on Hermes's own prompts | none | unknown | unknown | unknown | reflection | yes | yes | research | med |

### 2.10 Hermes ecosystem core + Other

| System | What it is | Mem | Lbl | Retr | MCP | Learn | S/P | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **Hermes Agent** (Nous) | Self-improving agent; FTS5 memory + autonomous `hermes curator` grading/consolidating/pruning skill library on cron | epi+sem+proc | self-organizing | hybrid | yes | skill-induction | yes (7-day Curator) | yes | core project | high |
| **AutoGPT** | Low-code continuous-agent platform; modular blocks + marketplace | unknown | unknown | unknown | unknown | none | desc | yes | ~185K★ | low |
| **AutoGen** (MS) | Multi-agent conversation framework; shared state/memory substrate | none | none | unknown | unknown | none | desc | yes | large OSS | low |
| **ReAct** | Reasoning+acting interleave; working-memory baseline, not persistent | none | none | none | no | none | no | yes | foundational | low |
| **MemBench / MemoryAgentBench / MemoryArena** | Memory **evaluation benchmarks** (factual vs reflective; cognitive competencies; multi-session action coupling) | n/a | n/a | n/a | no | n/a | no | unknown | benchmarks | low (but useful) |

---

## 3. Buy vs build, need by need

| Our need | Recommendation | One-line justification |
|---|---|---|
| **1. Labeled shared memory store (MCP search+label+query)** | **Adopt** mcp-memory-service or Mem0; for temporal/provenance use **Zep/Graphiti** | Hybrid tag+vector retrieval over MCP with dashboards is a solved, self-hostable commodity; do not rebuild the substrate. |
| **2. Self-organizing / auto-label taxonomy without tag-soup** | **Build** (steal A-MEM + SwiftMem); **govern** with a manager-curated controlled vocabulary | Every self-org system grows labels bottom-up with no curator — the exact failure you fear; the governed/curated layer is yours to build. |
| **3. Memory-as-MCP** | **Adopt** one MCP server (mcp-memory-service / Basic Memory / Memorizer) | ~12 mature MCP memory servers exist; reusing one is days, building one is weeks. |
| **4. Learning-from-sessions (summarize→label→store)** | **Build the policy, steal patterns** from AWM / Memp / ReasoningBank / ExpeL; storage from #1 | Consolidation primitives exist but the manager's session→procedure abstraction loop with your taxonomy is bespoke. |
| **5. Skill/tool management per worker** | **Adopt** SKILL.md standard + **OpenSkills** loader + **eagle-eye** routing; **build** the role→skill assignment; **steal** Voyager validation | Authoring/loading/routing/scanning are commoditized; "manager provisions skills per worker by role and learned fit" is not shipped anywhere. |
| **6. Dynamic prompt composition on the fly** | **Build**; steal **ACE** delta-updates + **AutoGuide** context-keyed injection; gate with **GEPA/DSPy** offline | Optimizers tune prompts offline against a metric; runtime per-worker assembly from labeled memory as an HR decision does not exist as a product. |
| **7. Agent capability registry / HR layer** | **Build**; closest patterns **AgentStore** (registry+meta-controller), **MemClaw** (governed fleet memory), **Collaborative Memory** (access-control+provenance) | The integrated registry + memory governance + prompt composition over a pluggable runtime is the genuine white space. |

---

## 4. Patterns worth stealing

**Auto-generating labels/links without tag-soup (A-MEM, SwiftMem, Nemori, SEDM).**
A-MEM is the reference design. On each write it does NOT emit free-form tags. It generates a *structured note* — keywords, a contextual description, and tags — then computes nearest-neighbor links to existing notes and triggers **"memory evolution"**: the new note's neighborhood causes old notes' descriptions/links to be *updated*, so the network reorganizes rather than accreting orphan tags. SwiftMem adds the operational fix you need at scale: **embedding-tag co-consolidation** over a temporal/semantic DAG, so semantically equivalent labels ("postgres-error" vs "pg-failure") collapse instead of multiplying. The governance lesson for us: **generate labels conditioned on the existing taxonomy neighborhood, not from a blank prompt**, and run periodic label-merge passes. Pair this with a *curated controlled vocabulary* the manager owns (none of these systems have a human-curated taxonomy — that is your differentiator).

**Consolidating episodes into reusable procedures (Reflexion → ExpeL → AWM/Memp → ReasoningBank).**
There is a clean maturity ladder: Reflexion stores a verbal self-critique per attempt (episodic, no abstraction). ExpeL *contrasts* success vs failure trajectories to extract NL "rules of thumb." Agent Workflow Memory and Memp go further — they induce *structured workflow templates/scripts* from successful trajectories and retrieve them as plan scaffolds (AWM reports ~24% fewer web-nav errors). ReasoningBank's key insight: **distill from failures too**, and store as *titled* items (Title/Description/Content) so retrieval is precise and items are auditable. AutoManual frames the output as a categorized *rules manual*. Steal: your "learn-from-session" job should produce **titled, typed, deduplicated procedure items** (not raw summaries), labeled into the curated taxonomy, with both success and failure provenance.

**Provenance & confidence (Zep/Graphiti, Collaborative Memory, Graphlit, yantrikdb, Memorizer).**
Zep/Graphiti make time first-class: every fact has `valid_from`/`valid_until` and an `invalidated_by` pointer, so superseded facts are retired without deletion (point-in-time queries). Collaborative Memory and Graphlit carry **full provenance + permissions** with bipartite access control governing read/write per agent. yantrikdb's pattern is the most directly stealable for trust: `recall()` returns a **`why_retrieved` reason list**, and a `conflicts()` operation **surfaces contradictions** rather than silently overwriting. Memorizer/MemoryGraph add versioning + audit trails + bi-temporal markers. Steal: every memory item carries `{source_session, confidence, valid_window, supersedes, why}`, and retrieval explains itself.

**Validating self-authored skills before reuse (Voyager, OS-Copilot, SkillClaw, hermes-eval, AgentOptimizer).**
Voyager's discipline is the gold standard: a skill is added to the library **only after a self-verification module confirms it succeeded in-environment**. OS-Copilot/FRIDAY generate→execute→store-on-success. AgentOptimizer treats functions as learnable weights and **adds/revises/removes** them by *measured* performance. hermes-eval runs **skill regression tests + drift detection before propagation**; SkillCheck/agentskill.sh **security-scan** packages first. Steal the pipeline: induced skill → sandbox execution / eval gate → security scan → version + provenance → only then promote to the assignable registry. **Never auto-promote an unverified self-authored skill into a worker's toolset.**

---

## 5. Traps the prior art already hit

**Self-organizing memory → tag-soup and label explosion.** mcp-neo4j-agent-memory is the cautionary example: it lets the LLM pick *any* lowercase label with **no enforced schema** — convenient, but the inevitable result is hundreds of near-synonym labels. Mitigations in the catalog: A-MEM's link-evolution (reorganize, don't accrete), SwiftMem's embedding-tag co-consolidation (merge equivalents), MemMachine's *configurable taxonomy*, FadeMem/MemoryBank/PowerMem **Ebbinghaus decay** and SAGE forgetting (prune low-value labels), MemoryOS **garbage collection**. Our move: a **closed vocabulary the manager curates**, auto-label *into* it, plus a scheduled merge/decay pass — i.e., top-down taxonomy + bottom-up suggestion, not pure bottom-up.

**Prompt drift & self-rewriting blast radius.** ACE explicitly names **"context collapse"** — monolithic rewrites of an evolving playbook degrade it — and fixes it with *incremental delta updates* instead of full regeneration. GEPA uses **Pareto selection** to avoid regressions; DSPy/OPRO are **metric-gated** (a change ships only if it beats the baseline on an eval set); hermes-eval catches **skill drift before propagation**. The unanimous lesson: never let an agent overwrite its own system prompt in place. Our move: **versioned prompt fragments, delta edits, an eval/approval gate, and rollback** — the manager proposes, a gate disposes.

**Memory poisoning / provenance attacks — largely UNSOLVED.** No system in the catalog robustly defends against an adversary (or a confused worker) writing false memories that later steer the fleet. The partial mitigations are: provenance + confidence (Zep, Collaborative Memory, Graphlit), conflict surfacing (yantrikdb `conflicts()`), access control (MemClaw permission tags, Collaborative Memory bipartite graphs), and **skill supply-chain scanning** (SkillCheck, agentskill.sh, SkillsBench). Treat this as a known **open risk**: writes must carry attribution and confidence, cross-source corroboration should gate promotion of a memory from "claimed" to "trusted," and worker write privileges should be scoped.

**Summarization decay & unbounded growth.** Lossy summaries compound errors and quietly grow. Mitigations: HiMem **hierarchical multi-level summaries** (episode→session→theme) with consolidation, Memobase profile consolidation, MemoryBank/FadeMem/PowerMem/Suyi **forgetting curves**, SimpleMem **lossless structured compression**, MemoryOS GC. The defensive pattern: **retain raw events and keep summaries *re-derivable*** (so a bad summary can be regenerated), tier summaries by abstraction level, and decay/prune by access frequency + importance rather than letting everything persist forever.

**The verification problem (your hardest, and prior art barely helps).** Your deliverables are non-code (good labels, good summaries, good prompts) so there is no test-as-oracle. The closest evaluation harnesses — **AFTER** (procedural-skill transfer across roles/models), **SkillsBench** (skill performance on real workflows), **MemBench / MemoryAgentBench / MemoryArena** — are all benchmark suites for *code/task* outcomes; none validates "is this a good label/summary/prompt" intrinsically. Realistic mitigations to assemble yourself: **downstream-task proxy metrics** (did sessions retrieving memory item X succeed more often?), **LLM-judge rubrics with provenance** (per ACE/GEPA reflection), **regression sets of golden labelings**, and **human spot-audit of the curated taxonomy**. Accept that verification here is *statistical and proxy-based*, not pass/fail.

---

## 6. What still does NOT exist (the novel surface you must build)

1. **A governed, self-documenting taxonomy curator.** A-MEM/SwiftMem reorganize labels mechanically, but **nobody curates a controlled vocabulary AND keeps its human-readable documentation in sync** as the taxonomy evolves. The manager that (a) accepts bottom-up label suggestions, (b) reconciles them against a curated vocabulary, and (c) **regenerates the taxonomy's living documentation** on each change is genuinely new.

2. **An integrated manager-as-HR closing all three loops at once.** Pieces exist in isolation — memory store (Mem0/Zep), capability registry (AgentStore/IntentKit), prompt optimization (GEPA/ACE), fleet dispatch (mission-control). **No system unifies labeled-memory governance + per-worker skill/tool provisioning + on-the-fly prompt composition over a pluggable container runtime, exposed via one MCP.** That integration, in Go, is the product.

3. **Retrieval-to-prompt assembly as an HR decision at dispatch time.** ACE/AutoGuide inject *guidance* into a running agent; AgentStore *selects* a specialist. Neither **composes a fresh per-worker system prompt by querying labeled memory + the capability registry + role at session-launch**, then provisions the matching skills. This "compile the worker" step is unbuilt.

4. **A learned worker↔skill fit model with safe promotion.** AgentOptimizer adds/removes functions by performance and eagle-eye routes skills, but **no system learns "which worker profile should get which skills" from finished-session outcomes** with a verify-before-promote gate tied to a capability registry.

5. **A verification harness for non-code deliverables.** As above — the genuinely missing oracle. Building a proxy/judge/golden-set evaluation loop for *labels, summaries, and prompts* (not task success) is novel work the catalog does not provide.

**Net:** adopt the substrate (#1–3 of §3), steal the proven consolidation/auto-label/validation patterns (§4), import the prior art's hard-won mitigations (§5), and concentrate your original engineering on the five surfaces above — the governed taxonomy curator, the integrated HR loop, dispatch-time prompt compilation, learned skill-fit with safe promotion, and a proxy-based verification harness.
