package main

import "context"

// ---------------------------------------------------------------------------
// Boot report: does this database actually have the pgvector column?
//
// Migration 022 adds `memories.content_embedding` only where the extension is
// available, and swallows the failure with a RAISE NOTICE where it is not —
// which is exactly what happens on managed Postgres when the app role cannot
// CREATE EXTENSION (the GCP direction). Before this ran at boot, the first
// anyone heard of it was a memory search that quietly came back keyword-only,
// or (before RD3 was fixed) a memory_create that answered "embedded": true.
//
// A deployment fact belongs in the boot log next to `store=postgres` and
// `embeddings=...`, where it is read while someone is still looking.
// ---------------------------------------------------------------------------

// reportMemoryVectorColumn logs the pgvector state once, at boot. It never
// fails the boot: a Postgres without pgvector is a supported deployment
// (memory search degrades to keyword+recency, §7.6.5) — it is just not one
// anybody should discover by accident.
//
// The combination that deserves a WARNING is "an embedding provider IS
// configured but the column is absent": memory_create refuses in that state
// (ErrMemoryEmbeddingUnstorable), so the operator has configured a semantic leg
// that cannot exist and every write of a memory will fail until one of the two
// is changed.
func reportMemoryVectorColumn(ctx context.Context, probe func(context.Context) (bool, error), embedderConfigured bool, logf func(string, ...any)) {
	if probe == nil {
		return
	}
	ok, err := probe(ctx)
	switch {
	case err != nil:
		logf("[agentd] WARNING: could not determine whether memories.content_embedding exists (%v) — "+
			"memory search may degrade to keyword-only without further notice", err)
	case ok:
		logf("[agentd] memory semantic leg=available (memories.content_embedding present)")
	case embedderConfigured:
		logf("[agentd] WARNING: memories.content_embedding is ABSENT but an embedding provider is configured — " +
			"migration 022 could not create the pgvector extension (typical on managed Postgres where the app role lacks the " +
			"privilege). memory_create will REFUSE to store embedded memories rather than write rows that can never be " +
			"embedded, and search is keyword+recency. Install pgvector and re-run migration 022, or unset " +
			"AGENTKIT_EMBEDDING_BACKEND to run keyword-only deliberately")
	default:
		logf("[agentd] memory semantic leg=unavailable (memories.content_embedding absent; no embedding provider configured) — " +
			"memory search is keyword+recency")
	}
}
