package watchapi

import "net/http"

// RevisionDTO is the timeline projection of a board revision (author, message, ts)
// — the legible "watch it learn" story, without the raw ops payload.
type RevisionDTO struct {
	ID        string `json:"id"`
	ParentID  string `json:"parent_id"`
	Seq       int64  `json:"seq"`
	Author    string `json:"author"`
	Message   string `json:"message"`
	CreatedAt int64  `json:"created_at"`
}

// FragmentDTO / BoardDTO project the folded board to the fragments the surface
// renders (deferred Staff/EventTypes/Pipelines are omitted).
type FragmentDTO struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Body          string `json:"body"`
	LastChangedIn string `json:"last_changed_in"`
}

type BoardDTO struct {
	Revision  string        `json:"revision"`
	Fragments []FragmentDTO `json:"fragments"`
}

// Revisions serves GET /api/board/revisions — the story timeline.
func (h *Handlers) Revisions(w http.ResponseWriter, r *http.Request) {
	revs, err := h.cfg.Revisions.Revisions(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]RevisionDTO, 0, len(revs))
	for _, rv := range revs {
		out = append(out, RevisionDTO{
			ID: rv.ID, ParentID: rv.ParentID, Seq: rv.Seq,
			Author: rv.Author, Message: rv.Message, CreatedAt: rv.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// Current serves GET /api/board/current — the folded fragments at head.
func (h *Handlers) Current(w http.ResponseWriter, r *http.Request) {
	board, err := h.cfg.Board.Current(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dto := BoardDTO{Revision: board.Revision, Fragments: make([]FragmentDTO, 0, len(board.Fragments))}
	for _, f := range board.Fragments {
		dto.Fragments = append(dto.Fragments, FragmentDTO{
			ID: f.ID, Kind: f.Kind, Body: f.Body, LastChangedIn: f.LastChangedIn,
		})
	}
	writeJSON(w, http.StatusOK, dto)
}
