<!--
Generated 2026-06-26 by an exhaustive discovery workflow (policy-as-code-landscape):
14 parallel search angles -> fetch + per-system extraction (lists exploded) ->
completeness-critic round -> synthesis. 212 distinct systems.
Companion docs: ARCHITECTURE.md (§6B/§6C), AGENTS_SELF_IMPROVING_LANDSCAPE.md,
AGENTS_MEMORY_HR_LANDSCAPE.md, AGENTS_ORCHESTRATION_LANDSCAPE.md, AGENTS_STACK_DECISION.md.
NOTE: maturity/star counts/"GA" claims are vendor/author-reported, NOT verified. Re-check primary sources.
-->

# Policy-as-Code & GitOps for Agents — Landscape & Design Guidance

## 1. Executive read

For **declarative, versioned agent/org config** mature prior art exists only to **steal from, not adopt**: GitOps engines (Argo CD, Flux), agent-config DSLs (PayPal's Agentic Pipeline DSL, gh-aw, Shaped), and the 12-Factor-Agents methodology all prove the "config-in-git, compiled to a pinned manifest" mechanism — but none models an *org operating policy* (staff/scope/subscriptions/pipelines/taxonomy), so the schema itself we **build**; best pattern donor is **GitHub Agentic Workflows (gh-aw)** (declarative Markdown+YAML → compiled `.lock.yml` the agent can't tamper with).

For **PR/approval-gated change** and **rollback** we simply **adopt** the substrate we already use — a git host's PR + branch-protection + `git revert` — augmented by an eval gate (see below); no product beats raw git here, and **HumanLayer** is the only clean library abstraction of the human-approval step itself.

For **canary/A-B of prompts-or-policy**, **steal** from feature-flag/experimentation platforms; the single best candidate is **GrowthBook** (OSS, self-hostable, progressive rollout + real A/B with guardrails, *and* an MCP server letting an agent propose flag changes through the same approval workflow — the closest existing analogue to our whole loop), with Argo Rollouts/Flux as the GitOps-native canary pattern.

