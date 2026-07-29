package agentdb

// Token usage, as it is actually stored.
//
// This file exists because the two SQL readers of token usage
// (GetSessionTokenSummary and CountProjectTokensSince) spent the project's
// whole life querying a shape no writer has ever produced — flat, snake_case,
// and only on the FIRST envelope of a query — so both answered 0 forever and
// the router's `daily_tokens_soft`/`daily_tokens_hard` brakes could not fire.
// Keeping the expressions in one place is the cheapest way to stop the two
// readers drifting apart again.
//
// The shape below is CAPTURED, not designed. It was read out of the running
// e2e stack's Postgres on 2026-07-28 (`agent-orange-stack-e2e-postgres-1`,
// 942 stored query rows) with:
//
//	SELECT jsonb_pretty(e)
//	  FROM agent_query_events q, LATERAL jsonb_array_elements(q.events) e
//	 WHERE e->>'type' = 'query_complete' AND e->'data'->'usage' IS NOT NULL
//	 LIMIT 1;
//
//	{
//	    "data": {
//	        "model": "claude-opus-4-5",
//	        "usage": {
//	            "inputTokens": 10,
//	            "outputTokens": 6
//	        },
//	        "result": "Hello from the agentd mock model proxy. ...",
//	        "status": "completed",
//	        "queryId": "1e74bdb0-5666-4c21-8c67-8e928355c84a",
//	        "totalCostUsd": 0.0004
//	    },
//	    "type": "query_complete",
//	    "timestamp": "2026-07-25T23:29:06.243Z"
//	}
//
// Three facts about that corpus decided the SQL:
//
//   - usage is NESTED under `data.usage`, not flat on the envelope. Across all
//     942 rows, zero envelopes carried `input_tokens` at the envelope root or
//     under `data` — the flat shape the old readers queried never existed.
//   - the usage-bearing envelope is the LAST one, never the first (index 0 is
//     `user_message` in every row), so `events->0` could not have matched even
//     if the key names had been right. These expressions therefore sum over
//     EVERY envelope in the array, which also makes them correct if a stored
//     query ever carries more than one `query_complete`.
//   - the only key spelling in the corpus is camelCase (`inputTokens`), which
//     is what `sandbox/src/harness/claude-agent-sdk.ts` converts the provider's
//     snake_case into before emitting `query_complete`.
//
// The snake_case fallback below is nevertheless kept, and it is not the shape
// that caused this bug: `input_tokens` is the *provider's* own spelling on the
// Anthropic wire format, and the camelCase conversion happens in exactly one
// line of one PLUGGABLE harness. A future harness that forwards its provider's
// usage object verbatim would otherwise silently re-zero the ledger — the exact
// failure being fixed here. Both spellings are pinned by live-Postgres tests.
// What is deliberately NOT tolerated is the invented flat-at-the-envelope-root
// shape: no writer has ever emitted it and no stored row contains it.
//
// This is Postgres-only SQL (jsonb operators, LATERAL). On a store without
// them the readers return an error, which the router treats as "do not stop the
// world" — see tokenBudget.Allow.

// Which FIELDS are summed — the second half of the same bug (RD2, 2026-07-29).
//
// TOK1 fixed the jsonb PATH; the ledger still under-read, because `input_tokens`
// is only one of three separately-billed input components. The provider's usage
// object (`BetaUsage` in `@anthropic-ai/sdk`, reached through the agent SDK's
// `NonNullableUsage`) documents them as:
//
//	input_tokens                 "The number of input tokens which were used."
//	cache_creation_input_tokens  "The number of input tokens used to create the cache entry."
//	cache_read_input_tokens      "The number of input tokens read from the cache."
//
// None of the three contains the others, and all three are billed. With a large
// composed prompt and prompt caching active — which is exactly the shape this
// product creates, since ComposeJob concatenates a core preamble, a project
// prompt, a worker prompt and a briefing on every job — most input arrives as
// cache READS. Summing only `input_tokens` therefore reported a plausible
// non-zero fraction of true spend, so `daily_tokens_hard` fired far too late or
// never, and nobody investigates a counter that appears to work.
//
// Why tokens and not `data.totalCostUsd`, which is also on the envelope and is
// already truthful: the settings are named `daily_tokens_soft`/`daily_tokens_hard`
// and are entered, displayed and documented in tokens. Cost is provider-priced
// and model-dependent, so metering on it would make a fixed ceiling mean a
// different amount of work per model and silently re-scale every project's brake
// whenever list prices move. Tokens are the unit the operator chose; the fix is
// to count all of them. `totalCostUsd` stays stored (and is now written on the
// error path too), so a future cost-denominated ceiling is a reader change, not
// a re-instrumentation.
//
// Backward compatibility: the cache components are additional COALESCE'd terms,
// so an envelope written before this change — which has neither key — reads
// exactly as it did before rather than becoming unreadable.
const (
	// usageInputSQL / usageOutputSQL read one envelope (aliased `e`) expanded
	// by usageEnvelopes. A missing key yields NULL, which COALESCEs to 0.
	//
	// Both spellings are accepted per component for the reason given above:
	// snake_case is the provider's own wire spelling and the harness is
	// pluggable.
	usageInputSQL = `(COALESCE((e->'data'->'usage'->>'inputTokens')::bigint, (e->'data'->'usage'->>'input_tokens')::bigint, 0)` +
		` + COALESCE((e->'data'->'usage'->>'cacheCreationInputTokens')::bigint, (e->'data'->'usage'->>'cache_creation_input_tokens')::bigint, 0)` +
		` + COALESCE((e->'data'->'usage'->>'cacheReadInputTokens')::bigint, (e->'data'->'usage'->>'cache_read_input_tokens')::bigint, 0))`

	// Output has exactly one billed field. `output_tokens_details` is a
	// read-only decomposition of it ("output_tokens remains the inclusive,
	// authoritative total used for billing"), so adding it would double-count.
	usageOutputSQL = `COALESCE((e->'data'->'usage'->>'outputTokens')::bigint, (e->'data'->'usage'->>'output_tokens')::bigint, 0)`
)

// usageEnvelopes is the FROM-clause fragment expanding one agent_query_events
// row into one row per stored envelope, aliased `e`. `tbl` is the alias of the
// agent_query_events row in the enclosing query.
//
// The jsonb_typeof guard is not paranoia about our own writes (the column is
// NOT NULL DEFAULT '[]' and JSONArray always marshals an array) but about
// jsonb_array_elements, which raises rather than returning no rows if a row
// ever holds a non-array — one malformed row must not fail a project's whole
// budget query and, through the gate's fail-open rule, quietly unmeter it.
func usageEnvelopes(tbl string) string {
	return `CROSS JOIN LATERAL jsonb_array_elements(
		CASE WHEN jsonb_typeof(` + tbl + `.events) = 'array' THEN ` + tbl + `.events ELSE '[]'::jsonb END
	) AS e`
}
