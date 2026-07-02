package orchestrator

import (
	"encoding/json"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// SeedFragment builds an OpAdd changeset that seeds a named guidance fragment.
// The board is never empty: startup is human-authored seed fragments + a goal.
func SeedFragment(id, body string) agentdb.Changeset {
	b, _ := json.Marshal(agentdb.BoardPromptFragment{ID: id, Kind: "role", Body: body})
	return agentdb.Changeset{
		Author:  "human",
		Message: "seed " + id,
		Ops:     []agentdb.Op{{Op: agentdb.OpAdd, EntityType: "prompt_fragment", EntityID: id, Body: b}},
	}
}
