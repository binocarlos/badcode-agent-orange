package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/gofiber/fiber/v3"
)

// Publish a running agent session as a reusable image (snapshot-as-image).
//
// Optionally writes a focus into /workspace/CLAUDE.md before snapshotting, and
// stamps skill_set from the session's installed_skills metadata. Snapshots the live
// session container into the image registry and records the resulting handle as an
// agent_custom_images row, so new sessions can be launched from it via the existing
// CustomImageID path (resolveLaunchImage).

type saveSessionAsImageBody struct {
	Customer   string `json:"customer"`
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
	Focus      string `json:"focus"`
}

// saveSessionAsImage handles POST /agent/custom-images/from-session/:sessionId.
func (apiServer *PlatinumAPIServer) saveSessionAsImage(c fiber.Ctx) error {
	jwtUser, ok := GetJWTUserFromContext(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "Unauthorized"})
	}
	sessionID := c.Params("sessionId")
	if sessionID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "sessionId is required"})
	}
	var b saveSessionAsImageBody
	if err := c.Bind().Body(&b); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if b.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name is required"})
	}
	visibility := b.Visibility
	if visibility == "" {
		visibility = "organizational"
	}
	if visibility != "private" && visibility != "organizational" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "visibility must be private or organizational"})
	}
	if !apiServer.validateCustomerAccess(jwtUser, b.Customer) {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "forbidden"})
	}
	if apiServer.deps.AgentRunner == nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "agent runner not configured"})
	}

	ctx := c.Context()

	// Optionally write a focus into CLAUDE.md before snapshotting so it is baked
	// into the image and active for all sessions launched from it.
	if strings.TrimSpace(b.Focus) != "" {
		if err := apiServer.deps.AgentRunner.WriteWorkspaceFile(ctx, agentkit.SessionRef{SessionID: sessionID}, "CLAUDE.md", []byte(b.Focus)); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "failed to write focus: " + err.Error()})
		}
	}

	// Snapshot the live session. The runner refuses on shared-tenancy workers that
	// do not support per-session snapshots; surface that message to the caller.
	handle, err := apiServer.deps.AgentRunner.Snapshot(ctx, agentkit.SessionRef{SessionID: sessionID})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}

	// Stamp skill_set + provenance from the source session so the published image
	// records which skills are baked in and what base it was burned from.
	skillSet := "[]"
	baseImageID, baseInstallation := "", ""
	if sess, sErr := apiServer.agentdb.GetSession(ctx, sessionID); sErr == nil && sess != nil {
		if raw, ok := sess.Metadata["installed_skills"]; ok {
			if bts, mErr := json.Marshal(raw); mErr == nil {
				skillSet = string(bts)
			}
		}
		// A session launched from a custom image records CustomImageID; one launched
		// from a platform installation records Installation (never both).
		if sess.CustomImageID != "" {
			baseImageID = sess.CustomImageID
		} else {
			baseInstallation = sess.Installation
		}
	}

	handleJSON, _ := json.Marshal(handle)
	// content hash identifies this snapshot handle (no composition skill set involved).
	sum := sha256.Sum256([]byte(handle.Kind + ":" + handle.Ref))

	row, err := apiServer.agentdb.UpsertCustomImage(ctx, &agentdb.CustomImage{
		Name:             b.Name,
		Description:      "Snapshot of session " + sessionID,
		Visibility:       visibility,
		Customer:         b.Customer,
		OwnerEmail:       jwtUser.Email,
		ContentHash:      hex.EncodeToString(sum[:]),
		RegistryHandle:   string(handleJSON),
		RequiresBuild:    false,
		SkillSet:         skillSet,
		BaseImageID:      baseImageID,
		BaseInstallation: baseInstallation,
		SourceSessionID:  sessionID,
		// Trim to match the CLAUDE.md write above (only applied when non-blank), so a
		// whitespace-only focus isn't recorded on the row but silently never applied.
		Focus: strings.TrimSpace(b.Focus),
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(fiber.StatusOK).JSON(row)
}
