<!--
Generated 2026-06-26 by an exhaustive discovery workflow (self-improving-org-landscape):
15 parallel search angles -> fetch + per-system extraction (lists exploded) ->
completeness-critic round -> synthesis. 248 distinct systems.
Companion docs: ARCHITECTURE.md (§6A), AGENTS_MEMORY_HR_LANDSCAPE.md,
AGENTS_ORCHESTRATION_LANDSCAPE.md, AGENTS_STACK_DECISION.md, AGENTS_RESEARCH.md.
NOTE: benchmark %s, star counts, "SOTA"/"production" claims are vendor/author-reported, NOT verified.
Treat as a map to investigate, not gospel — re-check a system's primary source before adopting.
-->

# Self-Improving, Event-Driven Agent Orchestration — Landscape & Design Guidance

Scope note: this is a build-decision document for a **Go-based, self-improving manager** for a general-purpose (non-code) agent org. The architecture under evaluation is: an **event bus**; a **CBR routing scope** (retrieve-reuse-revise-retain over a history of event→reaction→outcome); cheap **summary bots** on `session.completed`; and a **Consultant** meta-agent that mines history and proposes process changes through an eval/approval gate. DAGs are emergent. There is no clean outcome oracle. I have been deliberately skeptical of vendor blogs and of papers that only demo on code/benchmarks.

---

## 1. Executive read

