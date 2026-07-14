-- Runs once on first postgres boot. agentdb's own migrations create the agent
-- tables; this just pre-enables pgvector for the upcoming memory system.
CREATE EXTENSION IF NOT EXISTS vector;
