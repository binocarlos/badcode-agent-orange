package agentdb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// SourceHash returns a stable digest of the query-event set, used to detect
// whether a session needs re-indexing. Order-insensitive (sorted by ID).
func SourceHash(qes []*QueryEvents) string {
	if len(qes) == 0 {
		return ""
	}
	keys := make([]string, 0, len(qes))
	for _, qe := range qes {
		keys = append(keys, fmt.Sprintf("%s:%d", qe.ID, qe.CreatedAt))
	}
	sort.Strings(keys)
	h := sha256.Sum256([]byte(strings.Join(keys, "|")))
	return hex.EncodeToString(h[:])
}

// ComposeTranscriptText concatenates the precomputed search_text of each query
// event into the keyword corpus indexed by transcript_tsv.
func ComposeTranscriptText(qes []*QueryEvents) string {
	parts := make([]string, 0, len(qes))
	for _, qe := range qes {
		if s := strings.TrimSpace(qe.SearchText); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}
