package api

// The served /SKILL.md is a go:embed of skill.md in THIS package —
// the embed directive cannot reach outside the package dir, so the
// skills/markupmarkdown/SKILL.md is copied here. This test fails the
// build check whenever the two drift, which is what keeps rule #12
// ("SKILL.md is the canonical agent guide") true in practice.
// Fix: cp skills/markupmarkdown/SKILL.md backend/internal/api/skill.md

import (
	"os"
	"testing"
)

func TestEmbeddedSkillMatchesCanonical(t *testing.T) {
	canonical, err := os.ReadFile("../../../skills/markupmarkdown/SKILL.md")
	if err != nil {
		t.Skipf("canonical SKILL.md not readable (non-repo build?): %v", err)
	}
	if string(canonical) != embeddedSkill {
		t.Fatal("backend/internal/api/skill.md is out of sync with skills/markupmarkdown/SKILL.md — run: cp skills/markupmarkdown/SKILL.md backend/internal/api/skill.md")
	}
}
