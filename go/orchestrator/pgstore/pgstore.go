// Package pgstore holds the Postgres (gorm) implementations of the Slice-A data
// seams: agentdb.BoardStore (PgBoard), orchestrator.TicketStore (PgTicketStore),
// and orchestrator.Telemetry (PgTelemetry). It imports agentdb (row models +
// Board types) and orchestrator (domain types + interfaces); agentdb never
// imports orchestrator, so there is no cycle. Fast tests run against sqlite via
// AutoMigrate (the agentdb/board_test.go pattern); production wires *gorm.DB from
// agentdb.Store.DB().
package pgstore

import (
	"encoding/json"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

func opsToJSONArray(ops []agentdb.Op) agentdb.JSONArray {
	b, _ := json.Marshal(ops)
	return agentdb.JSONArray(b)
}

func jsonArrayBytes(j agentdb.JSONArray) []byte {
	if len(j) == 0 {
		return []byte("[]")
	}
	return []byte(j)
}
