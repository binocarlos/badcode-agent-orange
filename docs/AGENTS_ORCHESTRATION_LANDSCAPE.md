<!--
Generated 2026-06-26 by an exhaustive discovery workflow (orchestration-landscape):
18 parallel search angles -> fetch + per-project extraction (awesome-lists exploded) ->
completeness-critic round -> synthesis. 220 distinct projects catalogued.
Companion docs: ARCHITECTURE.md (the design), AGENTS_STACK_DECISION.md, AGENTS_RESEARCH.md (seed).
Index: AGENTS_DESIGN.md.
NOTE: maturity/star-counts and capability flags are best-effort from web sources, some unverified.
Treat as a map to investigate, not gospel — re-check a project's primary repo before adopting.
-->

# Agent Orchestration / Manager-Layer Landscape

*A reference survey for building a continuously-running manager agent that turns a vague business goal into a spec, decomposes it into a kanban board of tickets, spawns/assigns scoped worker agents in sandboxes, reviews their work, and escalates to humans.*

---

## 1. Executive read

No, a true drop-in "manager layer over worker agents" that does the **full loop you want — vague business goal → spec → kanban board → dynamically-spawned scoped sandbox workers → adversarial review → human escalation, running continuously — does not exist off the shelf.** Dozens of projects implement *slices* of it, but almost every mature one is scoped to **software-engineering tasks** (issue → PR), and almost every "autonomous business" one is either a thin demo, a fixed role-roster, or marketing ahead of substance. The closest integratable building blocks are **AgentsMesh** (fleet of sandbox runners + Autopilot control agent + kanban + human takeover, self-hosted, production-ish), **builderz-labs/mission-control** (six-column kanban + Aegis review gate + cron/webhook triggers + auto-dispatch, but alpha), and **saltbo/agent-kanban** (leader agent decomposes goals into tickets, daemon spawns a worker-per-task in git worktrees, leader reviews/merges). For the *spec→review* spine specifically, **zeroshot**, **takt**, and **APM (Agentic Project Management)** are the best patterns to steal. For the *durable continuous-operation substrate*, **Temporal/Trigger.dev/Hatchet/Restack** are the battle-tested non-agent foundations to build on rather than fork.

Bottom line: **fork/integrate** AgentsMesh or agent-kanban for the board+sandbox-spawn spine; **steal patterns** from zeroshot/takt/APM/Magentic-One for the spec+review+ledger loop; **build on** a durable engine (Temporal-class) for continuous operation; and **build yourself** the genuinely missing piece — a goal→spec planner that generalizes beyond coding and a manager that *creates* (not just routes among) scoped workers with adversarial review and human escalation as first-class.

---

## 2. Full catalog by category

Legend — **M** = manager/planning layer · **Spawn** = dynamic worker spawning · **Board** = kanban/ticket model · **Cont/Trig** = continuous operation / cron-event triggers · **Host** = self-hostable · **Rel** = relevance to our goal. Values: `yes / partial / no / static` (static-roster) / `?` (unknown).

### orchestration-framework

