package events

import "testing"

func TestSkillInstalledConst(t *testing.T) {
	if SkillInstalled != "skill_installed" {
		t.Fatalf("SkillInstalled = %q, want %q", SkillInstalled, "skill_installed")
	}
}
