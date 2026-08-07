// Project API keys: the long-lived server-side credential a third-party
// application's backend uses to reach one project's API.
//
// There is deliberately no api_keys table and no key-management UI. A key is ops
// config, exactly like the project map that names it: the map holds the *name*
// of an environment variable, agentd reads the value at boot, and rotation is
// "change the env var and restart". Only BadCode ops will ever add one, and the
// project map is already the surface where they do that kind of thing.
package main

import (
	"crypto/subtle"
	"fmt"
	"sort"
	"strings"
)

// projectKeys resolves a raw API key to the project it grants, and reports the
// origins allowed to frame a project's embed page.
type projectKeys interface {
	// ProjectForKey returns the project a raw API key grants, using a
	// constant-time compare. ok=false for unknown or empty keys.
	ProjectForKey(raw string) (project string, ok bool)
	// AllowedOrigins returns the frame-ancestors list for a project.
	AllowedOrigins(project string) []string
	// hasKeys reports whether any project has a key at all. The auth middleware
	// needs it: a configured key means a real deployment, which is what turns
	// dev-open mode off.
	hasKeys() bool
}

// minAPIKeyLen is the floor a configured key must clear. A weak key is worse
// than no key: it looks like security and grants a whole project. 24 characters
// is roughly 128 bits of base64url, which is what `openssl rand -base64 24`
// produces and what the docs tell ops to run.
const minAPIKeyLen = 24

type projectKeyEntry struct{ project, key string }

// projectKeyIndex is the boot-time resolution of the project map's api_key_env
// names against the process environment. It is immutable after construction.
type projectKeyIndex struct {
	entries []projectKeyEntry
	origins map[string][]string
}

// newProjectKeys reads each project's api_key_env from getenv and builds the
// value→project index. It fails the boot rather than degrading when the config
// is dangerous (a short key, or one value granting two projects); it only warns
// when a project simply has no key, which is the normal state of most projects.
func newProjectKeys(cfgs map[string]projectConfig, getenv func(string) string, logf func(string, ...any)) (*projectKeyIndex, error) {
	idx := &projectKeyIndex{origins: map[string][]string{}}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	// Sorted so boot logs and error messages are deterministic across restarts —
	// map iteration order would otherwise make "which project errored first" a
	// coin flip when two are misconfigured.
	ids := make([]string, 0, len(cfgs))
	for id := range cfgs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	byValue := map[string]string{} // key value → project, for the collision check
	var keyless []string
	for _, id := range ids {
		cfg := cfgs[id]
		if len(cfg.AllowedOrigins) > 0 {
			idx.origins[id] = append([]string(nil), cfg.AllowedOrigins...)
		}
		if cfg.APIKeyEnv == "" {
			keyless = append(keyless, id)
			continue
		}
		// Trimmed: a key mounted from a file or a compose env_file routinely
		// arrives with a trailing newline, and "the key is right but has a \n"
		// is an unpleasant thing to debug through a constant-time compare.
		raw := strings.TrimSpace(getenv(cfg.APIKeyEnv))
		if raw == "" {
			logf("[agentd] project %q: %s is unset or empty — this project has no API key", id, cfg.APIKeyEnv)
			keyless = append(keyless, id)
			continue
		}
		if len(raw) < minAPIKeyLen {
			return nil, fmt.Errorf("project %q: %s is %d characters; an API key must be at least %d (a weak key is worse than none)",
				id, cfg.APIKeyEnv, len(raw), minAPIKeyLen)
		}
		if other, clash := byValue[raw]; clash {
			return nil, fmt.Errorf("projects %q and %q have the same API key value (%s and %s) — one key must grant exactly one project",
				other, id, cfgs[other].APIKeyEnv, cfg.APIKeyEnv)
		}
		byValue[raw] = id
		idx.entries = append(idx.entries, projectKeyEntry{project: id, key: raw})
	}
	if len(idx.entries) > 0 {
		named := make([]string, 0, len(idx.entries))
		for _, e := range idx.entries {
			named = append(named, e.project)
		}
		logf("[agentd] project API keys enabled for %d project(s): %s", len(named), strings.Join(named, ", "))
	}
	if len(keyless) > 0 {
		logf("[agentd] project(s) with no API key: %s", strings.Join(keyless, ", "))
	}
	return idx, nil
}

// ProjectForKey returns the project a raw key grants. The scan visits every
// entry with no early exit and compares in constant time, so neither the number
// of comparisons nor their duration depends on how much of the key was right.
func (p *projectKeyIndex) ProjectForKey(raw string) (string, bool) {
	if p == nil || raw == "" {
		return "", false
	}
	rawB := []byte(raw)
	match, found := "", 0
	for _, e := range p.entries {
		if subtle.ConstantTimeCompare(rawB, []byte(e.key)) == 1 {
			match, found = e.project, 1
		}
	}
	if found == 0 {
		return "", false
	}
	return match, true
}

// AllowedOrigins returns the origins permitted to frame this project's embed
// page. Nil means none are configured, which the embed page renders as
// frame-ancestors 'none'.
func (p *projectKeyIndex) AllowedOrigins(project string) []string {
	if p == nil {
		return nil
	}
	return p.origins[project]
}

// allowedOrigins is the union of every project's framing list, with duplicates
// left in — frameAncestors dedups and sorts. It exists because the embed page's
// request identifies no project (see embedcsp.go), so the per-project accessor
// above has nothing to be called with. Kept OFF the projectKeys interface: that
// interface exists for the auth middleware, and this is the CSP route's
// business alone.
func (p *projectKeyIndex) allowedOrigins() []string {
	if p == nil {
		return nil
	}
	var all []string
	for _, origins := range p.origins {
		all = append(all, origins...)
	}
	return all
}

func (p *projectKeyIndex) hasKeys() bool { return p != nil && len(p.entries) > 0 }

// projectConfigsOf is the nil-tolerant accessor for the projects half of the
// map: a deployment with no project map at all still gets a working (empty) key
// index rather than a special case at every call site.
func projectConfigsOf(s *projectSettings) map[string]projectConfig {
	if s == nil {
		return nil
	}
	return s.projects
}