| Project | What it is | M | Spawn | Board | Cont/Trig | Host | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|
| [a5c-ai/babysitter](https://github.com/a5c-ai/babysitter) | JS-code-as-authority process orchestrator; quality gates, breakpoints, harness delegation; `forever` mode | yes | yes | no | yes / no | yes | ~1.4k★, active | high |
| [open-multi-agent](https://github.com/open-multi-agent/open-multi-agent) | Coordinator decomposes goal into replayable task DAG, parallelizes | yes | static | no | no / no | yes | ~6.4k★, Apr 2026 | high |
| [agency-orchestrator](https://github.com/jnMetaCode/agency-orchestrator) | One-line req → DAG by selecting from 216+ expert roles; web Studio | yes | static | no | no / no | yes | ~1.5k★, v0.6 | high |
| [omnigent](https://github.com/omnigent-ai/omnigent) | YAML agents; tech-lead "Polly" delegates to coders in git worktrees, routes diffs to reviewer | yes | static | no | partial / no | yes | ~5k★, alpha v0.2 | high |
| [dohooo/helmor](https://github.com/dohooo/helmor) | Local workbench for multi-agent software dev | ? | ? | ? | ? / ? | yes | ~1.2k★ | medium |
| [CompanyHelm](https://github.com/CompanyHelm/companyhelm) | Distributed orchestrator, task mgmt, agent-to-agent convos | yes | ? | yes | ? / ? | yes | unknown | high |
| [gastown](https://github.com/steveyegge/gastown) | Multi-agent orchestration w/ persistent work tracking (Steve Yegge) | yes | ? | yes | yes / ? | yes | notable author | high |
| [orc](https://github.com/spencermarx/orc) | Hierarchical orchestrator w/ review pipelines | yes | ? | ? | ? / ? | yes | unknown | high |
| [ORCH](https://github.com/oxgeneral/ORCH) | CLI runtime, typed agent teams driven by state machine | partial | static | ? | ? / ? | yes | unknown | high |
| [claude-flow](https://github.com/ruvnet/claude-flow) | Deploys coordinated multi-agent swarms | yes | yes | ? | ? / ? | yes | widely known (ruvnet) | high |
| [kodo](https://github.com/ikamensh/kodo) | Autonomous orchestrator; work cycles w/ independent verification | yes | ? | ? | yes / ? | yes | unknown | high |
| [Bernstein](https://github.com/chernistry/bernstein) | Deterministic (zero-LLM) orchestrator, parallel coders, TDD verify | yes | yes | no | yes / ? | yes | emerging | high |
| [MagiC](https://github.com/kienbui1995/magic) | "Kubernetes for AI agents"; routing, DAG, circuit breakers | yes | yes | no | yes / ? | yes | emerging | medium |
| [Microsoft Agent Framework](https://github.com/microsoft/agent-framework) | Merges Semantic Kernel + AutoGen; orchestration primitives | partial | ? | no | yes / ? | yes | MS-backed, new | medium |
| [OpenAI Agents (Python)](https://github.com/openai/openai-agents-python) | Handoffs, guardrails, tools; manager-as-tool pattern | partial | no | no | yes / ? | yes | OpenAI | medium |
| [OpenAI Swarm](https://github.com/openai/swarm) | Educational handoff-based routing | partial | no | no | yes / no | yes | OpenAI, educational | medium |
| [Swarms](https://github.com/kyegomez/swarms) | Prod multi-agent infra; hierarchical/director topologies | yes | partial | no | yes / ? | yes | several-k★ | high |
| [agentUniverse](https://github.com/agentuniverse-ai/agentUniverse) | PEER (Plan/Execute/Express/Review) + DOE patterns | yes | ? | no | ? / ? | yes | Ant Group origin | medium |
| [Shannon](https://github.com/Kocoro-lab/Shannon) | Prod multi-agent platform; reliability, durability, cost | partial | ? | no | yes / ? | yes | newer | medium |
| [Semantic Kernel](https://github.com/microsoft/semantic-kernel) | MS SDK; planners decompose goals into plugin steps | partial | no | no | yes / ? | yes | ~20k★, MS | medium |
| [AutoGen](https://github.com/microsoft/autogen) | GroupChatManager coordinates conversational agents; reviewer/critic patterns | yes | yes/static | no | yes/? / no | yes | MS, very high | medium-high |
| [AgentScope](https://github.com/agentscope-ai/agentscope) | Alibaba scalable distributed multi-agent; coordinator/pipeline | partial | partial | no | ? / ? | yes | Alibaba, active | high |
| [Camel-AI](https://github.com/camel-ai/camel) | Role-playing agents + workforce coordinator module | partial | partial | no | no / no | yes | large★, research | medium |
| [Pydantic AI](https://ai.pydantic.dev/) | Type-first agent framework; delegation + output validators | partial | no | no | no / no | yes | Pydantic team | medium |
| [LlamaIndex Agents](https://docs.llamaindex.ai/) | Data-centric agents; Workflows + AgentWorkflow | partial | no | no | no / no | yes | company-backed | medium |
| [Haystack Agents](https://docs.haystack.deepset.ai/) | RAG/NLP pipelines w/ tool use, conditional routing | no | no | no | no / no | yes | deepset, mature | low |
| [LangChain](https://www.langchain.com/) | Foundational chain/tool/agent primitives | no | no | no | no / no | yes | very high | low |
| [cohen-liel/hivemind](https://github.com/cohen-liel/hivemind) | PM Agent → typed TaskGraph DAG + Architect pre-planning; specialized agents | yes | static | no | no / no | yes | ~102★, early | high |
| [cuibuaa/flow-crew](https://github.com/cuibuaa/flow-crew) | Brief → pipeline stages w/ gates/retries; "Reality-Gate" evidence checks | yes | static | no | partial / no | yes | ~7★, v0.3 | high |
| [synapse-ai](https://github.com/synapseorch-ai/synapse-ai) | Platform to create/connect/orchestrate agents | partial | ? | ? | ? / ? | yes | ~293★ | medium |
| [dfinke/PSAI](https://github.com/dfinke/PSAI) | PowerShell multi-agent orchestration framework | partial | ? | ? | ? / ? | yes | ~265★ | medium |
| [eggai-tech/EggAI](https://github.com/eggai-tech/EggAI) | Async-first meta-framework for enterprise multi-agent | partial | ? | ? | ?(async) / ? | yes | ~55★ | medium |
| [automatos-ai](https://github.com/AutomatosAI/automatos-ai) | Context engineering + multi-agent for enterprise automation | partial | ? | ? | ? / ? | yes | ~40★ | medium |
| [ParthivPandya/multi-agent-orchestrator](https://github.com/ParthivPandya/multi-agent-orchestrator) | 8 autonomous agents, Vision-to-Code, on Groq | partial | static | ? | ? / ? | yes | ~4★ | medium |
| [adrq/agentbeacon](https://github.com/adrq/agentbeacon) | Multi-agent orchestrator for AI coding tools (Rust) | partial | ? | ? | ? / ? | yes | ~7★ | medium |
| [codex-threaddeck](https://github.com/readysteadyscience/codex-threaddeck) | Turns a Codex convo into a dispatch desk over others | partial | ? | ? | partial / ? | yes | ~4★, Shell | medium |
| [AgentRearrange-Paper](https://github.com/The-Swarm-Corporation/AgentRearrange-Paper) | DSL primitive for mixed-topology Swarms workflows | partial | no | no | ? / ? | yes | ~5★, paper | low |
| [Microsoft Magentic-One](https://www.microsoft.com/en-us/research/articles/magentic-one-a-generalist-multi-agent-system-for-solving-complex-tasks) | Orchestrator + Task Ledger/Progress Ledger over specialist agents; self-correct loop | yes | static | yes | yes / ? | ? | MS Research, on AutoGen | high |
| [CrewAI](https://github.com/crewAIInc/crewAI) | Role-based crew; hierarchical manager delegates & validates | yes | static | no | yes/no | yes | very high, Fortune-500 claims | high |
| [Google ADK](https://github.com/google/adk-python) | Root orchestrator + sequential/parallel/loop agents | yes | static | no | yes / ? | yes | Google, growing | high |
| [Claude Agent SDK](https://docs.anthropic.com/en/docs/claude-code/sdk) | Tool-use loop; sub-agents as tools; MCP | partial | yes | no | ? / ? | no | Anthropic, high | medium |
| [Mastra](https://github.com/mastra-ai/mastra) | TS Supervisor Agent + Workflows; suspend-resume, OTel | partial | static | no | yes / ? | yes | emerging, TS-native | high |
| [Langroid](https://github.com/langroid/langroid) | Actor-model agents delegate via message passing | yes | no | no | yes / no | yes | established OSS | medium |
| [BeeAI (IBM)](https://github.com/i-am-bee/beeai-framework) | MCP-first interop; discover/integrate third-party agents | partial | yes | no | yes / ? | yes | IBM/Linux Foundation | medium |
| [Agents (AIWaves)](https://github.com/aiwaves-cn/agents) | Controller symbolically plans SOPs | yes | partial | no | no / no | yes | research | high |
| [IX](https://github.com/kreneskyp/ix) | Self-host no-code graph editor for agent fleets (LangChain) | yes | partial | no | yes / ? | yes | smaller community | medium |
| [Dify](https://github.com/langgenius/dify) | Visual agentic-workflow platform | partial | partial | no | yes / ? | yes | ~146k★ | medium |
| [Microsoft Conductor](https://github.com/microsoft/conductor) | YAML multi-agent workflows + Copilot SDK + dashboard | yes | partial | partial | no / ? | yes | MIT, v0.1.1 early | medium |
| [AWS Multi-Agent Orchestrator](https://awslabs.github.io/multi-agent-orchestrator/general/how-it-works) | Classifier routes queries to specialized agents | yes | partial | no | yes / ? | yes | AWS, recent | high |
| [Cognizant Neuro AI](https://www.cognizant.com/us/en/services/neuro-intelligent-automation/neuro-generative-ai-adoption) | Drag-drop Model Orchestrator over 4 agents | yes | ? | no | ? / ? | no | Cognizant, vendor | medium |
| [Langflow](https://medium.com/logspace/langflow-1-1-release-b6df2f8189a6) | Visual builder; agents call agents as tools | partial | partial | no | ? / ? | yes | company-backed | medium |
| [LlamaIndex](https://www.llamaindex.ai/) | Agent/data framework over indexed data | partial | ? | no | ? / ? | yes | established | medium |
| [Claude Code](https://claude.ai/code) | Agentic CLI; plans multi-step, can dispatch sub-agents | partial | ? | no | no / no | no | Anthropic | medium |
| [GSD (Get Shit Done)](https://github.com/gsd-build/get-shit-done) | Meta-prompting; spawns parallel research/plan/execute/verify around a spec | yes | yes | no | no / no | yes | ~61k★, Dec 2025 | high |
| [Restack](https://github.com/restackio) | Durable workflow engine for agents (Temporal-style), K8s | partial | static | no | yes / yes | yes | company OSS SDKs | medium |
| [Inngest AgentKit](https://github.com/inngest/agent-kit) | TS agent networks; Router (code or agent-based) over shared State | partial | static | no | yes / partial | yes | ~906★, Inngest | medium |
| [Julep](https://github.com/julep-ai/julep) | "Firebase for AI agents"; YAML stateful workflows | no | ? | no | partial / ? | yes | ~6.6k★, transitioning | medium |
| [ControlFlow (Prefect)](https://github.com/PrefectHQ/ControlFlow) | Tasks/Agents/Flows; **archived** Mar 2026 → Marvin | partial | static | no | no / ? | yes | ~1.4k★, archived | low |
| [Flowise](https://github.com/FlowiseAI/Flowise) | Visual low-code multi-agent/supervisor builder | partial | no | no | no / ? | yes | ~54k★ | low |
| [Cline](https://github.com/cline/cline) | IDE/CLI coding agent; Plan/Act + multi-agent teams + Kanban + cron | partial | yes | yes | partial / yes | yes | ~63.9k★, company | medium |
| [goose](https://github.com/block/goose) | Block/LF general agent; subagents (one level) + cron scheduler | partial | yes | no | yes / yes(cron) | yes | ~50k★, AAIF | medium |
| [LangGraph Multi-Agent Supervisor](https://github.com/langchain-ai/langgraph-supervisor-py) | Official supervisor lib; nested hierarchies, shared memory | yes | static | no | no / no | yes | ~1.6k★, LangChain | high |
| [Overstory](https://github.com/jayminwest/overstory) | Coordinator spawns Scout/Builder/Reviewer/Lead/Merger in worktrees; SQLite mail | yes | yes | no | yes / no | yes | ~1.3k★, **archived** May 2026 (→ Warren) | high |
| [LangGraph on AWS (guidance)](https://github.com/aws-solutions-library-samples/guidance-for-multi-agent-orchestration-langgraph-on-aws) | 5 LangGraph agents as ECS services + supervisor | partial | static | no | yes / ? | yes | ~35★, AWS official | medium |
| [langgraph-multiagent-with-a2a](https://github.com/5enxia/langgraph-multiagent-with-a2a) | Supervisor over A2A-protocol distributed workers | yes | static | no | no / no | yes | ~16★, educational | medium |
| [OpenAI Agents SDK (JS)](https://github.com/openai/openai-agents-js) | TS handoffs; triage/manager pattern | partial | no | no | no / no | yes | OpenAI official | high |
| [openai-swarm-basic-demo](https://github.com/learnwithtalhagillani/openai-swarm-basic-demo) | Minimal Swarm orchestrator → 2 workers | partial | static | no | no / no | yes | ~7★, demo | medium |
| [Multi-Agent Portfolio Collaboration](https://developers.openai.com/cookbook/examples/agents_sdk/multi-agent-portfolio-collaboration/multi_agent_portfolio_collaboration) | Head PM agent → 3 specialist sub-agents-as-tools | yes | static | no | no / no | yes | OpenAI cookbook | high |
| [OpenManus](https://github.com/FoundationAgents/OpenManus) | PlanningAgent + run_flow decomposes goal → steps | partial | static | no | no / ? | yes | ~56.7k★, MetaGPT team | medium |
| **LangGraph** (×4 listings: [core](https://langchain-ai.github.io/langgraph/), [.com](https://www.langchain.com/langgraph)) | Directed-graph stateful workflows; supervisor patterns buildable; checkpointing | partial | static | no | yes / ? | yes | most-mature cited | high |
| **CrewAI** (×5 listings incl. [Hierarchical Process](https://docs.crewai.com/en/learn/hierarchical-process), [enterprise](https://www.crewai.com/enterprise)) | Hierarchical manager agent decomposes, delegates, validates | yes | static | no | no / ? | yes | very high, company | high |
| Demo apps: [CrewAI Research Crew](https://github.com/Arindam200/awesome-ai-apps/tree/main/starter_ai_agents/crewai_starter), [AI Hedgefund](https://github.com/Arindam200/awesome-ai-apps/tree/main/advance_ai_agents/ai-hedgefund), [Content Team](https://github.com/Arindam200/awesome-ai-apps/tree/main/advance_ai_agents/content_team_agent), [Due Diligence](https://github.com/Arindam200/awesome-ai-apps/tree/main/advance_ai_agents/due_diligence_agent), [Cosmos Debate Council](https://github.com/Arindam200/awesome-ai-apps/tree/main/advance_ai_agents/cosmos_arena_debate_council), [Study Coach](https://github.com/Arindam200/awesome-ai-apps/tree/main/memory_agents/study_coach_agent), [Deep Researcher](https://github.com/Arindam200/awesome-ai-apps/tree/main/advance_ai_agents/deep_researcher_agent), [Deep Research+Writing](https://github.com/Arindam200/awesome-ai-apps/tree/main/advance_ai_agents/deep_research_writing_agents_nebius_okahu), [MS Agents Starter](https://github.com/Arindam200/awesome-ai-apps/tree/main/starter_ai_agents/microsoft_agents_starter), [AutoGen Starter](https://github.com/Arindam200/awesome-ai-apps/tree/main/starter_ai_agents/autogen_starter), [Conference CFP](https://github.com/Arindam200/awesome-ai-apps/tree/main/advance_ai_agents/conference_agnositc_cfp_generator) | Small role-based demos (CrewAI/Agno/AG2/LangGraph); some w/ reviewer/debate roles | partial | static/no | no | no / no | yes | demos | low-medium |

### agentic-os

| Project | What it is | M | Spawn | Board | Cont/Trig | Host | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|
| [stagewise](https://github.com/stagewise-io/stagewise) | Agentic IDE that creates/orchestrates coding agents | partial | ? | ? | ? / ? | yes | ~6.7k★, company | medium |
| [thesongzhu/Friday](https://github.com/thesongzhu/Friday) | Private control plane for AI agents | partial | ? | ? | ? / ? | ? | ~917★ | medium |
| [goclaw](https://github.com/nextlevelbuilder/goclaw) | OpenClaw in Go; multi-tenant, 5-layer security runtime | ? | ? | ? | ? / ? | yes | ~3.3k★ | medium |
| [Hephaestus](https://github.com/agentlas-ai/Hephaestus) | Agent OS w/ meta-agent builder + A2A routing | yes | yes | ? | ? / ? | yes | unknown | high |
| [centaur](https://github.com/paradigmxyz/centaur) | Self-host team platform; Slack-native + K8s sandboxes | partial | ? | ? | ? / ? | yes | Paradigm-backed | medium |
| [AutoGPT](https://github.com/Significant-Gravitas/AutoGPT) | Pioneer autonomous agent → platform w/ visual builder, task queue | yes | yes | partial | yes / partial | yes | very large ecosystem | medium |
| [OpenClaw](https://github.com/openclaw/openclaw) | Self-host 24/7 personal agent; 5,700+ skills via SOUL.md | partial | yes | no | yes / partial | yes | viral, community | medium |
| [OpenHands](https://github.com/All-Hands-AI/OpenHands) | SWE agents using a computer in a sandbox | partial | partial | no | no / partial | yes | ~40k★, company | medium |
| [Agno](https://github.com/agno-agi/agno) | Full-stack runtime/control plane; agent "teams" w/ leader | partial | ? | no | partial / ? | yes | medium-high, company | medium |
| [HiClaw](https://github.com/agentscope-ai/HiClaw) | Manager-Workers OS; managers coordinate & assign to workers | yes | yes | ? | yes / ? | yes | ~4.9k★, agentscope | high |
| [SuperAGI](https://github.com/TransformerOptimus/SuperAGI) | Dev-first platform to build/run concurrent agents + GUI | partial | ? | no | yes / partial | yes | ~15k★, slowed | medium |
| [modimihir07/agentic-os](https://github.com/modimihir07/agentic-os) | Smart Router + visual kanban + APScheduler cron + cost analytics | yes | static | yes | yes / yes | yes | ~58★, v0.2 stable | high |
| [Azure AI Foundry Agent Service](https://azure.microsoft.com/en-us/products/ai-foundry) | Managed Azure agent runtime; connected-agents delegation | yes | ? | no | yes / ? | no | MS, production | medium |
| [Agent OS](https://github.com/imran-siddique/agent-os) | Safety-first "kernel" governing agents w/ POSIX-style primitives | partial | ? | no | yes / ? | yes | ~68★, early | medium |
| [cagent Starter](https://github.com/Arindam200/awesome-ai-apps/tree/main/starter_ai_agents/cagent_starter) | Docker cagent declarative runtime; root → sub-agents | yes | static | no | ? / no | yes | demo, Docker-backed | high |
| [KAOS Starter](https://github.com/Arindam200/awesome-ai-apps/tree/main/starter_ai_agents/kaos_starter) | K8s-native agents as cluster workloads | partial | ? | no | yes / ? | yes | demo, emerging | high |
| [Relevance AI](https://relevanceai.com) | Enterprise agent "workforce"; Manager Agent + nested subagents; L1–L4 maturity | yes | static | no | yes / yes | no | $15M+, enterprise GA | high |

### autonomous-org

| Project | What it is | M | Spawn | Board | Cont/Trig | Host | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|
| [loki-mode](https://github.com/asklokesh/loki-mode) | PRD → deployed product; 41 agents / 8 swarms; RARV cycles | yes | static | ? | yes / ? | yes | unknown | high |
| [opengoat](https://github.com/marian2js/opengoat) | Build autonomous orgs of OpenClaw agents | yes | yes | ? | ? / ? | yes | unknown | high |
| [paperclip](https://github.com/paperclipai/paperclip) | Orchestration for "zero-human companies" | yes | ? | ? | ? / ? | ? | unknown | high |
| [MetaGPT](https://github.com/geekan/MetaGPT) (also [FoundationAgents](https://github.com/FoundationAgents/MetaGPT)) | Software-company-as-code; PM/architect/engineer/QA via SOPs | yes | static | partial | no / no | yes | ~40k★+ | high |
| [agency-swarm](https://github.com/VRSEN/agency-swarm) | "Agencies" w/ CEO/manager delegating to workers along comm flows | yes | static | no | partial / ? | yes | established OSS | high |
| [great_cto](https://github.com/avelikiy/great_cto) | Eng-mgmt layer; 34 specialist agents, full SDLC + compliance | yes | static | ? | ? / ? | yes | ~44★ | high |
| [Hivemoot](https://github.com/hivemoot/hivemoot) | Autonomous teams build software on GitHub | partial | ? | ? | ? / ? | ? | low-medium, verify | high |
| [ChatDev](https://github.com/OpenBMB/ChatDev) | Virtual software company; CEO/CTO/programmer/tester convo chains | yes | static | no | no / ? | yes | ~33.5k★ | high |
| [EvoMap (evolver)](https://github.com/EvoMap/evolver) | Worker Pool + AI Council governance + Evolution Circles | yes | yes | ? | yes / ? | ? | ~8.7k★ | high |
| [TradingAgents](https://github.com/TauricResearch/TradingAgents) | Fund-manager + analyst roles coordinate trades | yes | static | no | partial / ? | yes | ~88k★ | medium |
| [XAgent](https://github.com/OpenBMB/XAgent) | Outer planner + inner executor loops; dispatcher | yes | partial | no | partial / no | yes | ~8.5k★, OpenBMB | medium |
| [auto-co](https://github.com/NikitaDmitrieff/auto-co-meta) | "Autonomous company OS"; 14 role agents, continuous cycles, human escalation | yes | static | ? | yes / ? | yes | ~36★, MIT | high |
| [AgentGPT](https://github.com/reworkd/AgentGPT) | Browser single autonomous agent; think/generate/execute loop; **archived** Jan 2026 | partial | no | no | no / no | yes | ~36.2k★, archived | low |
| [Devin (Cognition)](https://devin.ai) | Autonomous AI software engineer; "team of Devins" fan-out; consumes human board | partial | yes | yes | partial / ? | no | enterprise, funded | medium |

### spec-driven

| Project | What it is | M | Spawn | Board | Cont/Trig | Host | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|
| [zeroshot](https://github.com/the-open-engine/zeroshot) | "Conductor" classifies complexity, picks workers + **blind adversarial validators**; iterates to acceptance | yes | partial | yes | no / no | yes | ~1.6k★, v6 | high |
| [takt](https://github.com/nrslib/takt) | YAML plan/implement/review/fix topology; non-skippable review; SDD output contracts | yes | static | partial | no / partial | yes | ~1.2k★, established | high |
| [Dex](https://github.com/francescoalemanno/dex) | Structured Ralph; human-gated planning + multi-reviewer review, 7 backends | yes | ? | ? | yes / ? | yes | unknown | high |
| [Forge](https://github.com/LucasDuys/forge) | Autonomous brainstorm/plan/execute SDD loop | yes | ? | ? | yes / ? | yes | ~29★ | medium |
| [AgentPlane](https://github.com/basilisk-labs/agentplane) | Git-native task→plan→approve→implement→verify | yes | ? | partial | ? / ? | yes | ~70★ | medium |
| [Patchwork](https://github.com/patched-codes/patchwork) | CI-triggered "patchflows" for PR review/fixes/docs | partial | no | no | partial / yes | yes | company-backed | medium |
| [komluk/scaffolding](https://github.com/komluk/scaffolding) | Claude Code plugin; 11 agents/31 skills, OpenSpec + reviewer | partial | static | no | no / no | yes | ~15★, v2.7.3 | medium |
| [GPT Pilot (Pythagora)](https://github.com/Pythagora-io/gpt-pilot) | Builds app step-by-step; PO/architect/dev/reviewer through spec | yes | static | no | no / no | yes | high, → Pythagora IDE | high |
| [Intent](https://www.intentapp.dev/) | macOS; Coordinator drafts spec → parallel waves; Implementor + Verifier; living spec | yes | yes | partial | yes / yes | no | beta, Augment-backed | high |
| [AWS Kiro](https://kiro.dev) | Agentic IDE; Requirements/Design/Tasks 3-phase + hooks | partial | ? | no | no / no | no | AWS, proprietary | medium |
| [GitHub Spec Kit](https://github.com/github/spec-kit) | Specify/Plan/Tasks/Implement; "constitution" contract; 30+ agents | yes | static | no | no / no | yes | ~93k★, GitHub | high |
| [BMAD-METHOD](https://github.com/bmad-code-org/BMAD-METHOD) | 12+ SDLC role agents (PM/architect/UX/dev/QA/SM), role-separated | yes | static | ? | no / no | yes | ~46.7k★, MIT | high |
| [OpenSpec](https://github.com/Fission-AI/OpenSpec) | Proposal-centered spec workflow (ADDED/MODIFIED/REMOVED deltas) | no | no | no | no / no | yes | OSS | medium |
| [Tessl](https://www.tessl.io) | Spec Registry (10k+ library specs) for MCP agents | no | no | no | ? / no | ? | company product | medium |
| [AgentFlow](https://github.com/lupantech/AgentFlow) | Trainable planner/executor/verifier/generator; Flow-GRPO (Stanford) | yes | static | no | no / no | yes | ~1.5k★, research | medium |
| [Portia AI](https://github.com/portiaAI/portia-sdk-python) | Structured plan → stateful execution + human-in-loop | yes | static | no | no / no | yes | ~1.1k★, company | medium |
| [APM (Agentic Project Management)](https://github.com/sdi2200262/agentic-project-management) | Planner → Spec/Plan/Rules; Manager assigns to Workers, reviews; memory/handover; APM Auto spawns ephemeral subagents | yes | partial | no | no / no | yes | ~2.3k★, active | high |

### board-ticket-driven

| Project | What it is | M | Spawn | Board | Cont/Trig | Host | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|
| [AgentsMesh](https://github.com/AgentsMesh/AgentsMesh) | Fleet of self-host runners spawn PTY pods; **Autopilot** control agent; kanban; human takeover | yes | yes | yes | yes / ? | yes | Go, ~2.2k★, v0.44, prod-ish | high |
| [builderz-labs/mission-control](https://github.com/builderz-labs/mission-control) | 6-col kanban; auto-dispatch; **Aegis** review gate; cron + webhooks; WS/SSE | yes | partial | yes | yes / yes | yes | ~5.4k★, alpha v2 | high |
| [Fusion](https://github.com/Runfusion/Fusion) | Multi-node orchestrator; kanban + plan-review-execute gates | yes | ? | yes | ? / ? | yes | unknown | high |
| [vibe-kanban](https://github.com/BloopAI/vibe-kanban) | Kanban board for managing AI coding agents | partial | no | yes | ? / ? | yes | BloopAI, popular | medium |
| [Miyabi](https://github.com/ShunsukeHayashi/Miyabi) | Issue-Driven Dev; 7 coding + 14 business agents; GitHub issues board; 172+ MCP | yes | yes | yes | yes / partial | yes | emerging | high |
| [GNAP](https://github.com/farol-team/gnap) | Git-native coordination via 4 JSON files; any agent that can push | partial | yes | yes | yes / partial | yes | novel protocol | high |
| [TeamHero](https://github.com/sagiyaacoby/TeamHero) | Claude Code-based; task lifecycle dashboard + autopilot | yes | yes | yes | yes / ? | yes | emerging | high |
| [BabyAGI](https://github.com/yoheinakajima/babyagi) | Task-creation + prioritization loop over an execution worker | yes | no | yes | yes / no | yes | foundational | high |
| [LoopTroop](https://github.com/looptroop-ai/LoopTroop) | LLM council drafts/scores/votes; decompose into "beads"; Ralph loops 10h+ | yes | static | yes | yes / ? | yes | ~33★, alpha | high |
| [Relay](https://github.com/jcast90/relay) | Local-first MCP; ticket DAGs + PR tracking | partial | ? | yes | ? / ? | yes | ~4★ | medium |
| [amux (mixpeek)](https://github.com/mixpeek/amux) | Agent multiplexer; web dashboard + kanban + REST API | partial | ? | yes | ? / ? | yes | ~247★ | medium |
| [Composio Agent Orchestrator](https://github.com/ComposioHQ/agent-orchestrator) | Multiple coding agents in worktrees → PRs; lifecycle mgr handles CI/retries | yes | ? | yes | yes / yes(CI) | yes | prod-ready, MIT | high |
| [Emdash](https://github.com/generalaction/emdash) | Electron env, ~22 CLIs; ticket intake from Linear/GitHub/Jira | no | no | yes | no / yes | yes | YC W26 | medium |
| [Code Conductor](https://github.com/ryanmac/code-conductor) | GitHub-native; agents claim `conductor:task` issues | partial | ? | yes | yes / yes | yes | early, Claude-only | medium |
| [Agent Kanban (vscode)](https://github.com/appsoftwareltd/vscode-agent-kanban) | VS Code + Copilot Chat; AGENTS.md + markdown lanes | no | no | yes | no / no | yes | niche, Copilot-only | medium |
| [Agent Kanban (saltbo)](https://github.com/saltbo/agent-kanban) | Leader agent decomposes goals → tickets, assigns workers in worktrees; leader reviews/merges; daemon polls; SSE | yes | yes | yes | yes / no | yes | ~368★, v1.13, active | high |
| [OpenHands](https://github.com/OpenHands/OpenHands) (dup listing) | Single capable worker over a task; some delegation; consumes GitHub issues | partial | partial | partial | no / ? | yes | ~40k★ | medium |
| [Sweep](https://github.com/sweepai/sweep) | GitHub issue → PR; plan/self-review; pivoted to JetBrains plugin | partial | no | yes | no / partial | yes | ~7.7k★ | low |
| [Codegen](https://github.com/codegen-sh/codegen) | @codegen in Linear/GitHub/Slack/Jira → plan → PR + first-pass review | partial | no | yes | partial / yes(event) | no | funded SaaS | medium |

### durable-triggering

| Project | What it is | M | Spawn | Board | Cont/Trig | Host | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|
| [sortie](https://github.com/sortie-ai/sortie) | Tickets → autonomous coding agent sessions | partial | yes | yes | ? / yes(event) | ? | unknown | high |
| [AXME](https://github.com/AxmeAI/axme) | Durable coordination; crash recovery, approval gates, kill switch (AXP) | yes | yes | partial | yes / ? | yes | prod-focused | high |
| [Temporal](https://github.com/temporalio/temporal) | Durable exactly-once long-running workflows | partial | yes | no | yes / yes | yes | battle-tested | medium |
| [ralph-orchestrator](https://github.com/mikeyobrien/ralph-orchestrator) | Hat-based; loops agents until completion | yes | partial | ? | yes / ? | yes | ~3k★ | high |
| [agx](https://github.com/ramarlina/agx) | Checkpoint engine; Wake/Work/Sleep loops | partial | ? | ? | yes / partial | yes | ~24★ | medium |
| [n8n](https://n8n.io/) | Workflow automation; AI nodes as workers; first-class cron/webhook | no | no | no | yes / yes | yes | ~50k★+ | medium |
| [Lindy](https://www.lindy.ai/) | No-code business automation; "societies of Lindies" | partial | ? | no | yes / yes | no | funded, closed | medium |
| [Trigger.dev](https://github.com/triggerdotdev/trigger.dev) | Durable background-jobs platform for agent tasks | no | no | no | yes / yes | yes | active, company | medium |
| [Conductor (oss)](https://github.com/conductor-oss/conductor) | Event-driven durable workflow engine | partial | no | no | yes / yes | yes | ~31.9k★ | medium |
| [Hatchet](https://github.com/hatchet-dev/hatchet) | Orchestration engine for bg tasks/agents; queues + scheduling | partial | no | no | yes / yes | yes | ~7.4k★ | medium |
| [Baton](https://github.com/mraza007/baton) | Daemon polls GitHub Issues → Claude Code in worktrees; Dispatcher/Reconciler | yes | yes | no | yes / yes(poll) | yes | early-stage | high |
| [Cordum](https://github.com/cordum-io/cordum) | Safety-first orchestration; pre-dispatch policy + job scheduling | partial | ? | no | yes / partial | yes | ~484★ | medium |
| [Temporal Agents](https://github.com/Arindam200/awesome-ai-apps/tree/main/advance_ai_agents/temporal_agents) | Agents on Temporal durable workflows | partial | no | no | yes / yes | yes | demo | high |
| [Price Monitoring Agent](https://github.com/Arindam200/awesome-ai-apps/tree/main/advance_ai_agents/price_monitoring_agent) | CrewAI price monitor + Twilio alerts | no | static | no | partial / partial | yes | demo | low |

### memory-runtime

| Project | What it is | M | Spawn | Board | Cont/Trig | Host | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|
| [pro-workflow](https://github.com/rohitg00/pro-workflow) | Self-correcting memory for Claude Code across 50+ sessions | no | no | no | partial / ? | yes | ~2.3k★ | low |
| [Letta (formerly MemGPT)](https://www.letta.com/blog/ai-agents-stack) | Framework + hosting for stateful agents | partial | ? | no | yes / ? | partial | established | medium |
| [MemGPT (letta)](https://github.com/letta-ai/letta) / [cpacker/MemGPT](https://github.com/cpacker/MemGPT) | Tiered self-editing memory for long-lived agents | no | no | no | yes / ? | yes | high, → Letta | low-medium |

### dynamic-spawning

| Project | What it is | M | Spawn | Board | Cont/Trig | Host | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|
| [The Factory (remote-factory)](https://github.com/akashgit/remote-factory) | Self-evolving meta-harness; auto-research/experiment loops | yes | yes | ? | yes / ? | yes | ~38★ | medium |
| [EvoAgentX](https://github.com/EvoAgentX/EvoAgentX) | Auto-generates/evaluates/evolves agentic workflows | partial | yes | no | partial / ? | yes | research | medium |
| [ClawTeam](https://github.com/HKUDS/ClawTeam) | Self-organizing teams; dynamic task allocation + messaging | partial | yes | ? | ? / ? | yes | ~5.3k★, HKUDS | high |
| [agent-orchestrator (AgentWrapper)](https://github.com/AgentWrapper/agent-orchestrator) | Parallel coding agents; task planning + autonomous handoffs | yes | yes | ? | partial / ? | yes | ~7.6k★ | high |
| [hcom](https://github.com/aannoo/hcom) | Rust; agents message/observe/spawn each other across terminals | partial | yes | no | partial / ? | yes | ~189★ | high |
| [AI Legion](https://github.com/eumemic/ai-legion) | Swarm; dynamic task allocation, emergent behavior | partial | yes | no | partial / no | yes | ~1.4k★, inactive | medium |
| [AgentVerse](https://github.com/OpenBMB/AgentVerse) | Recruiter/coordinator dynamically assembles expert team | partial | yes | no | no / no | yes | OpenBMB | high |
| [Background Agents (ColeMurray)](https://github.com/ColeMurray/background-agents) | Sandbox bg agents (Modal/Daytona/Vercel); spawn-task child sessions; CF Workers control plane; cron/Sentry/webhooks | partial | yes | no | partial / yes | yes | ~2.1k★, individual | high |

### other

| Project | What it is | M | Spawn | Board | Cont/Trig | Host | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|
| [preset-io/agor](https://github.com/preset-io/agor) | Multiplayer canvas for Claude Code/Codex sessions | partial | ? | ? | ? / ? | ? | ~1.3k★ | medium |
| [herdr](https://github.com/ogulcancelik/herdr) | Terminal agent multiplexer | no | no | no | ? / ? | yes | ~7.4k★ | low |
| [opensessions](https://github.com/Ataraxy-Labs/opensessions) | tmux sidebar + local HTTP API for agents | no | no | no | ? / ? | yes | ~1.2k★ | low |
| [Bindu](https://github.com/GetBindu/Bindu) | Identity/comms/payments layer for agents | no | ? | no | ? / ? | ? | ~7k★ | low |
| [jarvis-registry](https://github.com/ascending-llc/jarvis-registry) | Secure MCP/Agent gateway to enterprise tools | no | no | no | ? / ? | ? | ~1.6k★ | low |
| [ainativelang](https://github.com/sbhooley/ainativelang) | AI-native language for structured workflows | partial | ? | no | ? / ? | ? | ~827★ | low |
| [awesome-harness-engineering](https://github.com/ai-boost/awesome-harness-engineering) | Curated list (not software) | no | no | no | no / no | no | ~2k★, list | low |
| [Radulepy/mcp-ai-agents-template](https://github.com/Radulepy/mcp-ai-agents-template) | MCP TS template; email/meeting/KB agents | no | static | no | ? / partial | yes | ~7★ | low |
| [selectools](https://github.com/johnnichev/selectools) | Single-agent framework w/ guardrails/audit/RAG | no | ? | no | ? / ? | yes | ~10★ | low |
| [swarm-meal-planner](https://github.com/ALucek/swarm-meal-planner) | Swarm demo notebook | partial | static | no | no / no | yes | ~20★, demo | low |
| [Legal-Swarm-Template](https://github.com/The-Swarm-Corporation/Legal-Swarm-Template) | One-click legal swarm template | partial | ? | no | no / no | yes | ~17★ | low |
| [az9713](https://github.com/az9713/claude-cowork-content-plugin) / [Rainbowlight](https://github.com/Rainbowlight-pixel/claude-cowork-content-plugin) cowork plugins | Claude Cowork Agent Teams showcases | partial | static | no | no / no | ? | ~6–14★ | low |
| [SceneConductor](https://github.com/jhkim0759/SceneConductor) | Multi-agent 3D scene gen research | partial | ? | no | no / no | ? | ~22★, research | low |
| [LangSmith](https://www.langchain.com/langsmith) | Observability/PM for LangChain | no | no | ? | ? / ? | ? | LangChain | low |
| [Amazon Bedrock](https://aws.amazon.com/bedrock/) | Managed inference + guardrails | partial | ? | no | yes / ? | no | AWS | low |
| [Composio](https://composio.dev/) | Tool library for agents | no | no | no | ? / ? | ? | unknown | low |
| [SWE-agent](https://github.com/SWE-agent/SWE-agent) | Single agent: GitHub issue → patch | no | no | no | no / ? | yes | ~19.6k★, research | low |
| [Claude Squad](https://github.com/smtg-ai/claude-squad) | TUI over tmux + worktrees for parallel agents | no | static | no | yes / no | yes | AGPL, active | low |
| [Nimbalyst](https://nimbalyst.com/) | Crystal successor; parallel session mgmt + visual editing | no | static | no | no / no | ? | early | low |
| [Augment Code](https://www.augmentcode.com) | Proprietary context engine (BYOA) | no | no | no | no / no | no | 70.6% SWE-bench | low |
| [Cursor](https://www.cursor.com) | AI editor w/ Plan Mode | no | no | no | no / no | no | widely adopted | low |
| [Aider](https://github.com/Aider-AI/aider) | Terminal pair-programming, single agent | no | no | no | no / no | yes | ~46.7k★ | low |
| [Open Interpreter](https://github.com/OpenInterpreter/open-interpreter) | Single-agent code executor | no | no | no | no / ? | yes | ~64k★ | low |
| [E2B](https://github.com/e2b-dev/E2B) | Secure cloud sandboxes for AI code (infra, not orchestrator) | no | static | no | no / ? | yes | ~12.7k★, GCP/AWS/Azure | low |
| [Botpress](https://botpress.com) | Conversational-bot platform w/ flow orchestration | partial | no | no | yes / ? | partial | established | low |
| [IBM Bee (BeeAI)](https://github.com/i-am-bee) | IBM agent framework | ? | ? | no | ? / ? | ? | IBM | low |
| [OpenDevin/OpenHands](https://github.com/OpenDevin/OpenDevin) | Autonomous SWE agent (now OpenHands) | partial | no | no | no / no | yes | ~30k★+ | medium |
| [Devika](https://github.com/stitionai/devika) | Open Devin alternative; internal planner | partial | no | no | no / no | yes | stalled | medium |
| [Strands Agents](https://github.com/strands-agents/sdk-python) | AWS model-driven SDK; model drives dispatch | partial | yes | no | yes / ? | partial | AWS | low |

---

## 3. Closest to what we want (high-relevance shortlist)

**[AgentsMesh](https://github.com/AgentsMesh/AgentsMesh)** — The single best *structural* match: a self-hostable fleet where runners spawn isolated PTY pods per agent, an "Autopilot" control agent feeds the next instruction with iteration caps and decision history, work is modeled as kanban tickets bound to pods, and a human can take over/hand back at any point. Go, ~2.2k★, 110 releases — the most production-leaning of the open candidates. **Generalization: coding-centric** (MR/PR tracking, runner pods aimed at code), so the goal→spec→business-task part would need to be added; but the runner/pod/board/Autopilot spine is exactly our shape.

**[builderz-labs/mission-control](https://github.com/builderz-labs/mission-control)** — Closest on the *control-plane + governance* axis: a six-column kanban (inbox→assigned→in-progress→review→quality-review→done), auto-dispatch with orchestration rules, the **Aegis** review system that *blocks completion without sign-off* (your adversarial gate), plus cron + outbound webhooks and realtime WS/SSE. **Generalization: fairly task-agnostic** (it's a dispatch/coordination dashboard with framework adapters, not hardwired to code), which is rare here. Caveat: alpha (v2.0.1), and "inline sub-agent spawning" is thin on runtime detail.

**[saltbo/agent-kanban](https://github.com/saltbo/agent-kanban)** — The cleanest open implementation of *exactly* the leader→board→worker loop: a leader agent breaks a goal into tasks on a Todo→In Progress→In Review→Done board, a daemon polls assigned tasks, sets up a git worktree, loads role-specific skills (architect/frontend/backend/reviewer), and spawns a worker per task; the leader reviews and merges, daemon auto-completes on merge. Tasks are YAML specs. **Generalization: coding-only** (worktrees, PRs) and review is collaborative not adversarial — but the daemon-spawns-scoped-worker-per-ticket mechanic is directly liftable.

**[zeroshot](https://github.com/the-open-engine/zeroshot)** — Best *spec+adversarial-review* engine: a conductor classifies task complexity, selects workers **and independent validators that do "blind validation" (never see worker context/code history)**, and iterates until all validators approve against explicit acceptance criteria. ~1.6k★, mature (v6). **Generalization: coding-leaning** (PR/ship modes, GitHub/Jira ingestion) and runs discrete tasks rather than continuously — but the blind-adversarial-validator pattern is the strongest in the catalog and worth porting wholesale.

**[APM (Agentic Project Management)](https://github.com/sdi2200262/agentic-project-management)** — Best articulation of the *manager/planner separation* you want: a **Planner** does discovery and decomposes requirements into Spec (what) / Plan (how organized) / Rules (how performed); a separate **Manager** assigns Tasks to Workers, reviews output, and maintains state via a memory + handover protocol across sessions; "APM Auto" spawns ephemeral subagents at runtime. ~2.3k★, active. **Generalization: relatively framework-neutral** (it's a methodology + prompts), though billed for software projects, no board, and user-mediated rather than continuous.

**[AXME](https://github.com/AxmeAI/axme)** — Best *durable + human-escalation* primitive: durable state orchestration with crash recovery, human approval gates, and a kill switch over an open protocol (AXP). Multi-language, production-focused. **Generalization: domain-agnostic substrate** — it's coordination plumbing, not a coding tool — making it a candidate to sit *underneath* a manager rather than be the manager.

Runners-up worth a direct look: **Miyabi** (GitHub issues as board + 7 coding *and 14 business* agents — the rare one explicitly reaching past code), **Overstory** (coordinator spawns Scout/Builder/Reviewer/Merger in worktrees — but **archived May 2026**, successor "Warren"), **Relevance AI** (the most credible *business-goal* Manager Agent with nested subagents and L1–L4 autonomy, but cloud-only SaaS), and **Microsoft Magentic-One** (Task Ledger / Progress Ledger + self-correction loop — a clean ledger pattern).

---

## 4. What genuinely does NOT exist off the shelf

Be skeptical: many projects *claim* "autonomous company / zero-human" but ship fixed role rosters, demos, or human-in-every-loop reality. The honest gaps our build must fill:

1. **Vague business goal → spec, generalized beyond coding.** Almost every planner that decomposes well is wired to *software* (issues, PRs, worktrees, test gates). The strong spec engines (zeroshot, Spec Kit, BMAD, takt, GPT Pilot) all assume "the deliverable is code." A manager that takes "grow newsletter signups" or "launch this product line" and produces an actionable spec + acceptance criteria is **not** an off-the-shelf capability. Relevance AI is the closest, and it's closed SaaS.

2. **Continuous operation *and* full goal-decomposition *and* dynamic spawning *and* adversarial review, in one system.** Every project has 2–3 of these; none robustly has all four. AgentsMesh has board+spawn+continuous+human-takeover but weak spec/adversarial-review; mission-control has board+review+triggers but thin spawning and is alpha; zeroshot has spec+adversarial-review but is discrete, not continuous; durable engines (Temporal/Hatchet/Restack/AXME) have continuity+triggers but no agent-aware planner. **The integration is the missing product.**

3. **True dynamic *hiring* — creating a new scoped worker *role* at runtime, not just routing among a predefined roster.** Most "dynamicSpawning: yes" entries spawn *instances of the same agent* (Devin's "team of Devins," Baton workers, agent-kanban worker-per-task) or *select from a fixed library* (AgentVerse recruiter, agency-orchestrator's 216 roles). Genuine runtime synthesis of a *new specialized worker with a scoped prompt/toolset/sandbox* for an emergent ticket is rare (Hephaestus' meta-agent builder and EvoMap's Evolution Circles claim it but are unproven).

4. **Adversarial review as a first-class, enforced gate over arbitrary work.** zeroshot's blind validators and mission-control's Aegis are the only convincing enforced gates; most "specWithReview: yes" are really "a reviewer agent exists" or "guardrails validate output." Nothing offers a configurable, non-skippable, *blind* adversarial reviewer that generalizes beyond diffs/tests.

5. **Human escalation as a designed control surface (not a chat fallback).** AXME (approval gates + kill switch), AgentsMesh (takeover/handback), and Relevance AI (low-confidence escalation) are the only serious treatments. A structured "the manager escalates *this decision* to *this human* with *this context* and resumes durably" is mostly absent.

6. **Sandbox-per-worker as the orchestration unit, decoupled from git/PRs.** The good sandbox stories (AgentsMesh pods, Background Agents on Modal/Daytona/Vercel, centaur K8s, E2B as substrate) exist, but the ones with real *manager* logic bind the sandbox to a code worktree/PR. A manager that provisions a scoped sandbox for a *non-code* ticket is unproven. (This is precisely Agent Orange's wheelhouse — your container-backed session runtime is the substrate these projects lack cleanly.)

7. **The full loop running unattended for days without degenerating.** LoopTroop (10h+ Ralph loops) and loki-mode (PRD→deploy) are the boldest claims, but at ~33★ / unknown maturity they're unvalidated. No catalog project demonstrably runs a *general* business goal to completion over long horizons with self-healing.

---

## 5. Patterns worth stealing

**Orchestrator loops & ledgers**
- **Task Ledger + Progress Ledger with self-correction** (Magentic-One): keep an explicit plan ledger and a separate progress ledger; on stall, revisit the plan and restart. This is a cleaner state model than free-form agent memory.
- **Plan/Execute/Express/Review (PEER)** (agentUniverse) and **RARV — reason/act/review/verify** (loki-mode): name and separate the phases so review is structurally unavoidable.
- **Poll-dispatch-reconcile daemon** (Baton): a Dispatcher controls concurrency; a Reconciler detects stale/hung runs. Pair with **stale-agent detection** (agent-kanban marks agents offline after 2h) for self-healing.
- **Deterministic / zero-LLM coordination** (Bernstein): spend no model tokens on orchestration bookkeeping; reserve the LLM for the work. Good for cost and reproducibility.
- **Wake/Work/Sleep checkpoint loop** (agx) and **Ralph loops** (Dex, LoopTroop, ralph-orchestrator): bounded iterate-until-done with checkpoints so long runs are resumable.

**Spec + adversarial-review workflows**
- **Blind adversarial validation** (zeroshot): validators never see the worker's context or history — they judge output against acceptance criteria only. The strongest anti-collusion review pattern in the catalog; port it.
- **Non-skippable review gate that blocks completion** (mission-control's Aegis; takt's mandatory review→fix routing): make "done" impossible without sign-off, and route findings *back* to a fix step, not forward.
- **Planner/Manager separation with Spec/Plan/Rules artifacts** (APM): discovery+decomposition (Planner) is a different role from assignment+oversight (Manager); persist Spec(what)/Plan(how organized)/Rules(how performed) as durable documents.
- **"Constitution"/living spec as persistent contract** (Spec Kit, Intent): a single source of truth that propagates changes to in-flight workers (Intent) rather than re-prompting.
- **Evidence/Reality gates** (flow-crew's "Reality-Gate"): require *evidence* of completion, not the agent's self-report.
- **Council scoring/voting** (LoopTroop): multiple model instances draft, score on a weighted rubric, and vote before committing a plan — cheap ensemble review at the planning stage.

**Dynamic hiring**
- **Recruiter/coordinator assembles a per-task team** (AgentVerse) and **role-as-skill-load** (agent-kanban: architect/frontend/backend/reviewer load different skills so a generic worker becomes a specialist per ticket). The latter is the most pragmatic path to "spawn a scoped worker" without synthesizing whole agents.
- **One-level subagent fan-out** (goose, omnigent, Background Agents' `spawn-task` with depth limits and per-repo guardrails): cap recursion depth explicitly to avoid runaway spawning.
- **Worktree/sandbox-per-worker + merge queue** (Overstory's FIFO merge queue + tiered conflict resolution; agent-kanban; AgentsMesh pods): isolate each worker, then reconcile via a queue with conflict tiers.

**Board / ticket models**
- **Kanban as the canonical state machine** (mission-control's 6 columns incl. a dedicated *quality-review* lane; AgentsMesh ticket↔pod binding; modimihir07's triage→todo→ready→in_progress→blocked→done). A *blocked* column + an explicit *review* column are the load-bearing additions over a naive 3-column board.
- **YAML/declarative task specs** (agent-kanban `ak apply -f task.yaml`, takt, Miyabi GitHub issues): tickets as version-controlled, machine-checkable specs.
- **Git-native coordination with no server** (GNAP: 4 JSON files in a repo): if you ever want auditability and "any agent that can push can participate," this is an elegant minimal protocol.
- **Follow-up-ticket creation** (Codegen: agents open new tickets for bugs/deps they discover): let workers *expand* the board, with the manager re-prioritizing — this is how BabyAGI's creation/prioritization loop generalizes to a board.

**Durable substrate & triggers (build on, don't rebuild)**
- Use a **durable execution engine** (Temporal / Hatchet / Trigger.dev / Restack / AXME) for crash-resistant continuous operation, exactly-once semantics, and native cron + webhook + signal triggers — rather than hand-rolling the daemon. AXME additionally gives human-approval gates and a kill switch as protocol primitives, which map directly onto your escalation requirement.
