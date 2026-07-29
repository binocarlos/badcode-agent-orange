package main

// mcp_images.go — the image MCP tools (spec 08-images-and-skills §13),
// registered onto the host MCP server in mcpserver.go.
//
// The whole surface is two tools:
//
//	image_create(name, labels)   → the new {name, version} record
//	image_list(label_selector?)  → the project's catalogue, newest first
//
// There is no image_update and no image_delete, and their absence is the design
// (§13.2): a version is never overwritten, so publishing an improved
// environment means burning a NEW version under the same name — exactly as
// improving a rolling summary means appending a newer memory. The store has no
// mutating method to call even if a tool wanted to.
//
// # What image_create actually does
//
// It is a thin naming layer over machinery that has existed since layer 0
// (§13.6). `Runner.Snapshot` commits the CALLING SESSION's container and
// persists the result through `imageregistry.Persist`; the handle that comes
// back is what the catalogue row points at. The session is identified from the
// token, never from an argument — a session can only ever snapshot ITSELF, and
// there is no parameter with which to name another one.
//
//	session container ──image_create──▶ agentd ──Runner.Snapshot──▶ its own container
//	                                        └──CreateCustomImage──▶ agentdb (+ config event)
//
// Order matters and is deliberate: validate, then snapshot, then record. A
// failed record after a successful snapshot leaves orphaned bytes the reaper
// will collect; the reverse order would leave a catalogue row pointing at
// nothing, which is precisely the dishonesty §13.7 exists to prevent.
//
// The `config_events` row is written IN the same transaction as the catalogue
// row by I1's CreateCustomImage — this file only passes the caller through as
// ConfigWrite. The routable `config.changed` emission happens after commit and
// belongs to J3, not here (§15.4).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// imageCatalog is the narrow slice of *agentdb.Store the tools need. Note what
// is NOT in it: no update, no delete — the append-only invariant of §13.2
// survives the seam.
type imageCatalog interface {
	CreateCustomImage(ctx context.Context, ci *agentdb.CustomImage, cw agentdb.ConfigWrite) (*agentdb.CustomImage, error)
	ListCustomImageVersions(ctx context.Context, q agentdb.ImageCatalogQuery) ([]*agentdb.CustomImage, error)
}

// sessionSnapshotter is the engine seam image_create burns through:
// agentkit.Runner satisfies it. Narrow on purpose — the tools may snapshot a
// session and nothing else, and a test can fake one honestly.
type sessionSnapshotter interface {
	Snapshot(ctx context.Context, ref agentkit.SessionRef) (imageregistry.Handle, error)
}

// imageListCap bounds a bare image_list. §13.4 specifies no limit and this
// deliberately does not add a parameter for one — but a catalogue that has been
// curated for a year would otherwise arrive as one enormous tool result, and a
// silent truncation is worse than none, so the cap is stated in the result.
const imageListCap = 200

type imageTools struct {
	store      imageCatalog
	snapshots  sessionSnapshotter
	permalinks permalinker
}

func newImageTools(store imageCatalog, snapshots sessionSnapshotter, permalinks permalinker) *imageTools {
	return &imageTools{store: store, snapshots: snapshots, permalinks: permalinks}
}

// ---------------------------------------------------------------------------
// Result shapes
// ---------------------------------------------------------------------------

// imageRecord is one catalogue version, exactly the tuple §13.4 names, plus the
// session permalink every provenance-carrying result in this system emits under
// the key `session_url` (F3).
//
// The registry handle is deliberately absent: it is how the engine finds the
// bytes, not something a model has any use for, and it names storage locations.
type imageRecord struct {
	Name             string            `json:"name"`
	Version          int               `json:"version"`
	Labels           map[string]string `json:"labels"`
	CreatedByWorker  string            `json:"created_by_worker"`
	CreatedBySession string            `json:"created_by_session"`
	SessionURL       string            `json:"session_url"`
	CreatedAt        int64             `json:"created_at"`
}

func (i *imageTools) record(project string, ci *agentdb.CustomImage) imageRecord {
	return imageRecord{
		Name:             ci.Name,
		Version:          ci.Version,
		Labels:           labelMap(ci.Labels),
		CreatedByWorker:  ci.CreatedByWorker,
		CreatedBySession: ci.CreatedBySession,
		SessionURL:       i.permalinks.SessionURL(project, ci.CreatedBySession),
		CreatedAt:        ci.CreatedAt,
	}
}

// ---------------------------------------------------------------------------
// Tool descriptions
//
// Prompt, not documentation: the only thing standing between a model and a
// misuse of the catalogue, read on every job.
// ---------------------------------------------------------------------------

const imageCreateDescription = `Snapshot THIS session's environment as a new, named, versioned project image.

Use it after you have installed and VERIFIED a set of tools that future jobs ` +
	`should start with, so nobody pays the install cost again. Nothing about a ` +
	`session's filesystem survives the session unless you call this — that is by ` +
	`design, not an oversight.

Versions are append-only. Burning "toolbox" again does not replace anything: it ` +
	`records toolbox:2 alongside toolbox:1, and workers pointed at the bare name ` +
	`"toolbox" pick up the new one next time they run. Nothing is ever overwritten ` +
	`and no tool deletes a version.

Labels are the commit message — say WHY this version exists ` +
	`({"purpose":"marketing-toolbox","adds":"ffmpeg+imagemagick"}). They are ` +
	`identifiers, not prose: alphanumeric with '-', '_' or '.', at most 63 ` +
	`characters, at most 32 labels.

Burning an image does NOT point any worker at it. That is a separate, deliberate ` +
	`act (worker_update with an image field) so that "when did this worker change ` +
	`environment, and who decided?" stays answerable.

This commits a container image, so it takes a while and it is not free. Burn ` +
	`when you have something worth keeping, not routinely.`