For **an AI proposing its own config through a gate**, nothing mature is adoptable: the self-evolving-agent research (DSPy, EvoPrompt, AFlow, EvoAgentX, Darwin Gödel Machine) gates on an *automated metric with no human and no oracle*, while the governed examples (GrowthBook MCP, the EMNLP-2025 HITL paper, Particle41's guidance, Braintrust) gate human-or-eval changes but never let an agent rewrite a whole *org's* policy — so this is mostly **build**, stealing the gate plumbing.

For **policy-as-code guardrails**, **adopt** outright: **OPA + Conftest** (Rego, the canonical pre-merge config gate) for board validation, with Cedar/OpenFGA-style engines for action-time authorization; OPAL/OPA-Control-Plane already do git→bundle distribution we can reuse.

For **prompt versioning**, **adopt**: **promptfoo** for the git-diffable, CI-gating eval layer, plus a registry (Langfuse/Braintrust/Arthur) if we want tag-repoint rollback and built-in canary.

Net: the *mechanisms* for all seven needs exist and are battle-tested; the **novel surface we must build** is the board schema for a general (non-coding) agent org and the Consultant→PR→eval/human→canary loop that mutates it.

## 2. Full catalog (by category)

Legend: D=Declarative, G=Git-versioned, PR=PR/approval gate, RB=Rollback, AB=Canary/A-B, AIg=AI-proposes-via-gate, GP=General-purpose (y/n/code), SH=Self-host, Rel=Relevance. Y/N/? = yes/no/unknown.

### policy-as-code

| System | What it is | D | G | PR | RB | AB | AIg | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Open Policy Agent (OPA)/Rego | CNCF general policy engine, Rego DSL, decision point outside model loop | Y | Y | ? | ? | N | N | y | Y | CNCF graduated | H |
| Cedar | AWS open analyzable authz language, forbid-wins/default-deny | Y | Y | ? | Y | ? | N | y | Y | AWS-backed | H |
| Casbin | Cross-lang ACL/RBAC/ABAC lib via model+policy files | Y | Y | ? | Y | ? | N | y | Y | ~17k★ | M |
| SpiceDB | Zanzibar ReBAC DB, declarative schema | Y | Y | ? | ? | ? | N | y | Y | AuthZed active | M |
| Conftest | Rego tests against config files; canonical pre-merge gate | Y | Y | Y | N | N | N | y | Y | OPA subproject | H |
| Regal | Rego linter/LSP, 60+ rules; CI policy-quality gate | Y | Y | Y | N | N | N | y | Y | Styra/OPA | H |
| gator CLI | Gatekeeper offline Constraint test CLI | Y | Y | Y | N | N | N | n | Y | active | M |
| github-action-opa-rego-test | GH Action running Rego unit tests as merge gate | Y | Y | Y | N | N | N | y | Y | community | H |
| RuleHub | PaC framework for AI/ML pipelines; Rego+Kyverno, signed bundles, compliance maps | Y | Y | Y | ? | N | N | y | Y | early (~5★) | H |
| Agent OS | Safety kernel governing agents via POSIX-like primitives+policy engine | Y | ? | ? | ? | ? | ? | y | Y | unknown | H |
| Invariant Guardrails | Rule-based guardrails-as-code + trace analysis | Y | Y | ? | ? | ? | N | y | Y | Invariant Labs | H |
| Regulus | EU/UK compliance plane, 10 regs as runtime ADK profiles | Y | Y | ? | ? | ? | N | y | Y | Neul Labs | H |
| Permit.io | Authz platform on OPA+OPAL; PaC+GitOps, MCP gateway, agent identities | Y | Y | Y | Y | ? | N | y | Y | VC-funded | M |
| Oso | Authz-as-code via Polar DSL; extending to agent prompts/tool-calls | Y | Y | ? | ? | ? | N | y | ? | VC-backed | M |
| Aserto (Topaz) | OPA+ReBAC, policy→signed OCI→edge authorizers; hosted winding down to OSS Topaz | Y | Y | ? | Y | ? | N | n | Y | pivot to OSS | L |
| Portkey (gateway) | OSS AI gateway, 60+ configurable guardrails | Y | ? | N | ? | ? | N | y | Y | active | M |
| HashiCorp Sentinel | PaC gating Terraform applies; versioned guardrail | Y | Y | Y | ? | N | N | y | N | commercial | M |
| Amazon Bedrock AgentCore Policy | Managed Cedar authz at gateway, LOG_ONLY vs ENFORCE | Y | Y | ? | Y | Y | N | y | N | GA 2026-03 | H |
| Cedar (language site) | OSS analyzable authz language underpinning above | Y | Y | ? | ? | ? | N | y | Y | CNCF | H |
| AWS Verified Permissions | Managed Cedar service, sub-ms eval | Y | ? | N | ? | N | N | y | N | AWS managed | L |
| Ory Keto | Zanzibar permission server, relations as data | Y | ? | N | ? | N | N | y | Y | active | L |
| ArGen | Research: align LLMs to machine-readable rules, OPA-inspired enforcement | Y | ? | ? | ? | ? | N | y | ? | paper 2025 | M |
| agenttier | K8s runtime, one agent per Pod+PVC, default-deny NetworkPolicy | Y | ? | ? | ? | N | N | code | Y | unknown | M |

### agent-governance-audit

| System | What it is | D | G | PR | RB | AB | AIg | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|---|
| TRACE | Signed EAT/JWT decision receipts for agent actions | ? | Y | ? | N | N | N | y | ? | spec | H |
| Agent Manifest | Signs 10 deploy artifacts + HW attestation for provenance | Y | ? | ? | N | N | N | y | ? | project | H |
| Agent Governance Toolkit (MS) | Policy kernel + execution rings + Merkle audit; LangChain/CrewAI/AutoGen | Y | ? | ? | ? | ? | N | y | Y | Microsoft | H |
| ScopeBlind protect-mcp | Gateway enforcing Cedar on MCP traffic, Ed25519-signed decisions | Y | ? | ? | ? | N | N | y | Y | early | H |
| Survey of Self-Evolving Agents (2507.21046) | Taxonomy: what/when/how/where to evolve; frames gated self-mod | ? | N | N | N | N | ? | y | N | arXiv survey | H |
| EMNLP-2025 HITL self-improving agent | Agent updates own behavior at test time, human reviews before effect | ? | ? | Y | ? | ? | Y | y | ? | paper | H |
| AgentMesh | Zero-trust trust layer, Ed25519 identity between agents | ? | ? | ? | ? | ? | ? | y | Y | unknown | H |
| Towards Automated Governance DSL (2510.14465) | Declarative DSL for human+agent OSS project governance: who/approval/process | Y | ? | Y | ? | N | ? | code | ? | paper Oct'25 | H |
| Security in Age of AI Teammates (2601.00477) | Empirical study of 33.6k agent PRs, merge/reject/latency signals | N | Y | Y | ? | N | Y | code | ? | preprint Jan'26 | H |
| GitHub: Reviewing Agent PRs (playbook) | Approval-gate discipline: least-priv, sanitize untrusted input, split analysis/exec | N | Y | Y | ? | N | Y | code | ? | blog 2025 | H |
| Feng et al. UI-as-regulation (2512.00742) | UI as governance infra: HITL gates, audit, policy enforcement | N | N | Y | ? | N | N | y | ? | paper | H |
| Organizational Control Layer (OCL/AiMai, 2606.04306) | Intercepts agent actions at exec boundary → role/constraint/audit/escalate; approve/revise/block/escalate | ? | N | Y | N | N | N | n | Y | paper+code; unsafe 88%→~0% | H |
| GitHub Copilot Code Review | AI first-pass PR review before human | ? | Y | Y | ? | ? | N | code | N | GA | M |
| Fairwinds Insights | Multi-stage OPA/Polaris K8s policy enforcement | Y | Y | Y | ? | ? | N | n | N | commercial | M |
| Galileo AI | Managed obs+eval+runtime guardrails (Luna-2) | ? | ? | ? | ? | ? | N | y | Y | enterprise | M |
| loki-mode | Autonomous SDLC orchestrator, 9 gates, blind 3-reviewer | ? | ? | Y | ? | N | N | code | Y | BUSL | M |
| Fusion | Multi-node orchestrator, kanban, plan-review-execute gates, worktrees | ? | Y | Y | ? | N | N | code | ? | unknown | M |
| Dex | Ralph orchestrator, human-gated planning, multi-reviewer | ? | ? | Y | ? | N | N | code | Y | unknown | M |
| ivy-tendril | Coding orchestrator, verification gates, HITL checkpoints, self-improving memory | ? | ? | Y | ? | N | ? | code | Y | unknown | M |
| LangSmith | LangChain obs/eval, variant compare, pytest CI gate | ? | ? | Y | ? | Y | N | y | ? | company | M |
| Arize Phoenix | OTel tracing + offline/online evals | ? | N | ? | ? | N | N | y | Y | 9k★ | L/M |
| Project Ariadne (paper) | Structural causal audit of agent reasoning faithfulness | N | N | N | N | N | N | y | ? | paper | M |
| AgentField | DID-based agent backends with configurable policy boundaries | Y | ? | ? | ? | ? | ? | y | Y | unknown | M |
| Comprehensive Survey Self-Evolving (2508.07407) | Survey w/ safety+governance section | N | N | N | N | ? | N | y | N | paper | M |
| Magentic UI (MS) | Interactive HITL/plan-approval UI patterns | N | N | N | ? | ? | N | y | ? | research | L |
| AgentOps | Agent obs/tracing/cost/audit | N | N | N | N | N | N | y | Y | 4k★ | L |
| Traceloop/OpenLLMetry | OTel LLM reliability, eval gates on PRs | N | N | Y | ? | N | N | y | Y | ~5k★, ServiceNow | L |
| Credo AI | Enterprise AI governance/risk/compliance mapping | ? | ? | ? | N | N | N | y | N | established | L |
| Hephaestus | "Open Agent OS", meta-agent builder, A2A routing, governed memory | ? | ? | Y | ? | N | ? | code | ? | unknown | H |
| Agentic Radar | Security scanner for agentic workflows (CVE/OWASP) | ? | ? | ? | ? | N | N | y | Y | unknown | L |
| Gatekeeper Policy Manager | Web UI for Gatekeeper constraints/audit | Y | ? | N | N | N | N | n | Y | SIGHUP | L |
| Lakera Guard | Real-time prompt-injection/guardrail API (now Check Point) | ? | N | N | ? | N | N | y | Y | acquired | L |
| ChatGPT Agent/Operator | Web-automation agent, UI confirmations | N | N | N | ? | ? | N | y | N | OpenAI | L |
| Brood-box | CLI running agents in microVMs w/ egress control | ? | ? | N | N | N | N | y | Y | Stacklok | L |

### control-plane-reconciliation

| System | What it is | D | G | PR | RB | AB | AIg | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Argo CD | GitOps CD for K8s; git = single source of truth, continuous reconcile | Y | Y | N | Y | Y | N | n | Y | CNCF graduated | H |
| OPAL | Real-time control plane watching git policy repo, live-pushes to OPA | Y | Y | N | Y | ? | N | y | Y | Permit.io ~5k★ | H |
| OPA Control Plane (OCP) | Builds versioned policy bundles from many git repos, distributes via object store | Y | Y | ? | ? | N | N | y | Y | OPA community | H |
| cMCP (Confidential MCP) | MCP gateway in TEE; policy bundle measured into HW attestation | Y | ? | ? | ? | N | N | y | Y | agentrust-io | H |
| systemprompt-template | Self-hosted governance layer in front of Claude Code/MCP | Y | Y | ? | ? | ? | ? | n | Y | small/early | H |
| systemprompt-core | MCP governance runtime: auth/rate-limit/policy | Y | Y | ? | ? | ? | ? | y | Y | early | H |
| HumanLayer | SDK for HITL approval gate before high-stakes agent actions | ? | ? | Y | ? | ? | Y | y | ? | YC-backed | H |
| MartinLoop | Control plane for coding agents: budget stops, verifier gates, rollback evidence, run receipts | ? | ? | Y | Y | N | N | code | ? | unknown | H |
| Cordum | Agent control plane for lifecycle + policy enforcement | ? | ? | ? | ? | ? | ? | ? | Y | early | H |
| Topaz | OSS app authz: OPA decisioning + ReBAC directory at edge | Y | Y | N | ? | ? | N | y | Y | Aserto ~1k★ | M |
| Rönd | Sidecar enforcing OPA for HTTP APIs | Y | Y | N | ? | N | N | y | Y | Mia-Platform | M |
| IBM mcp-context-forge | Enterprise MCP gateway, context guardrails | Y | ? | ? | ? | ? | N | y | Y | IBM OSS | M |
| Gate22 | MCP gateway, RBAC + audit on tool usage | Y | ? | ? | ? | N | N | y | Y | Aipotheosis | M |
| LiteLLM | LLM proxy: budgets, rate-limit, central config | Y | ? | N | ? | ? | N | y | Y | very active | L |
| Dagger (self-healing CI) | Containerized CI w/ AI-fix blueprint, reviewed changes | Y | ? | Y | ? | N | ? | code | Y | active | M |
| LangGraph | Graph agent orchestration, HITL interrupts, checkpoints | N | N | N | ? | N | N | y | Y | ~10k★ | M |
| Floom | AI gateway + pipeline marketplace, K8s-style declarative | Y | ? | ? | ? | ? | N | y | Y | unknown | M |
| toryo | Orchestrator: trust delegation, quality ratchet via commit/revert, Ralph retries | ? | Y | ? | Y | N | N | code | Y | unknown | M |
| Control Plane as a Tool (2505.06817) | Decouples orchestration from reasoning; single callable interface | N | N | N | N | N | N | y | N | paper | M |
| TrueFoundry | K8s-native LLM serving infra | ? | ? | ? | ? | ? | N | y | Y | infra | L |
| swarm-protocol | Headless MCP coordination: claim work, conflicts, heartbeat | ? | ? | N | ? | N | N | code | Y | unknown | L |
| Apache Airflow/Temporal | Workflow orchestration (DAGs/durable exec) | Y | Y | ? | ? | N | N | y | Y | mature | L |
| LangGraph Cloud | Managed LangGraph w/ visual debug, chain-level canary | Y | ? | N | ? | Y | N | y | ? | GA | L |
| Orchestration of MAS (survey) | Unified MAS architecture survey (MCP+A2A) | ? | N | N | ? | N | N | y | ? | paper | M |

### prompt-versioning

| System | What it is | D | G | PR | RB | AB | AIg | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|---|
| promptfoo | git-diffable YAML eval/red-team CLI; CI gate comparing prompts/models | Y | Y | Y | ? | y(varies) | N | y | Y | popular OSS | H |
| Langfuse | OSS LLM eng: prompt mgmt/versioning, datasets, experiments, evals | N | N | N | Y | Y | N | y | Y | widely used | H |
| PromptLayer | Prompt CMS + monitoring, versioning outside code | ? | N | ? | Y | Y | N | y | N | commercial | H |
| Braintrust | Eval-first LLMOps: content-addr versions, env promotion, canary/blue-green, CI gate | Y | Y/? | Y | Y | Y | Y | y | ? | enterprise customers | H |
| Arthur (Engine) | Versioning w/ rich metadata, env-tag promotion, rollback by repoint | Y | ? | Y | Y | Y | N | y | ? | commercial | H |
| agenta | OSS LLMOps: prompt versioning, eval, A/B of variants | Y | N | N | Y | Y | N | y | Y | ~2k★ | H |
| Prompt Registry pattern (blog) | Central registry as source of truth; per-env allowed versions, canary/blue-green | Y | Y | Y | Y | Y | ? | y | ? | conceptual | H |
| Hypersigil | OSS prompt lifecycle mgmt + gateway, web UI | ? | N | ? | ? | ? | N | y | Y | unknown | M |
| MLflow Model Registry | Versioning + stage transitions + promotion gates | ? | N | Y | Y | ? | N | n | Y | mature | M |
| MLflow GenAI evaluate | 50+ judges, regression datasets, registry-backed | N | N | N | Y | Y | N | y | Y | mature | M |
| W&B Weave | Artifact-versioned prompts, experiment tracking, sweeps | ? | ? | ? | ? | ? | N | y | Y | established | M |
| DSPy | Compiles declarative LM signatures; optimizers tune prompts to a metric | Y | ? | N | ? | N | Y | y | Y | widely used | M |
| TextGrad | "Differentiation via text" optimizing prompts via NL feedback | Y | ? | N | ? | N | Y | y | Y | paper+OSS | M |
| EvoPrompt | LLM+evolutionary algos mutate/select prompts on dev-set fitness | N | ? | N | ? | N | Y | y | Y | paper+repo | M |
| PromptAgent | MCTS over prompt edits vs metric | N | ? | N | ? | N | Y | y | Y | paper+repo | M |
| OPRO | LLM-as-optimizer proposes prompt candidates, score=gate | N | ? | N | ? | N | Y | y | Y | DeepMind | L |
| agent-opt | Prompt-refinement engine, 6 algos, LLM-judge | ? | ? | N | ? | Y | N | y | Y | unknown | M |
| Lunary | Obs + prompt templating/versioning | ? | N | N | ? | ? | N | y | Y | ~1k★ | M |
| Kiln AI | Agents/eval/RAG/fine-tune w/ dataset+eval versioning | ? | ? | N | ? | ? | N | y | Y | unknown | L |
| Dify | OSS LLM app framework, visual prompt orchestration, exportable DSL | Y | ? | N | ? | ? | N | y | Y | unknown | L |
| Microsoft Prompt flow | YAML DAG flows, prompt variants, batch eval (deprecated 2026) | Y | Y | N | ? | N | N | code | Y | deprecated | L |
| Humanloop | Prompt/eval mgmt (acquired by Anthropic, sunset) | ? | ? | ? | ? | ? | N | y | ? | sunset | L |

### canary-experimentation

| System | What it is | D | G | PR | RB | AB | AIg | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|---|
| GrowthBook | OSS warehouse-native flags + A/B + analytics; MCP server lets agents propose flag changes through same approval+audit | Y | N(db revisions) | Y | Y | Y | Y | y | Y | ~7.8k★ | H |
| Statsig | Flags + experimentation, traffic splits for canary (OpenAI-acquired) | ? | ? | N | Y | Y | N | y | ? | acquired | H |
| LaunchDarkly | Flag service, 1→5→25→full traffic splits | ? | ? | N | Y | Y | N | y | ? | established | H |
| Unleash | OSS feature mgmt: canary, kill switches, approval-gated change requests | Y | ? | Y | Y | Y | N | y | Y | ~13.6k★ | M |
| ConfigCat | Hosted flags: % rollout, targeting, kill-switch rollback | Y | ? | ? | Y | Y | N | y | N | SaaS | M |
| PostHog | Analytics + LLM obs + flags + A/B w/ stat significance | ? | ? | N | ? | Y | N | y | Y | OSS | M |
| Tonic Validate | RAG eval metrics as GH Action gate | Y | Y | Y | ? | N | N | n | Y | OSS | H |
| Vercel agent-eval | A/B test coding agents, pass-rate dashboards | ? | ? | ? | ? | Y | N | code | Y | Vercel Labs | H |
| Braintrust (eval SDK) | Offline evals + online experiments | N | ? | N | ? | Y | N | y | N | well-funded | M |
| LangWatch | LLMOps + DSPy optimization studio | ? | N | N | ? | Y | N | y | Y | ~1k★ | M |
| Giskard | ML/LLM testing for bias/hallucination; CI gate | ? | N | ? | N | N | N | y | Y | ~4k★ | M |
| Deepchecks | Continuous ML validation; regression gate | ? | N | ? | N | N | N | y | Y | ~3k★ | M |
| Evidently | Eval/test/monitor ML+LLM; test suites gate releases | ? | N | ? | N | N | N | y | Y | ~5k★ | M |
| EvalView | Golden-baseline diffing regression for agents | ? | ? | ? | ? | Y | N | y | ? | unknown | M |
| Portkey (gateway) | AI gateway w/ caching, LB, config-driven A/B routing | Y | ? | N | ? | Y | N | y | ? | commercial+OSS | L |
| AgentGym | Interactive learning env to evolve/eval agents | ? | N | N | N | ? | N | y | Y | research | L |

### agent-config-mgmt

| System | What it is | D | G | PR | RB | AB | AIg | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|---|
| 12-Factor Agents | Methodology: externalized, versioned, stateless agent config | Y | Y | N | ? | N | N | y | ? | popular repo | M |
| Shaped (Cloud) | Managed retrieval/ranking; engines/queries/prompts as versioned YAML, CI/CD deploy | Y | Y | Y | Y | Y | ? | y | ? | SOC2, customers | H |
| skillfold | Config language + compiler: declarative YAML → agent skills for Claude/Cursor/Codex etc. | Y | Y | ? | ? | N | N | code | Y | unknown | H |
| PayPal Agentic Pipeline DSL (2512.19769) | Declarative DSL, DAGs→JSON IR, runs Java/Python/Go; prod at PayPal | Y | Y | ? | ? | Y | N | y | ? | paper+prod | H |
| EvoAgentX | Evolves agentic workflow configs vs eval metrics | Y | ? | N | ? | N | Y | y | Y | paper+repo | M |
| AFlow | MCTS over code-workflow structures, eval=acceptance | Y/? | ? | N | ? | N | Y | y | Y | paper+MetaGPT | M |
| EvoFlow | Evolves population of heterogeneous workflows | ? | N | N | ? | Y | N | y | ? | paper | M |
| AgentSquare | Searches modular agent architecture space | ? | N | N | ? | Y | N | y | ? | paper | M |
| CrewAI | OSS multi-agent: agents/tasks declared in YAML | Y | Y | N | ? | ? | N | y | Y | ~54.5k★ | M |
| Flock | Declarative MAS, blackboard arch, typed contracts | Y | ? | N | ? | ? | N | y | Y | unknown | M |
| tutti | Multi-agent CLI: config workflows, worktree isolation, typed artifacts | Y | Y | N | ? | N | N | code | Y | unknown | M |
| Meta Context Engineering (paper) | Meta-agent evolves base agents' context-eng skills | ? | N | N | ? | N | Y | ? | ? | paper | M |
| DVC | Git-like VC for ML data/models; rollback-able | Y | Y | Y | Y | N | N | n | Y | mature | M |
| Konstraint | Generates Gatekeeper Constraints from Rego, keeps declarative | Y | Y | N | Y | N | N | n | Y | community | M |
| Open Policy Containers (OPCR) | Packages Rego as signed OCI images: push/pull/tag/version | Y | Y | N | Y | N | N | y | Y | Aserto | H |
| Vellum | Visual workflow builder, versioned workflows, evals | ? | N | ? | Y | Y | N | y | N | commercial | M |
| Voyager | Embodied agent grows reusable code-skill library | N | N | N | N | N | N | n | Y | ~6k★ | L/M |
| Alita | Self-evolves by generating own tools/MCPs | N | ? | N | ? | N | ? | y | Y | paper+repo | L |
| MetaAgent | Builds MAS via LLM-generated FSM specs | Y | ? | N | ? | N | ? | y | Y | paper+repo | L |
| Flagsmith | OSS flags/remote config, change requests, audit | Y | N | Y | Y | Y | N | n | Y | VC-backed | L |
| Langflow | OSS visual agent/RAG builder, flows→JSON | Y | N | N | ? | N | N | y | Y | ~138k★ | L |
| Policy Hub CLI | CLI registry to pull reusable Rego | Y | Y | N | N | N | N | y | ? | low activity | L |
| Versioning/Rollback 4-layer (Medium) | Concept: 4 independently-versioned agent layers in composite version ID | Y | ? | Y | Y | Y | ? | y | ? | blog concept | H |

### gitops-for-agents

| System | What it is | D | G | PR | RB | AB | AIg | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|---|
| GitHub Agentic Workflows (gh-aw) | Declarative MD+YAML in `.github/workflows` → compiled `.lock.yml`; frontmatter declares triggers/perms/tools + "safe-outputs" policy agent can't influence | Y | Y | Y | ? | ? | ? | code | ? | GH+MS preview | H |
| gnap (Git-Native Agent Protocol) | Coordinates agents via shared git repo as task board, no central orchestrator | Y | Y | N | ? | N | N | code | Y | unknown | H |
| Particle41 — GitOps when agents commit (guidance) | Pattern: pre-commit validation, risk-tiered approval, agent-documented PRs, auto-rollback on metric degradation, velocity limits | Y | Y | Y | Y | Y | Y | ? | ? | vendor article | H |
| Flux CD | CNCF GitOps + progressive delivery; whole desired state in git | Y | Y | Y | Y | Y | N | n | Y | CNCF graduated | M |
| Nx Self-Healing CI | Agent detects CI failures, proposes fixes for approval (data-plane) | ? | ? | Y | ? | N | N | code | ? | commercial | H |
| Datadog Bits AI Dev Agent | Diagnoses failures, opens fix PRs, human review | ? | ? | Y | ? | N | N | code | N | commercial | H |
| Gitar | Build-failure-fix agent → fix PRs | ? | ? | Y | ? | N | N | code | ? | commercial | H |
| GitHub Copilot Coding Agent | Autonomous code changes → PRs requiring review | N | N | Y | ? | N | N | code | N | GA | M |
| GitLab Duo Agent (Fix CI/CD) | Fixes CI/CD, proposes MRs gated by review | N | N | Y | ? | N | N | code | Y | active | M |
| Amazon Q Developer | Patches → PRs subject to approval | N | N | Y | ? | N | N | code | N | commercial | M |
| Google Gemini CLI Action | GH Action wrapping Gemini CLI for agentic CI tasks | ? | ? | ? | ? | N | N | code | Y | OSS Action | M |
| aeon | Unattended agent on GH Actions, 90+ skills, quality scoring, self-healing | ? | Y | ? | ? | N | ? | ? | Y | unknown | M |

### runbook-sop-as-code

| System | What it is | D | G | PR | RB | AB | AIg | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|---|
| MetaGPT | Encodes SOPs into role-based agents (software dev) | Y | N | N | N | N | N | n | Y | ~40k★ | M |
| Copilot SDK (custom review instructions) | Codifies team review checklist as executable rules | ? | ? | Y | ? | ? | N | code | ? | GH SDK | M |
| tmux-ide | tmux IDE w/ declarative ide.yml layouts, agent-team templates | Y | Y | N | ? | N | N | code | Y | low | L |

### other (research / adjacent)

| System | What it is | D | G | PR | RB | AB | AIg | GP | SH | Maturity | Rel |
|---|---|---|---|---|---|---|---|---|---|---|---|
| Gatekeeper | K8s admission webhook enforcing OPA constraints + audit | Y | Y | N | Y | Y | N | n | Y | CNCF ~3.7k★ | H |
| Spacelift | IaC/GitOps orchestration w/ OPA plan/approve/push gates | Y | Y | Y | Y | ? | N | n | N | commercial | M |
| Darwin Gödel Machine | Population evolution; agents propose self-code-mods, selected by benchmark | N | N | N | ? | Y | N | code | ? | paper | M |
| AlphaEvolve | LLM-mutated programs evaluated vs scoring fn | N | N | N | ? | Y | N | code | ? | DeepMind | M |
| Gödel Agent | Self-referential agent rewrites own logic | N | N | N | ? | N | N | y | ? | paper | M |
| CORAL | Long-running MAS self-evolving via shared memory, heartbeat HITL | N | N | N | ? | N | Y | ? | ? | paper | M |
| AutoRefine | Extracts reusable expertise from trajectories | N | N | N | ? | N | N | ? | ? | paper | M |
| Self-Evolving-Agents repo | Survey companion taxonomy | N | N | N | N | N | N | gen | ? | repo | M |
| SEW | Self-evolving workflows for code-gen | ? | N | N | ? | ? | N | code | ? | paper | L |
| AutoGen | MS multi-agent conversation framework | Y | ? | N | ? | N | N | y | Y | very high★ | L |
| Semantic Kernel | MS model-agnostic agent SDK + process framework | Y | ? | N | N | N | N | y | Y | ~28k★ | L |
| Google ADK | Google agent framework, multi-lang, eval/obs | Y | ? | N | ? | N | N | y | Y | GA | L |
| Pydantic AI | Typed agent framework, HITL tool approval, YAML/JSON agents | Y | ? | N | ? | ? | N | y | Y | widely adopted | L |
| Dapr Agents | Durable multi-agent on Dapr/K8s | partial | ? | N | ? | ? | N | y | Y | CNCF | L |
| MetaGPT (Foundation) | Multi-agent SOP framework | Y | ? | N | ? | N | N | y | Y | ~40k★ | M |
| MASLab/MAS-GPT | Codebase + method to generate MAS configs | ? | ? | N | ? | N | ? | y | Y | paper+repo | L |
| Reflexion | Verbal RL, self-feedback in episodic memory | N | N | N | N | N | N | y | Y | paper+OSS | L |
| Mem0 | Long-term memory layer that evolves over time | N | N | N | ? | N | N | y | Y | commercial OSS | L |
| Generative Agents | Self-updating memory stream + reflection | N | N | N | N | N | N | n | Y | ~18k★ | L |
| The AI Scientist | Automated research pipeline w/ auto peer-review gate | N | N | N | N | N | N | n | Y | ~9k★ | L |
| R-Zero | Self-evolving reasoning, weight-level | N | N | N | ? | N | N | y | Y | paper+repo | L |
| STaR | Bootstraps reasoning, fine-tunes on correct rationales | N | N | N | ? | N | N | y | ? | paper+repo | L |
| ToolLLM | Trains LLMs on 16k+ APIs | N | N | N | N | N | N | y | Y | paper+OSS | L |
| OpenHands | OSS software-dev agent platform (layer-1 runtime) | Y | ? | ? | ? | N | N | code | Y | very high★ | L |
| SWE-bench | Benchmark (the gate side) | N | ? | N | N | N | N | code | Y | widely used | L |
| Repairnator | Bot patches CI failures → PRs | ? | ? | Y | ? | N | N | code | Y | research 2019 | L |
| R-HERO | Automated program repair w/ continual learning | ? | ? | ? | ? | N | N | code | ? | research | L |
| Opik | OSS eval+obs, LLM judges, CI | N | ? | Y | ? | ? | N | y | Y | Comet | M |
| Inspect AI | UK AISI eval framework, evals as versionable code | Y | Y | ? | ? | N | N | y | Y | gov-backed | M |
| OpenAI Evals | Eval framework, registry files | Y | Y | ? | ? | N | N | y | Y | OpenAI | L |
| DeepEval | "pytest for LLMs", CI gate | N | Y | Y | ? | N | N | y | Y | popular | M |
| Helicone | OSS LLM gateway/obs | ? | N | N | ? | ? | N | y | Y | active | L |
| Agent-kanban/multica | Agent-first kanban, leader-worker, crypto identity | ? | ? | ? | ? | N | N | code | Y | unknown | L |
| MCP (protocol) | Standard agent↔tool interface, schema/access/audit | N | N | N | N | N | N | y | ? | emerging | M |
| LangChain/LangChain4j | Imperative agent framework (prior art) | N | N | ? | ? | N | N | y | Y | widely used | L |
| NVIDIA NeMo Agent Toolkit | Microservice agent suite | N | N | ? | ? | N | N | y | ? | vendor | L |
| AutoGPT | Recursive self-prompting agent | N | N | N | N | N | N | y | Y | popular | L |
| Claude Code / Cursor / GitHub Copilot | Coding agents w/ runtime approval prompts | N/? | N | N | ? | ? | N | code | N | vendor | L |

## 3. Buy vs steal vs build, need by need

| Need | Verdict | Best candidate | One-line justification |
|---|---|---|---|
| Declarative versioned agent/org config | **Build** (steal schema-pattern) | gh-aw / PayPal DSL / Argo CD | The "config-in-git compiled to a tamper-proof pinned manifest" mechanism is proven; no one models a non-code *org operating policy*, so the schema is ours. |
| PR / approval-gated change | **Buy/adopt** (the substrate) | GitHub/GitLab PRs + branch protection; HumanLayer for the gate abstraction | PRs, required reviewers, and CODEOWNERS already are the gate; only the human-approval-as-API wrapper is worth a library. |
| Rollback | **Buy/adopt** | `git revert` (+ Argo CD / tag-repoint) | Our board *is* git; revert-to-SHA is free and already what prompt registries reimplement. |
| Canary / A-B of prompts-or-policy | **Steal** | GrowthBook (or Argo Rollouts/Flux for git-native) | Feature-flag % rollouts + guardrails map directly onto board *branches*; GrowthBook even gates agent-proposed changes. |
| AI proposes own config via gate | **Build** (steal plumbing) | GrowthBook MCP + EMNLP-2025 HITL pattern | Only GrowthBook actually routes agent-proposed config through a human/audit gate, but for flags not org policy; the Consultant→PR loop is ours. |
| Policy-as-code guardrails | **Buy/adopt** | OPA + Conftest (Cedar for action-time) | Rego over JSON/YAML is the canonical pre-merge config gate; Cedar covers runtime tool authorization. |
| Prompt versioning | **Buy/adopt** | promptfoo (+ Langfuse/Braintrust registry) | git-diffable YAML evals running as a CI gate is exactly our prompt-fragment story; add a registry only if we want non-git rollback/canary. |

## 4. Patterns worth stealing

**How prompt-management tools version / rollback / diff.** Braintrust and Arthur both converge on the same shape: a *content-addressable version ID*, rich metadata bound to each version (author, timestamp, rationale, eval results, *full execution context* — model, params, tools, RAG), promotion across an environment hierarchy (dev→staging→prod) by **moving a tag**, and rollback = repointing that tag. The N. Raman "4-layer" piece pushes this further: never version the prompt alone — pin a *composite* version ID across Agent-Logic, Prompt-&-Policy, Model-Runtime, and Tool-API layers. **Steal:** our board commit SHA is already the content-addressable version; we should attach the eval score + score-source + environment context to each commit, and treat a routing decision's "board SHA" as a composite pin, not just a prompt pin. promptfoo's lesson is narrower but vital: keep the eval suite itself as git-diffable YAML so a board PR and its eval changes review together.

**How feature-flag/experimentation platforms do canary/A-B → maps to policy branches.** GrowthBook, Statsig, LaunchDarkly, Unleash, ConfigCat all decouple *release from deployment*: ship a variant behind a flag, ramp traffic 1%→5%→25%→100% with automatic guardrail metrics and an instant kill-switch that needs no redeploy. **Map:** a board *branch* is a flag value; "% of routing decisions pinned to branch SHA vs main SHA" is the traffic split; the kill-switch is repointing the active pointer back to main's SHA (a revert). Argo Rollouts/Flux show the git-native variant: canary state itself lives in git and is reconciled. The honest caveat (below): all of these assume a low-latency, statistically-clean metric per request; we have a sparse, delayed, noisy human score.

**How policy-as-code engines separate policy from execution.** OPA/Cedar's core move is a **Policy Decision Point outside the model loop**: the agent *proposes* an action, an independent engine evaluates declarative rules (Cedar: no loops/state/side-effects, default-deny, forbid-wins) and returns allow/deny, with the decision logged/signed (ScopeBlind, TRACE). OPAL and OPA Control Plane add the distribution half — watch a git policy repo, build versioned bundles, push to enforcement points. OCL/AiMai (2606.04306) is the agent-native version: intercept the candidate action at the execution boundary and return approve/revise/block/escalate (claimed unsafe-execution 88%→~0%). **Steal:** keep the board (policy) strictly separable from the workers (execution); routing/guardrail decisions consult a pinned board SHA the worker cannot mutate — exactly gh-aw's "safe-outputs the agent can't influence."

**How any system lets an agent propose config through a human/eval gate.** Three families, only one of which keeps a *human* in the loop:
- *Real product, governed:* **GrowthBook** — agents create/modify flags via the MCP server, and those proposals pass through the **same approval workflow + audit log** as human changes. This is the single closest existing analogue to our Consultant→gate loop.
- *Guidance/research:* Particle41 (risk-tiered approval + agent-documented PR reasoning + auto-rollback on metric degradation), the EMNLP-2025 HITL paper (agent updates own behavior, human guides before it takes effect), and the GitHub "review agent PRs" playbook (separate analysis from execution, human gate for prod).
- *Automated-gate only (no human, no oracle):* DSPy, EvoPrompt, PromptAgent, OPRO, AFlow, EvoAgentX, Darwin Gödel Machine — the agent proposes config and a *metric* selects survivors. Powerful for the proposal mechanics; useless as a governance gate.

**Steal:** GrowthBook's "agent and human pass through identical gate + audit" invariant, plus Particle41's risk tiers (cheap changes auto-merge on eval pass; high-blast-radius changes require human) — and borrow the *proposal* search techniques from the evolutionary literature while replacing their metric-only gate with our eval±human gate.

## 5. Traps the prior art already hit

**Over-declaring kills emergence.** Cedar deliberately removes loops/state to stay analyzable; Airflow/Temporal and the imperative frameworks (LangChain, AutoGPT) sit at the other extreme. The PayPal DSL and CrewAI show that over-specifying a DAG/role graph freezes behavior — every change becomes a redeploy. Our board should declare *scope and subscriptions*, not script the conversation; if the board encodes too much, the org stops adapting and the Consultant just churns config. Watch the line between "policy" (declare) and "behavior" (let emerge).

**Config drift.** The reconciliation systems (Argo CD, Flux, OPAL, OPA Control Plane) exist *because* distributed enforcement points silently diverge from git. Aserto/Topaz push signed OCI bundles precisely to guarantee what's running equals what's committed. Lesson: every routing/guardrail decision must cite the board SHA it actually used, and a reconciler must prove live workers are reading the intended SHA — otherwise "pinned to a commit" is fiction.

**Review bottlenecks under agent commit volume.** The Notre Dame study (33,596 agent PRs) and Particle41 both flag that human review does not scale to agent-generated change rates; GitHub's own playbook warns about prompt-injection via untrusted PR content. Naive "human approves every board PR" will collapse. Mitigation the field already converged on: risk-based approval tiers + automated pre-merge validation (Conftest/promptfoo) so humans only see high-blast-radius diffs.

**Canary evaluation without an oracle.** This is our sharpest trap and the one the prior art *cannot* fully solve for us. GrowthBook/Statsig/LaunchDarkly assume a dense, low-latency, statistically-significant metric per request. The self-evolving literature (Darwin Gödel Machine, AlphaEvolve, EvoPrompt) assumes a clean benchmark/fitness function. We have neither: outcome quality is a sparse, delayed, noisy human numeric score. A canary that "passes" on 20 scored interactions is noise; reward-hacking the score (STaR/OPRO-style) is a live risk. Borrow the *ramp + guardrail + kill-switch* mechanism, but do not borrow their confidence model — design for tiny-n, delayed, gameable signal (hold-outs, human spot-audit, regression-to-main on ambiguity).

**Prompt/version sprawl.** Langfuse/PromptLayer/Braintrust/Arthur all accumulate version explosions; MLflow's stage-transition model and content-addressable IDs are the response. With an AI Consultant generating PRs continuously, the board's history will sprawl far faster than human-authored repos. Need pruning/squashing discipline, semantic (not just chronological) version identity, and "why" rationale attached to every commit — or the history becomes unauditable, defeating the entire point of using git for legibility.

## 6. What still does NOT exist

The genuinely novel surface — none of the ~140 systems occupies it:

1. **An AI "Consultant" that PRs an entire ORG's operating policy for a general, non-coding org.** Every agent that opens PRs in the catalog (Copilot Coding Agent, Datadog Bits, Nx, Gitar, GitLab Duo, Amazon Q, Repairnator, gh-aw) edits the *data plane* — application/code — and is coding-only. The self-evolving systems that edit the *control plane* (EvoAgentX, AFlow, DSPy, Gödel/Darwin) have **no human/PR gate** and target prompts/workflows, not org structure (staff templates, scope, event subscriptions, taxonomy). GrowthBook governs agent-proposed *flags*, not org policy. Nobody has an agent proposing changes to *who is staffed, what they subscribe to, how events are routed* in a general business org, through review.

2. **Canary/A-B of org POLICY (not app traffic) under a bootstrapped human oracle.** Feature-flag platforms canary product features with statistical metrics; we'd canary *governance config* (a staff template, a pipeline) scored by a sparse human number. No tool combines policy-branching with a non-oracle, human-bootstrapped evaluation gate. The Particle41 article *describes* "auto-rollback on metric degradation for agent changes" but it's vendor guidance with no implementation, and it still presumes a metric.

3. **Routing decisions pinned to a policy commit SHA in a non-K8s, non-authz domain.** Argo CD pins cluster state to a SHA; OPA bundles pin policy to a version; but binding *every agent-org routing decision* to a board commit (so any outcome is replayable against the exact policy that produced it) is unbuilt outside infrastructure/authz contexts.

4. **The combined loop as one coherent system.** Each ingredient exists separately — git PR gate (GitHub), eval gate (promptfoo/Conftest), canary (GrowthBook/Argo Rollouts), policy/execution separation (OPA/Cedar/OCL), agent-proposal (evolutionary lit), provenance receipts (TRACE) — but **no system wires Consultant-proposes → eval±human-gate → merge → SHA-pinned routing → branch-canary → git-revert-rollback into one loop for an autonomous org.** That integration, plus the deliberate stance of having *no known-good desired state and no clean oracle* (the opposite of GitOps' founding assumption), is the build.

**Bottom line:** buy the guardrail engine (OPA/Cedar) and the substrate (git + a host's PRs); adopt promptfoo for eval-gating; steal canary mechanics from GrowthBook and reconciliation/SHA-pinning from Argo CD/OPAL; build the board schema, the Consultant, and — most novel and most dangerous — the small-n, gameable, human-bootstrapped evaluation gate that the entire prior art quietly assumes away.