The honest summary is that **every individual pillar exists in mature or near-mature form, but nobody has assembled them in our configuration, and almost nothing targets general (non-code) orgs.** For the **event bus / event-sourcing** substrate, you should **adopt, not build**: Temporal (Go-native, event-sourced, durable replay) or a Kafka/Pulsar log are production-grade, and agent-specific framings (AutoGen v0.4 actor runtime, AgentScope's unified bus) prove the pattern — single best candidate to **adopt: Temporal** (Go SDK, event-history replay), steal the agent-pub/sub framing from Confluent/StreamNative. For **CBR-style history-driven routing**, the pattern is well-formed but every implementation is research-grade and Python — **steal the pattern, build in Go**; the single best blueprint is **CASCADE** (deployment-time 4R cycle with a contextual-bandit retrieval policy and binary feedback, explicitly general-purpose), with **Memento** and **CBRkit** as secondary references. For **emergent/dynamic DAGs**, mature runtime-decomposition exists (open-multi-agent, Magentic-One) but our "emergent via subscription" stance is closest to the **StreamNative/Confluent** thesis (topology emerges from who subscribes) — **steal**, single best pattern-source: **StreamNative stream-native agents**. For **learned workflow selection**, there is heavy academic machinery (AFlow, FlowReasoner, MaAS, AgentSquare) but **all of it presumes a scoreable metric we do not have** — **build a degraded version, steal search structure from AFlow**, and treat the rest as a cautionary tale. For **self-modifying/self-evolving orchestration**, the archive-plus-empirical-gate pattern (ADAS, Darwin Gödel Machine) is the right shape for the Consultant, and **ACE** is the cleanest "evolve a playbook, not code" analog — **steal**, single best: **ADAS** (Meta Agent Search archive) tempered by **ACE** for safer scope. For **summary/reflection pipelines**, this is the most directly reusable pillar: **ReasoningBank** (distill labeled, reusable strategy items from *both* successes and failures) is essentially your summary-bot spec — **steal, single best: ReasoningBank**, with Reflexion/ExpeL/AutoGuide/CLIN as variants. For **organizational (non-code) process learning**, there is almost nothing productized; the relevant body is **process mining** (process discovery + enhancement over event logs) — **build, steal conceptually** from Agentic-AI Process Observability and AWO. Net: **adopt the bus, steal the rest, build the CBR-router + Consultant glue and the org-level learning loop — that glue is the genuinely novel surface.**

---

## 2. Full catalog by category

Legend: LW = learns-which-workflow · EV = event model · DAG = dynamic DAG · CBR = refs history/case base · SM = self-modifying · GP = general-purpose · SH = self-host · Rel = relevance. Values: y/p/n/u (yes/partial/no/unknown). Duplicate catalog entries (same system, multiple URLs/venues) are consolidated into one row.

### CBR for agents (case-base / retrieval-to-decide)

| System | What | LW | EV | DAG | CBR | SM | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| CBR-for-LLM-Agents survey (Hatalis et al.) | Formalizes 4R cycle c=(P,S,O,M) for agents | p | n | u | y | p | y | n | paper (2025) | high |
| Agent KB | Cross-domain reason-retrieve-refine experience base | p | n | n | y | n | y | y | paper+code (ICML'25) | high |
| ExpeL | Extracts NL insights + recalls past trajectories, no grads | p | n | n | y | n | y | u | paper (AAAI'24) | high |
| Memento | Continual learning as CBR over (state,action,outcome); retain writes back | y | n | p | y | y | y | y | paper+code (2025) | high |
| A-MEM | Zettelkasten agentic memory, evolving linked notes | n | n | n | y | p | y | y | paper+code (2025) | high |
| Memento-Skills (Let Agents Design Agents) | Agents compose new skills recursively | p | u | y | y | y | u | u | paper (2026) | high |
| Task-Aware LLM Council | Routes task to model by recorded success history | y | u | y | y | n | u | u | paper (2026) | high |
| Recommend MAS Subgraphs from Calling Trees | Recommends agent-team subgraph from past traces | y | u | y | y | n | u | u | paper (2026) | high |
| MAGMA | Multi-graph memory, policy-guided traversal | p | u | p | y | n | u | u | paper (2026) | med |
| MapCoder | Retrieval agent recalls prior problems (fixed pipeline) | n | n | n | y | n | code-only | u | paper | med |
| Agent4PLC | Retrieval of similar control-code examples | n | n | n | y | n | code-only | u | paper | med |
| Synapse | Trajectory-as-exemplar prompting w/ memory | p | n | n | y | n | y | y | paper (ICLR'24) | med |
| Agent-as-a-Router | Model routing as Context-Action-Feedback w/ memory | y | y | p | y | y | code-only | u | paper (2026) | high |
| DS-Agent | CBR over Kaggle expert solutions | p | n | n | y | n | code-only | y | paper (ICML'24) | med |
| CBR-RAG | CBR retrieval stage to augment RAG (legal QA) | n | n | n | y | n | no (legal) | u | paper (ICCBR'24) | med |
| **CBRkit** | Production Python CBR toolkit (full 4R, REST/CLI) | n | n | n | y | n | y | y | active OSS, ICCBR'24 | high |
| **CASCADE** | Deployment-time CBR 4R + contextual-bandit retrieval, binary feedback | p | n | n | y | p | y | y | paper+code (2026), DTLBench | high |
| AgentCF | Users/items as memory-agents (recommendation) | p | p2p | y | y | p | no (recsys) | u | paper | med |

### Strategy / experience / procedural memory

| System | What | LW | EV | DAG | CBR | SM | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **ReasoningBank (+MaTTS)** | Distills reusable strategies from success *and* failure; retrieves into context | p | n | n | y | p | y | u | paper (Google, 2025) | high |
| Agent Workflow Memory (AWM) | Induces named reusable workflows from trajectories | y | n | y | y | y | no (web) | y | paper+code | high |
| G-Memory | Hierarchical graph memory for multi-agent collab | p | n | n | y | n | y | u | paper (2025) | high |
| Buffer of Thoughts | Meta-buffer of reusable thought templates | p | n | n | y | p | y | y | paper (NeurIPS'24) | med |
| ArcMemo | Lifelong abstract reasoning-concept memory | p | n | n | y | n | y | u | paper (2025) | med |
| Memp | Procedural "how-to" skill memory build/retrieve/update | y | n | p | y | y | y | u | paper (2025) | high |
| Remember Me, Refine Me | Procedural memory retain+refine | y | n | p | y | y | y | u | paper (2025) | high |
| PRINCIPLES | Synthesized strategy memory for dialogue agents | y | n | n | y | p | no (dialogue) | u | paper (2025) | high |
| Dynamic Cheatsheet | Test-time adaptive memory of reusable snippets | p | n | n | y | y | y | u | paper (2025) | med |
| AutoGuide | State-aware guidelines from offline success/failure, retrieved by state | p | n | n | y | y | y | u | paper | high |
| StackPlanner | Hierarchical planner + RL-reused task-experience memory | y | u | y | y | p | u | u | paper (2026) | high |
| AtomMem | Learned atomic CRUD memory policy | p | u | n | y | y | u | u | paper (2026) | med |
| MetaClaw | "Talk to it, it learns/evolves" (undocumented) | p | u | u | y | y | y | y | repo | med |
| lettabot | Personal assistant on Letta durable memory | n | u | u | y | p | y | y | repo | med |
| rowboat | OSS coworker w/ memory | n | u | p | y | n | y | y | company repo | med |
| hermes-agent | "Grows with you" (undocumented) | u | u | u | p | u | y | y | Nous repo | low |
| Letta (MemGPT) | OS-like hierarchical paged memory via tool calls | n | n | n | y | y | y | y | company/OSS | med |
| Mem0 | Production long-term memory layer | n | n | n | y | p | y | y | company/OSS | med |
| ELITE | Experiential learning + intent-aware transfer | y | u | u | y | y | no (embodied) | u | paper (2026) | high |
| SkillZero | In-context agentic RL; composes learned behaviors | p | u | y | y | y | u | y | paper+code | high |
| SkillWeaver | Web agent codifies skills into APIs | p | n | n | y | y | no (web) | y | paper (2025) | med |
| Voyager | Minecraft skill library (executable code) + curriculum | p | n | y | y | y | no (game) | y | paper (NeurIPS'23) | high |
| OS-Copilot | Generalist desktop agent, experience accumulation | p | u | u | y | y | y | y | repo | med |
| Metis | Dual text+code memory; crystallizes plans into tools | p | u | y | y | y | y | u | paper (2026) | high |
| RaMem | Context-reinstatement long-term memory | n | n | n | y | n | y | u | paper (2026) | med |
| WISE-Flow | Structured experience for conversational service agents | y | u | n | y | p | y (service) | u | paper (2026) | high |
| CLIN | Frozen-LLM agent, persistent causal-abstraction memory across trials | p | n | n | y | p | y | y | paper (2023) | high |
| Think-in-Memory | Stores post-reasoning "thoughts" | n | n | n | y | n | y | u | paper (2023) | low |
| ChatDB | SQL DB as symbolic memory | n | n | n | y | n | y | y | paper (2023) | med |
| RET-LLM | Structured triplet memory | n | n | n | y | n | y | u | paper (2023) | low |
| Generative Agents (Park) | Memory stream + retrieval + reflection; emergent behavior | y | p2p | y | y | y | y (social sim) | y | paper, high-impact | high |
| Cradle | General Computer Control; episodic+procedural memory, skill curation | p | n | p | y | y | y | y | paper (2024) | med |
| Managing Procedural Memory (AFTER) | SKILL.md transfer + Reflector refinement; benchmark | y | u | u | y | y | y | u | paper+bench (2026) | high |

### Learned workflow routing

| System | What | LW | EV | DAG | CBR | SM | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| FlowReasoner | RL meta-agent generates per-query workflow | y | n | y | p | p | y | u | paper (2025) | high |
| EvoRoute | Experience-driven self-routing | y | u | y | y | p | u | u | paper (2026) | high |
| MaAS | Agentic supernet; samples query-conditioned sub-DAG | y | n | y | p | n | y | y | paper+code (ICML'25) | high |
| RCR-Router | Role-aware context routing under budget | p | n | p | y | n | y | u | paper (2025) | med |
| AMO (Agentic Meta-Orchestrator) | Learning-to-rank agent select+sequence | y | u | y | p | n | y | u | paper (2025) | high |
| AgentStore MetaAgent | AgentToken routing over agent pool | y | n | y | n | n | y | u | paper (2024) | high |
| CASTER | Lightweight router self-optimizing on negative feedback | y | u | y | p | y | u | u | paper (2026) | high |
| Decentralized LLM Collab (Actor-Critic) | RL collaboration policy | y | u | p | p | p | u | u | paper (2026) | med |
| Latency-Aware Orchestration | Learns scheduling of parallel paths | y | u | y | u | n | u | u | paper (2026) | med |
| Confidence-Aware Routing | Routes by confidence/complexity to roles+scales | y | u | y | u | n | u | u | paper (2026) | high |
| MAS-Orchestra | Orchestration as RL function-calling | y | u | y | p | p | u | u | paper (2026) | high |
| Dynamic Role Assignment (Debate) | Capability-matched roles per task | p | u | y | u | n | u | u | paper (2026) | med |
| Multi-Agent via Evolving Orchestration (Puppeteer) | RL-trained central orchestrator that evolves | y | central | y | p | y | y | y (ChatDev branch) | paper+code (2025) | high |
| DyLAN | Feed-forward agent net; prunes weak agents per task | y | layered | y | p | y | y | y | paper | high |
| SkillRouter | Routes among thousands of skills via learned policy | y | u | p | y | u | u | u | paper (2026) | high |
| Divergence-Point Preference Learning | ToolGraph + experience-weighted routing edges | y | n | y | y | y | y | u | paper (2026) | high |
| ReM-MoA | Reviewer agent routes across MoA layers | y | u | y | y | n | y | u | paper (2026) | high |
| Inngest AgentKit | Code/agent routers over shared KV state | p | state | y | n | n | y | y | company OSS | med |
| ProAgent (cooperative) | Infers teammate intent, adapts online | p | p2p | p | n | p | y | u | paper (2023) | high |
| Self-RAG | Learned retrieve-vs-generate gate via reflection tokens | p | n | y | n | n | y | y | paper (ICLR'24) | med |
| Graph of Skills | Dependency-graph skill retrieval/composition | p | u | y | y | u | u | u | paper (2026) | med |

### Self-modifying / self-evolving orchestration

| System | What | LW | EV | DAG | CBR | SM | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| **ADAS** | Meta Agent Search programs new agent systems; archive | y | n | y | y | y | y | y | paper+code (ICLR'25) | high |
| **Darwin Gödel Machine** | Self-rewrites own code; archive + empirical validation | y | n | y | y | y | no (SWE) | y | paper+code (2025) | high |
| GPTSwarm | Agents as optimizable graphs; tunes edges+prompts | y | n | y | p | y | y | y | paper+code (ICML'24) | med/high |
| Symbolic Learning (agents) | Language "gradients" optimize prompts/tools/pipeline | y | n | y | p | y | y | y | paper+code | med |
| Gödel Agent | Recursively reads/rewrites own logic at runtime | p | n | y | u | y | y | y | paper (ACL'25)+code | high |
| Alita | Generates/reuses own tools/MCP at runtime | p | n | y | p | y | y | y | paper (2025) | med |
| Alita-G | Generates/specializes other agents | p | n | y | y | y | y | u | paper (2025) | med |
| MemEvolve | Meta-evolution of memory mechanism | p | n | u | y | y | y | u | paper (2025) | med |
| **ACE (Agentic Context Engineering)** | Evolving playbook: generator/reflector/curator edit context from feedback | p | n | p | y | y | y | u/y | paper+code (2025) | high |
| AutoSpec | Evolves safety rules via inductive logic programming | n | n | n | y | y | y | u | paper (2026) | med |
| ResMAS | RL topology gen + topology-aware prompt opt | y | u | y | p | y | u | u | paper (2026) | med |
| CORAL | Multi-agent evolution via shared memory; heartbeat interventions | p | event | y | y | y | u | u | paper (2026) | high |
| SE-Agent | Self-evolves own solution trajectories | p | n | n | y | y | no (SWE) | u | paper (2025) | med |
| Automated Agent Design (Evolutionary) | Archive + context-aware sampling, mutate, eval on held-out | y | u | y | y | y | y | u | papers (2025) | high |
| loom | Autonomous loops evolve products | p | n | u | u | y | no (code) | y | repo | high |
| Metaswarm | Self-improving Claude-Code swarm; fixed 9-phase SDLC + post-merge reflect | p | n (BEADS DB) | n | y | p | no (code) | y | ~333★, v0.12 | high |
| OPRO | LLM optimizer proposes better prompts from (prompt,score) history | n | n | n | y | y | y | y | paper (ICLR'24) | med |
| Promptbreeder | Evolves task-prompts AND mutation-prompts | p | n | n | p | y | y | u | paper (DeepMind'23) | med |
| EvoMAC | Textual backprop rewrites agent net for SWE | p | n | y | y | y | no (code) | u | paper | high |
| TextGrad | Backprop NL feedback through pipeline to edit prompts/code | n | n | n | y | y | y | y | paper | high |
| EvoPrompt | EA evolves prompt population | n | n | n | y | y | y | u | paper (ICLR'24) | med |
| AgentVerse | Agents dynamically recruit into roles per stage | p | p2p | y | n | p | y | y | high ★ | high |
| Trace / OptoPrime | Trace graph + LLM optimizer tunes params/prompts/code (OPTO) | p | n | n | n | y | y | y | paper (NeurIPS'24) | med |
| MemGPT | Self-edits paged memory tiers via interrupts | n | event | n | y | y | y | y | paper+project | high |
| Self-Organized Agents (SoA) | Mother agents spawn children as complexity grows | n | u | y | n | y | no (code) | u | paper | med |

### Dynamic workflow generation

| System | What | LW | EV | DAG | CBR | SM | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| EvoAgentX | OSS framework: build/execute/evolve workflows (TextGrad/AFlow/MIPRO) | y | u | y | p | y | y | y | framework (EMNLP'25 demo) | high |
| **AFlow** | MCTS search over code-represented workflows | y | n | y | p | y | y/code | y | paper+code (ICLR'25) | high |
| ScoreFlow | Score-DPO preference optimization of workflows | y | n | y | p | y | y | y | paper+code (2025) | med |
| AutoAgents | Generates agent team + plan per task | p | n/p2p | y | n | p | y | y | paper (IJCAI'24)+code | med |
| G-Designer | VGAE generates per-query communication topology | y | n | y | n | p | y | u | paper (2025) | high/med |
| ProAgent (RPA) | JSON process graphs, test-driven repair | n | n | y | n | y | y (RPA) | u | paper (2023) | med |
| AgentSquare | Searches modular agent design space + perf predictor | y | n | y | y | y | y | y | paper+code (ICLR'25) | high |
| ToP (Think-on-Process) | Synthesizes SW process instances from knowledge | y | u | y | p | p | no (code) | u | paper | high |
| MegaAgent | Roles/tasks generated dynamically from requirements | p | u | y | u | y | no (code) | u | paper | high |
| TopoDIM | One-shot heterogeneous topology generation | p | pub/sub | y | u | p | u | u | paper (2026) | high |
| Do We Always Need Query-Level Workflows? | Task-level workflow prediction + few-shot calibration | y | u | y | p | p | u | u | paper (2026) | high |
| SP-Mind | Chains tools via skill templates (proteomics) | p | n | y | y | n | no (domain) | u | paper (2026) | med |
| LATS | Tree search over action/plan space per task | p | n | y | p | n | u | y | paper+code | med |
| GAP | DAG planning + parallel tools + RL planner | p | u | y | u | n | u | y | repo | med |
| BabyAGI / babyagi3 | Generates+reprioritizes task list in fixed chain | n/p | n | p | n | u | y | y | influential proto | med |
| skillfold | Config-language compiler for pipelines | n | n | n | n | n | y | y | early repo | low |
| loki-mode | Autonomous SDLC orchestrator | n | u | p | u | n | no (code) | y | repo | med |
| From Static Templates to Dynamic Runtime Graphs (survey) | Taxonomy: when/what/which of workflow optimization | y | n | y | p | y | y | n | survey (IBM/RPI, 2026) | high |
| Magentic-One | Orchestrator decomposes+replans; ledgers | n | seq handoffs | y | n | n | y | y | MSR (2024), on AutoGen | med |
| AutoGPT | Block-graph agents, low-code | n | u | n | n | n | y | y | ~185k★ | med |
| open-multi-agent | Coordinator decomposes goal into runtime task DAG | n | emit/sub | y | n | n | y (partial) | y | ~6.4k★ (2026) | high |
| OKR-Agent | Objectives/Key-Results decomposition | n | hier | p | n | n | no (creative) | u | paper | med |
| AgentCoord | Visual human-in-loop coordination design | n | flexible | p | n | n | y | u | paper | med |

### Reflection pipelines

| System | What | LW | EV | DAG | CBR | SM | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| GEPA | Reflective genetic prompt evolution | p | n | n | p | y | y | y | paper (ICLR'26), in DSPy | high |
| Reflexion | Verbal RL; reflect on failure → episodic memory → retry | p | n | n | y | p | y | y | paper (NeurIPS'23)+code | high |
| H2R | Hierarchical hindsight reflection | p | n | n | y | y | y | u | paper (2025) | med |
| SAGE | Reflective reasoning + memory augmentation | p | n | n | y | y | y | u | paper (2025) | med |
| MetaAgent (tool-learning) | Minimal workflow + continual evidence distillation | y | n | p | y | y | y | u | paper (2025) | high |
| EvolveR | Experience reflection + skill acquisition lifecycle | y | u | u | y | y | u | y | paper+code | high |
| Execute-Distill-Verify | Decouples exec/distill/validate across agent pool (anti-self-confirmation) | p | emit/sub | u | y | y | y | u | paper (2026) | high |
| Retroformer | Small retrospective LM trained by RL to write better reflections | n | n | n | p | y | y | y | paper (ICLR'24)+code | med |
| Self-Refine | Generate→self-critique→refine loop, no training | n | n | n | n | n | y | y | paper (NeurIPS'23) | med |
| MetaReflection | Offline mines reflections → improved instructions (semantic memory) | n | n | n | y | y | y | u | paper (2024) | high |

### Event-driven architecture / runtimes

| System | What | LW | EV | DAG | CBR | SM | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| claude-flow | Claude-Code swarm w/ hooks + shared memory | p | hooks | y | y | u | no (code) | y | popular OSS | high |
| swarm-protocol | Headless coordination as MCP server | n | msg-pass | u | u | n | y | y | early repo | med |
| gastown | Multi-agent orch + persistent work tracking | n | u | u | y | n | no (code) | y | active (Yegge) | med |
| scion | Parallel agent testbed (GCP) | n | u | p | n | n | code/testbed | y | GCP repo | low |
| Confluent EDA blog | Kafka swimlanes; orchestrator/hierarchical/blackboard/market patterns | n | bus/event-sourcing | n | n | n | y | y | vendor ref | high |
| LangGraph (+triggers) | Graph event-loops; conditional edges; TriggerRegistry | n | emit/sub | p | p | n | y | y | active OSS | high |
| Auxiliobits blog | Event bus coordination patterns | n | bus | n | n | n | y | u | vendor blog | high |
| StreamNative thesis | Stream-native agents; topology emerges from subscriptions; replay | n | bus (Pulsar) | y (emergent) | p | n | y | u | vendor thesis | high |
| Apache Pulsar | Durable pub/sub, replay/rewind, consumer groups | n | bus+log | n | p | n | y | y | mature Apache | high |
| Apache Kafka | Distributed log, offsets/replay, consumer groups | n | bus/log | n | p | n | y | y | mature | med |
| StreamNative Orca | Agent engine on Pulsar (preview) | u | bus | u | u | u | u | u | preview | med |
| Pulsar Functions | Agent = function over topics | n | emit/sub | n | n | n | y | y | mature feature | low |
| Atlan EDA article | Event bus as nervous system; O(N²)→O(N); saga/fan-out | n | bus/event-sourcing | p | p | n | y | u | vendor article | high |
| AWS EventBridge | Managed rule-based event bus | n | bus | n | n | n | y | n | managed | low |
| LlamaIndex Agent Workflows | Typed events + queues | n | emit/sub | y | p | n | y | y | active OSS | high |
| Spring AI (A2A) | Java A2A over event messaging | n | event/A2A | u | u | n | y | y | active OSS | low |
| Atlan Playbooks | Kafka MCL-driven governance agents | n | bus (Kafka) | n | y | n | no (gov) | n | product | med |
| AutoGen | Conversable agents; v0.4 actor/event-driven runtime | n | event-driven (v0.4) | p/y | n | n | y | y | very high ★ (MS) | high |
| AgentScope | Production MAS w/ unified event bus, permissions, sandboxes | n | bus | p | n | n | y | y | ~27k★ (Alibaba) | med |
| Dapr Agents | Durable workflows + pub/sub + virtual actors (CNCF) | n | bus/pub-sub | n | n | n | y | y | ~700★, early | med |
| **Temporal** | Durable execution; full event-history replay; signals | n | event-sourcing | n | n | n | y | y | mature, Go SDK | med |
| Restate | Single-binary durable execution; journaled replay | n | event-sourcing | n | n | n | y | y | active OSS | med |
| Beyond Rule-Based Workflows (A2A) | Info-flow orchestration via NL A2A, no predefined WF | p | event/A2A | y | u | p | u | u | paper (2026) | high |
| Orchestration of MAS (survey) | Unified MAS framework: MCP + A2A | n | bus/A2A | u | n | n | y (enterprise) | u | survey (2026) | med |
| CrewAI | Crews + event-driven Flows; LanceDB memory w/ recency/importance recall | n | emit/sub | p | y | n | y | y | ~54k★ MIT | med |
| Agent-Orchestration (Haaziq386) | Event-sourcing core; immutable log; replay/time-travel; visual DAGs | n | event-sourcing | n | n | n | y | y | ~2★, emerging | high |
| ATRIA | Clinical multi-agent w/ shared artifact store | n | emit/sub | n | p | n | no (clinical) | u | paper (2026) | med |
| SAFARI | Investigator agent probes traces via tools | p | emit/sub loop | y | p | n | y (debugging) | u | paper (2026) | med |

### Organizational learning / process mining (non-code-leaning)

| System | What | LW | EV | DAG | CBR | SM | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| MiroShark | Hundreds of grounded personas coordinate | u | swarm | y | u | u | y | y | active repo | med |
| paperclip | "Zero-human companies" org-level orchestration | u | u | u | u | u | y | u | early repo | med |
| PM × ABMS survey (Bemthuis & Lazarova-Molnar) | SLR: process mining (discover/conformance/enhance) + ABMS over event logs | p | event-sourcing | n | y | p | y | u | peer-rev SLR (2025) | high |
| Agent Miner | Discovers per-agent process model from event logs | y | event-log | y | y | n | y | u | paper | med |
| Agentic AI Process Observability | Process mining over agent execution traces; behavioral variability | p | event-sourcing | n | y | n | y | u | paper (2025) | high |
| PM face-validity validation | Outlier detection on sim logs for ABM validity | n | event-log | n | y | n | y | u | chapter (2023) | med |
| CRISP-DM PM methodology | Structured PM-for-ABMS assessment loop | n | event-log | n | y | p | y | u | paper (2025) | med |
| MonoScale | Safe agent-pool expansion w/ monotonic improvement | p | u | p | p | p | u | u | paper (2026) | med |
| Corpus2Skill | Enterprise knowledge → navigable skill trees | p | u | p | y | n | y (enterprise) | u | paper (2026) | med |
| **AWO (Agent Workflow Optimization)** | Mines traces → crystallizes recurring tool sequences into meta-tools | p | n | n | y | y | y | u | paper (2026) | high |
| MemClaw | Governed shared memory: scope/provenance/policy propagation | n | event/governance | n | y | n | y | u | paper (2026) | med |
| From Experience to Strategy | Trainable graph memory distilled into strategies | y/p | u | p | y | p | y | u | paper (2025) | high |
| Co-Learning (ChatDev) | Mines heuristics/shortcuts from past execution | p | u | n | y | y | no (code) | u | paper | high |
| MetaAgents (collaborative generative) | Role/task coordination via plan/reflect/memory | p | u | y | y | y | y | u | paper (2023) | med |
| Multi-Agent Collaboration Mechanisms (survey) | Taxonomy of coordination structures/strategies | n/a | survey | both | n/a | n/a | y | u | survey (2025) | high |
| MetaGPT | SOP waterfall, pub/sub shared message pool | n | pub/sub pool | n | n | n | no (code) | y | high ★ | med |
| ChatDev | SDLC waterfall chat-chain | n | chat-chain | n | n | n/y(ext) | no (code) | y | high ★ | med |

### Other / surveys / baselines / meta-learning / general frameworks

| System | What | LW | EV | DAG | CBR | SM | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|
| Awesome-Self-Evolving-Agents (EvoAgentX) | Best single prior-art index for a self-improving manager | n | – | – | – | – | y | y | active list | high |
| Awesome-Self-Evolving-Agents (XMUDeepLIT) | Mechanism taxonomy incl. topology evolution | n/a | – | – | – | – | y | y | active list | high |
| Agent-Memory-Paper-List | ~195 memory papers, factual/experiential/working | n | – | – | – | – | y | y | active list | high |
| From Storage to Experience (survey) | Storage→Reflection→Experience memory taxonomy | n/a | n | u | – | u | y | u | ACL'26 Findings | high |
| Du et al. survey (2503.12434) | Param-driven vs param-free agent optimization map | p | n | n | – | – | y | n/a | survey (2025) | high |
| Survey of Self-Evolving Agents (2507.21046) | What/when/how/where to evolve | p | n | u | – | – | y | n | survey | high |
| Comprehensive Survey Self-Evolving AI Agents (Fang) | Inputs/AgentSystem/Env/Optimisers framework | p | n | u | y | y | y | – | survey (2025) | high |
| Awesome-Memory-for-Agents (TsinghuaC3I) | Memory list by persistence/curation | n/a | n | – | – | – | y | – | list | high |
| self-improvement-llm repo | ~400-paper self-improvement survey (mostly weight-level) | n | n | n | n | n | n/a | n | survey+list | med |
| Rethinking Memory Mechanisms (2602.06052) | ~200-system memory survey; learning-policy axis | n/a | n | n/a | y | n/a | y | u | survey (2026) | high |
| Memory for Autonomous LLM Agents (Du, 2026) | write-manage-read loop; notes outcome-routing underexplored | p | n | n | y | y | y | u | survey (2026) | high |
| awesome-agent-orchestrators | ~100 orchestrators (mostly coding parallel runners) | n | n | n | n | n | no (catalog) | y | active list | med |
| Survey SE MAS (He, Treude, Lo) | SE-scoped MAS survey | n/a | n | n/a | n/a | n/a | no (code) | n/a | TOSEM'25 | high |
| Awesome-Agent-Papers (luo-junyu) | Construction/Collab/Evolution index | n/a | n | n | n | n | y | y | active list | high |
| Agent Forest | Sampling + majority vote ensemble | n | n | n | n | n | no (code) | u | paper | low |
| CodexGraph | Code-graph DB navigation | n | n | n | n | n | no (code) | u | paper | low |
| CAMEL | Two-role inception-prompted dialogue | n | role dialogue | n | n | n | y | y | high ★ | low |
| LLMARENA | Competitive gaming benchmark | n | competitive | n | n | n | no (game) | u | paper | low |
| MedAgent | Medical specialist agents | n | decentralized | n | n | n | no (med) | u | paper | low |
| MARG | Paper-review agents | n | decentralized | n | n | n | no (review) | u | paper | low |
| OpenAgents | 3 specialist agents platform | n | central | n | n | n | y | y | high ★ | low |
| LLM-Blender | Rank+fuse ensemble | n | central | n | n | n | y | y | paper | low |
| RoCo | Multi-robot dialogue roles | n | role dialogue | n | n | n | no (robotics) | u | paper | low |
| FedIT | Federated instruction tuning | n | server-client | n | n | n | y (training) | u | paper | low |
| Mixture-of-Agents (MoA) | Layered proposer/aggregator ensemble | n | layered | n | n | n | y | y | paper (2024), high ★ | low |
| OpenAI Swarm / Agents SDK | Handoff-based orchestration (Swarm deprecated) | n | proc loop | n | n | n | y | y | ~22k★, deprecated | med |
| OpenHands (OpenDevin) | Coding agents over shared EventStream | n | event-stream | n | p | n | no (code) | y | ~78k★ | med |
| LAMER | Cross-episode meta-RL credit assignment | p | n | n | y | y | y | u | paper (2025) | low |
| RL² | Recurrent meta-RL (Bayes-optimal in hidden state) | p | n | n | y | n | y | u | paper | low |
| MRA | Meta representations for agents | p | n | n | n | n | y | u | paper | low |
| MAML | Model-agnostic meta-learning | n | n | n | n | y | y | u | paper (2017) | low |
| Agent-Testing Agent (ATA) | Adversarial agent testing | p | n | n | u | n | y (testing) | u | paper (2025) | low |
| Plans Don't Persist | Diagnostic: plan persistence; context reinstatement | n | n | n | n | n | y | u | paper (2026) | low |

---

## 3. Buy vs steal vs build, pillar by pillar

| Pillar | Verdict | Best candidate | One-line justification |
|---|---|---|---|
| Event bus / event-sourcing | **Adopt** the substrate; **steal** agent framing | Temporal (Go SDK) + Confluent/StreamNative pattern | Durable replay and pub/sub are solved infra; reinventing a broker is wasted effort, but the "agent = subscriber that emits events" framing must be ported, not bought. |
| CBR history-driven routing | **Build** (steal pattern) | CASCADE (blueprint), Memento, CBRkit | The 4R cycle + bandit-balanced retrieval is well-specified, but every implementation is Python research code; our Go router is the core IP. |
| Emergent / dynamic DAG | **Build** (steal pattern) | StreamNative emergent-topology; open-multi-agent for runtime decomposition | Our "DAGs emerge from subscriptions + history routing" has no drop-in; closest is stream-native topology, which is a pattern not a product. |
| Learned workflow selection | **Build degraded** (steal search) | AFlow (MCTS over workflows); FlowReasoner | All mature work assumes a numeric oracle we lack; we steal the *search/selection structure* but cannot adopt the optimizers as-is. |
| Self-modifying / self-evolving orchestration | **Steal** (archive + gate) | ADAS (archive) + ACE (playbook scope) + DGM (empirical gate) | The archive-propose-validate loop is exactly the Consultant; ACE shows how to evolve a *playbook* rather than dangerous code rewrites. |
| Summary / reflection pipelines | **Steal**, nearly **adopt** | ReasoningBank | Distilling labeled, reusable insight items from both successes and failures *is* our summary-bot spec; reproducible without their code. |
| Org (non-code) process learning | **Build** (steal conceptually) | AWO + Agentic-AI Process Observability + PM×ABMS survey | Process mining (discovery + enhancement) is the only mature analog for the Consultant; nothing productized for agent orgs. |

---

## 4. Patterns worth stealing

**How the best systems do history / case-based strategy selection.**
- **Case representation is explicit and typed.** The Hatalis survey's `c=(P,S,O,M)` (problem, solution, outcome, meta) and ReasoningBank's `{title, description, content}` items both argue for *distilled* cases, not raw traces. Steal this: your history store should retain structured, retrievable case records — event-context + chosen strategy + measured outcome — not just transcripts.
- **Retrieve from successes AND failures.** ReasoningBank's central finding is that failure trajectories are as informative as successes; AutoGuide builds "state-aware guidelines" labeled by when-to-apply. Steal: your summary bots must mint negative ("when X, do NOT do Y") memories, not just success patterns.
- **Treat retrieval-reuse as a bandit.** CASCADE frames CBR reuse as a contextual bandit to balance exploit (reuse the best-scoring precedent) vs explore (try a variant) — and crucially it learns *only the retrieval policy*, leaving the model frozen. This is the single most transferable idea for us: it gives principled exploration without an oracle, and it confines learning to a small, auditable surface.
- **Key strategies to context, not to global state.** CLIN stores causal abstractions ("X may be necessary for Y") keyed to task conditions; AutoGuide retrieves by current state; Generative Agents score memories by relevance × recency × importance (CrewAI productizes the same composite). Steal the composite scoring and the context-keying — it is what makes routing precedent-based rather than global-average.

**How self-evolving frameworks search/optimize workflows — and avoid degeneracy.**
- **ADAS / Automated-Agent-Design / AgentSquare** keep an **archive of prior designs** and condition new proposals on it; AgentSquare adds a **performance predictor to prune** before expensive evaluation. Steal: the Consultant should propose against an archive and cheaply pre-score proposals.
- **AFlow / ScoreFlow** treat workflow construction as **search with execution feedback** (MCTS; Score-DPO). The guardrail against degeneracy is that *every mutation is empirically evaluated on a held-out set before adoption*. This is the discipline we must replicate even without a clean oracle (see traps below).
- **Darwin Gödel Machine** is the clearest anti-degeneration design: it self-rewrites code but **keeps an archive of variants and only retains empirically validated improvements** (open-ended search, not greedy). GPTSwarm constrains the search to a typed graph (edges/prompts) so mutations stay well-formed.
- **ACE** is the safest analog for us: it evolves a **structured playbook** via separate generator/reflector/curator roles, never touching model weights or core code. The role-separation (one writer, one critic, one curator) is a reusable safeguard against a single agent rationalizing its own bad edits — Execute-Distill-Verify makes the same anti-self-confirmation argument by decoupling who executes, who distills, and who validates.

**How reflective routers key strategies to context.**
- **ReasoningBank** retrieves relevant strategy items into context before acting (influence via injection, not a hard routing switch). **AutoGuide** retrieves the guideline matching the *current state*. **CLIN** retrieves causal memory tied to the task. The common pattern: *strategy memories carry an explicit applicability condition*, and routing is "match condition → inject/select." Steal: store applicability predicates with each memory so the router can match on event context, not just embedding similarity.

**How event-driven agent runtimes structure pub/sub + chaining.**
- **Confluent/Kafka pattern:** topics as swimlanes, key-based partitioning, consumer groups, immutable log as source of truth; four coordination patterns (orchestrator-worker, hierarchical, blackboard, market). **StreamNative:** agents declare produced/consumed event types; **topology emerges from subscriptions** and new bots attach to a running stream with zero disruption — this is exactly your "add a summary/consultant bot later" requirement.
- **Event-sourcing for free replay:** Temporal/Restate (durable execution, full event-history replay), Agent-Orchestration (Haaziq386) and OpenHands (EventStream of Actions/Observations) all reconstruct state from an append-only log. Steal: make the event log the *single source of truth* so the Consultant mines exactly the same log that drives execution, and so you get time-travel debugging and crash recovery for free.
- **Schema discipline:** Auxiliobits/Atlan stress event taxonomy, schema registry, and dead-letter handling. Steal early: a normalized event schema is the foundation the CBR store and process-mining both depend on.

---

## 5. Traps the prior art already hit

**Credit assignment (our stated hardest risk).** This is unsolved even in the strong work. ReasoningBank and CASCADE both lean on **binary/LLM-as-judge outcome signals** because dense rewards are unavailable; LAMER exists specifically because cross-episode credit assignment is hard. Documented mitigations to steal: (a) **LLM-as-judge with self-contrast** (ReasoningBank/MaTTS generate multiple trajectories and compare, reducing single-judge noise); (b) **decouple the evaluator from the executor** (Execute-Distill-Verify, Bayesian Control's Generator/Critic/Oracle split) so the thing scoring an outcome is not the thing that produced it; (c) **delayed/holistic attribution** (MAS-Orchestra optimizes system-level reward rather than per-step). For a general org with no oracle, expect to combine human spot-checks + LLM-judge + downstream proxy signals, and to *store the uncertainty* of each outcome label alongside the case.

**Reward hacking / metric gaming.** The workflow-optimization line (AFlow, ScoreFlow, FlowReasoner, GPTSwarm) optimizes against a metric and will overfit it; this is precisely why their results on coding/math benchmarks do not transfer to fuzzy org tasks — **be skeptical of any "beats SOTA on WebArena/SWE-bench" claim as evidence for general orgs.** Mitigations: held-out evaluation before adoption (AFlow), multi-objective scoring with cost as a soft constraint (G-Designer, MaAS), and never letting the optimizer choose its own metric. For us: the eval/approval gate must use metrics the Consultant cannot edit.

**Oscillation / ossification.** Two failure modes. Ossification: Metaswarm runs the *same fixed 9-phase workflow every time* and only reflects post-hoc — a self-improving system that never changes its routing. Oscillation/myopia: greedy workflow search can thrash. EvoFlow's mitigation is to **maintain a diverse population** of structurally distinct workflows rather than collapsing to one; DGM's open-ended archive serves the same purpose. Steal: keep a *population* of candidate strategies per context and decay-but-don't-delete losers, so the router can recover when conditions shift.

**Runaway self-modification.** The Gödel-machine lineage (Gödel Agent, DGM) literally rewrites its own code — powerful and dangerous. Every responsible variant gates it: **DGM** validates before retaining; **ACE** never touches code, only a playbook; **AutoSpec** evolves rules but through counterexample-guided synthesis with constrained edit operators; **MonoScale** guarantees non-decreasing performance when expanding the agent pool. Steal all of these: the Consultant should (a) propose, never auto-apply; (b) edit a structured playbook/process spec rather than code; (c) pass a human/eval approval gate; (d) be reversible (archive every prior version).

**Event loops / storms.** The EDA sources (Confluent, Auxiliobits, Atlan) flag this directly: feedback cycles where agents' emitted events re-trigger themselves, and fan-out amplification. Documented mitigations: **dead-letter queues**, idempotency/keys, consumer-group backpressure, and event-taxonomy discipline so a summary bot subscribing to `session.completed` cannot accidentally emit an event that re-spawns sessions. For a self-modifying system this is acute: a Consultant whose process change increases event volume could create a positive feedback storm — rate-limit and budget the meta-loop separately from the work loop.

**Self-confirmation bias in reflection.** Execute-Distill-Verify is named for this trap: an agent that both acts and judges its own action will rationalize. Mitigation: heterogeneous agent pool with separated execution/distillation/validation roles. Directly applies to your summary bots (should not grade the sessions they summarize) and Consultant (should not approve its own proposals).

---

## 6. What still does NOT exist

The catalog is dense, but the intersection you are targeting is empty. Specifically:

1. **CBR-routed orchestration for general business orgs.** Every explicit CBR-routing system is either a survey (Hatalis), a single-agent action selector (Memento, CASCADE, ExpeL), a model/skill router (Task-Aware LLM Council, SkillRouter, Agent-as-a-Router), or coding/web/domain-scoped (DS-Agent, CBR-RAG, MapCoder). The closest general-purpose, deployment-time, outcome-feedback CBR system is **CASCADE** — but it routes *which case to reuse for one agent*, not *which multi-step org strategy/DAG to spin up*. **No system uses a case base of (event, reaction, outcome) to select an emergent multi-agent workflow for non-code work.** The Du 2026 memory survey explicitly flags "memory-driven selection among alternative workflows based on past outcomes" as *underexplored*.

2. **A Consultant meta-agent that mines a live event log and proposes process changes through an eval gate, for general orgs.** The pieces exist in separate worlds: **process mining** (Agentic-AI Process Observability, Agent Miner, the PM×ABMS survey) can *discover and enhance* process models from event logs but is observational and post-hoc, with no self-modification loop; **AWO** mines traces and crystallizes meta-tools (closest functional analog) but auto-adds them without a real eval/approval gate and frames it as efficiency, not org-process change; **ACE/ADAS/DGM** have the propose-validate loop but operate on prompts/code/playbooks for single agents or coding benchmarks. **Nobody connects "process-mine the org's own event history → propose org-level process changes → gate them → apply."** That is your novel surface.

3. **Emergent-DAG-by-subscription combined with history-driven routing.** StreamNative/Confluent give emergent topology via pub/sub; CBR systems give history-driven choice; **no system makes the routing decision at each event by consulting outcome history AND letting the resulting topology emerge from subscriptions.** Existing "dynamic DAG" systems (open-multi-agent, Magentic-One, MaAS, FlowReasoner) plan a graph up front per query via an orchestrator or learned generator — they do not let the graph *emerge* from independent history-routed reactions.

4. **A Go-native implementation of any of the above.** The entire research corpus is Python; the durable-execution layer (Temporal) is the only mature Go-friendly piece. Everything in your routing/CBR/Consultant stack will be original Go.

5. **General (non-code) evidence for self-improvement at all.** Be skeptical: the strongest self-evolution results (DGM, AFlow, EvoMAC, SE-Agent, ToP, MegaAgent, Co-Learning, Metaswarm, OpenHands) are **coding/SWE-bench-scoped**, where a near-clean oracle (tests pass / benchmark score) exists. The systems that *are* general (ReasoningBank, CASCADE, ELITE, WISE-Flow, ACE) are memory/strategy-injection systems, not self-modifying orchestrators. The combination of "self-improving orchestration" + "general org" + "no oracle" is exactly where prior art thins to nothing — which is both the opportunity and the warning: the field's successes ride on having a verifier you do not have, so **invest disproportionately in the outcome-signal/credit-assignment layer**, because that is the load-bearing assumption every adoptable pattern silently depends on.

**Bottom line for the build:** adopt Temporal (or Kafka/Pulsar) for the event-sourced bus; port the stream-native "agents subscribe and emit" framing; build the CBR router in Go using CASCADE's bandit-gated 4R as the blueprint and ReasoningBank as the summary-bot spec; model the Consultant on ADAS's archive + ACE's playbook-scoped, role-separated, propose-validate-gate discipline; and treat the absence of an oracle — not the absence of frameworks — as the real engineering problem.