const imageListDescription = `List this project's images, newest first.

Each entry carries {name, version, labels, created_by_worker, created_by_session, ` +
	`session_url, created_at}: what it is called, which burn it was, why it exists, ` +
	`who burned it and the conversation it came from.

The optional label_selector uses the same Kubernetes-style grammar as ` +
	`memory_search, comma-ANDed: "purpose=marketing-toolbox", "adds in (ffmpeg, ` +
	`imagemagick)", "exists purpose", "!deprecated". No OR and no nesting.

Reading the versions: a worker pointed at a bare name gets the HIGHEST version ` +
	`listed for it; a worker pinned to "name:version" gets exactly that one. ` +
	`Reaped versions (whose bytes the storage policy has deleted) are not listed ` +
	`at all — if a version you remember is missing, it is gone, not hidden.`

// tools returns the two image tools, in the order the model sees them.
func (i *imageTools) tools() []*mcpTool {
	return []*mcpTool{
		{
			Name:        "image_create",
			Description: imageCreateDescription,
			InputSchema: objectSchema(map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The image name, e.g. \"marketing-tools\". Lowercase alphanumerics with '.', '-' and '_'; never a ':' — the version is allocated for you.",
				},
				"labels": map[string]any{
					"type":                 "object",
					"description":          "Flat string→string labels saying why this version exists. Identifiers only: [A-Za-z0-9] with '-', '_', '.', ≤63 chars, ≤32 labels.",
					"additionalProperties": map[string]any{"type": "string"},
				},
			}, []string{"name"}),
			Handler: i.create,
		},
		{
			Name:        "image_list",
			Description: imageListDescription,
			InputSchema: objectSchema(map[string]any{
				"label_selector": map[string]any{
					"type":        "string",
					"description": "Kubernetes-style label selector, comma-ANDed. Optional; omit for the whole catalogue.",
				},
			}, nil),
			Handler: i.list,
		},
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type imageCreateArgs struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

func (i *imageTools) create(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args imageCreateArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if caller.SessionID == "" {
		// image_create snapshots the CALLING session. Without one there is
		// nothing to snapshot, and there is no argument that could name a
		// substitute — which is the point (§13.4).
		return nil, errors.New("image_create can only be called from inside a session: this token names no session")
	}

	// Validate everything BEFORE the snapshot. Committing a container takes
	// real time and real bytes; a mistyped label must not cost either (§9) —
	// and neither must an unidentifiable caller (RD4), so the config-log actor
	// is resolved up here rather than at the write.
	cw, err := caller.configWrite("")
	if err != nil {
		return nil, err
	}
	ref, err := agentdb.ParseImageRef(strings.TrimSpace(args.Name))
	if err != nil {
		return nil, fmt.Errorf("name: %w", err)
	}
	if ref.Pinned() {
		return nil, fmt.Errorf("name %q must not carry a version: the version is allocated by the catalogue, one higher than the last burn of %q (§13.2)", args.Name, ref.Name)
	}
	if err := agentdb.ValidateLabels(args.Labels); err != nil {
		return nil, fmt.Errorf("labels: %w", err)
	}

	// Burn. §13.6: this is the existing Snapshot()/imageregistry.Persist() path,
	// nothing new — the session is taken from the token, so a caller can only
	// ever snapshot itself.
	handle, err := i.snapshots.Snapshot(ctx, agentkit.SessionRef{SessionID: caller.SessionID})
	if err != nil {
		return nil, fmt.Errorf("could not snapshot this session's environment, so NOTHING was recorded: %w", err)
	}
	encoded, err := json.Marshal(handle)
	if err != nil {
		return nil, fmt.Errorf("encode snapshot handle: %w", err)
	}

	// Record. CreateCustomImage allocates the version, writes the catalogue row
	// and the `image_create` config event in ONE transaction (§15.4), and reads
	// the stored row back — so what is echoed below is what the database holds,
	// not the struct handed to it (§9).
	stored, err := i.store.CreateCustomImage(ctx, &agentdb.CustomImage{
		Customer:         caller.Project, // in code, always — never an argument
		Name:             ref.Name,
		Labels:           agentdb.LabelSet(args.Labels),
		RegistryHandle:   string(encoded),
		CreatedByWorker:  caller.Worker,
		CreatedBySession: caller.SessionID,
	}, cw)
	if err != nil {
		return nil, fmt.Errorf("the environment was snapshotted but the catalogue refused the record, so this image is NOT usable: %w", err)
	}
	return i.record(caller.Project, stored), nil
}

type imageListArgs struct {
	LabelSelector string `json:"label_selector"`
}

func (i *imageTools) list(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args imageListArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	// Ask for one more than the cap, so "there is more" is a fact rather than a
	// guess from a full page.
	versions, err := i.store.ListCustomImageVersions(ctx, agentdb.ImageCatalogQuery{
		Project:       caller.Project, // in code, always — never an argument
		LabelSelector: args.LabelSelector,
		Limit:         imageListCap + 1,
	})
	if err != nil {
		return nil, err
	}
	truncated := len(versions) > imageListCap
	if truncated {
		versions = versions[:imageListCap]
	}
	out := make([]imageRecord, 0, len(versions))
	for _, ci := range versions {
		out = append(out, i.record(caller.Project, ci))
	}
	result := map[string]any{
		"images": out,
		"count":  len(out),
	}
	if truncated {
		result["truncated"] = true
		result["note"] = fmt.Sprintf(
			"Only the %d newest images are shown. Narrow the search with a label_selector to see older ones.", imageListCap)
	}
	return result, nil
}
