package orchestrator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

var fragRefRe = regexp.MustCompile(`\{\{fragment:([a-zA-Z0-9_-]+)\}\}`)

// Compose resolves {{fragment:ID}} refs against the board and {{input}} against
// input. An unknown fragment id is an error (never silently empty) — guidance is
// load-bearing, so a missing fragment must fail loud, not compose to nothing.
func Compose(board agentdb.Board, template, input string) (string, error) {
	bodies := map[string]string{}
	for _, f := range board.Fragments {
		bodies[f.ID] = f.Body
	}
	var missing string
	out := fragRefRe.ReplaceAllStringFunc(template, func(m string) string {
		id := fragRefRe.FindStringSubmatch(m)[1]
		body, ok := bodies[id]
		if !ok {
			missing = id
			return m
		}
		return body
	})
	if missing != "" {
		return "", fmt.Errorf("compose: unknown fragment %q", missing)
	}
	return strings.ReplaceAll(out, "{{input}}", input), nil
}
