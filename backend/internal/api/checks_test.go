package api

// White-box tests for the deterministic check evaluator.

import (
	"strings"
	"testing"

	"markupmarkdown/internal/models"
)

const checkDoc = "# Overview\n\nBeamable is great.\n\n```\n# not a heading\n```\n\n## Rollback plan\n\nSteps here.\n\n#### Deep detail\n"

func TestEvaluateChecks_AllKinds(t *testing.T) {
	rules := []models.CheckRule{
		{ID: "1", Kind: "required_sections", Label: "Sections", Sections: []string{"Overview", "rollback plan"}},
		{ID: "2", Kind: "required_sections", Label: "Missing", Sections: []string{"Security"}},
		{ID: "3", Kind: "forbidden_text", Label: "No lowercase brand", Pattern: `\bbeamable\b`},
		{ID: "4", Kind: "required_text", Label: "Brand present", Pattern: `Beamable`},
		{ID: "5", Kind: "max_heading_depth", Label: "Depth", MaxDepth: 3},
		{ID: "6", Kind: "forbidden_text", Label: "Bad regex", Pattern: `([`},
	}
	res := evaluateChecks(rules, checkDoc)
	want := map[string]bool{"1": true, "2": false, "3": true, "4": true, "5": false, "6": false}
	for _, r := range res {
		if r.Pass != want[r.RuleID] {
			t.Errorf("rule %s (%s): pass=%v want %v (detail: %s)", r.RuleID, r.Label, r.Pass, want[r.RuleID], r.Detail)
		}
	}
	// Fenced "# not a heading" must not satisfy required_sections.
	res2 := evaluateChecks([]models.CheckRule{
		{ID: "x", Kind: "required_sections", Label: "Fence", Sections: []string{"not a heading"}},
	}, checkDoc)
	if res2[0].Pass {
		t.Error("heading inside code fence counted as a section")
	}
}

func TestValidateCheckRules(t *testing.T) {
	// Bad regex rejected at save time.
	_, err := validateCheckRules([]models.CheckRule{
		{Kind: "forbidden_text", Label: "x", Pattern: "(["},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid pattern") {
		t.Errorf("expected invalid-pattern error, got %v", err)
	}
	// Missing label rejected.
	if _, err := validateCheckRules([]models.CheckRule{{Kind: "required_text", Pattern: "x"}}); err == nil {
		t.Error("expected label-required error")
	}
	// Valid set normalizes + assigns IDs.
	out, err := validateCheckRules([]models.CheckRule{
		{Kind: "max_heading_depth", Label: "Depth", MaxDepth: 3, Pattern: "junk-to-clear"},
	})
	if err != nil || out[0].ID == "" || out[0].Pattern != "" {
		t.Errorf("normalization failed: %+v err=%v", out, err)
	}
}

func TestEvaluateChecks_FriendlyKinds(t *testing.T) {
	doc := "# Notes\n\nOur beamable SDK and BEAMABLE portal ship with Beamable branding.\nWe will fix this ASAP, it's still TBD.\n"
	rules := []models.CheckRule{
		{ID: "s", Kind: "term_spelling", Label: "Spelling", Term: "Beamable"},
		{ID: "p", Kind: "forbidden_phrases", Label: "Placeholders", Phrases: []string{"TBD", "lorem ipsum"}},
		{ID: "p2", Kind: "forbidden_phrases", Label: "Clean", Phrases: []string{"nonexistent phrase"}},
	}
	res := evaluateChecks(rules, doc)
	byID := map[string]models.CheckResult{}
	for _, r := range res {
		byID[r.RuleID] = r
	}
	if byID["s"].Pass {
		t.Errorf("term_spelling should fail: %+v", byID["s"])
	}
	if !strings.Contains(byID["s"].Detail, "beamable") || !strings.Contains(byID["s"].Detail, "BEAMABLE") {
		t.Errorf("expected both wrong casings in detail, got %q", byID["s"].Detail)
	}
	if byID["p"].Pass || !strings.Contains(byID["p"].Detail, "TBD") {
		t.Errorf("forbidden_phrases should flag TBD: %+v", byID["p"])
	}
	if !byID["p2"].Pass {
		t.Errorf("clean phrase list should pass: %+v", byID["p2"])
	}
	// Correct-only doc passes spelling.
	res2 := evaluateChecks(rules[:1], "# Notes\n\nBeamable everywhere, Beamable always.\n")
	if !res2[0].Pass {
		t.Errorf("all-correct casing should pass: %+v", res2[0])
	}
}
