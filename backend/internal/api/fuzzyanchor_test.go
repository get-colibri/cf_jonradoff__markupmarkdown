package api

// White-box tests for the fuzzy anchor matcher. The bar: recover
// anchors through realistic small rewrites (punctuation churn, a word
// swapped, a sentence reflowed) while REFUSING to guess when the
// anchor is short, the text is gone, or two candidate passages are
// too similar to distinguish.

import (
	"strings"
	"testing"

	"markupmarkdown/internal/models"
)

const fuzzyDoc = `# Deployment guide

The deployment pipeline runs three stages in sequence. First the
build stage compiles the Go binary and bundles the frontend assets.
Then the verification stage runs the full integration suite against
a disposable database. Finally the release stage pushes the image to
the registry and restarts the fleet.

Rollbacks are manual by design: an operator inspects the failing
release and decides whether to roll forward or revert.`

func TestFuzzyFindAnchor_PunctuationChurn(t *testing.T) {
	anchor := "the verification stage runs the full integration suite"
	newDoc := strings.Replace(fuzzyDoc,
		"the verification stage runs the full integration suite",
		"the verification stage runs the full, integration suite", 1)
	got, ok := fuzzyFindAnchor(newDoc, anchor)
	if !ok {
		t.Fatal("expected a match through punctuation churn")
	}
	if !strings.Contains(got, "verification stage") {
		t.Errorf("matched the wrong passage: %q", got)
	}
	if !strings.Contains(newDoc, got) {
		t.Errorf("match is not a verbatim substring of the new doc: %q", got)
	}
}

func TestFuzzyFindAnchor_WordSubstitution(t *testing.T) {
	anchor := "compiles the Go binary and bundles the frontend assets"
	newDoc := strings.Replace(fuzzyDoc, "bundles", "packages", 1)
	got, ok := fuzzyFindAnchor(newDoc, anchor)
	if !ok {
		t.Fatal("expected a match through a single word substitution")
	}
	if !strings.Contains(got, "packages the frontend assets") {
		t.Errorf("expected the rewritten passage, got %q", got)
	}
}

func TestFuzzyFindAnchor_SentenceReflow(t *testing.T) {
	anchor := "an operator inspects the failing release and decides whether to roll forward"
	newDoc := strings.Replace(fuzzyDoc,
		"an operator inspects the failing\nrelease and decides whether to roll forward or revert",
		"an operator carefully inspects the failing release, then decides whether to roll forward or revert", 1)
	// Normalize the fixture's own line-wrap so the replace above hits.
	if !strings.Contains(newDoc, "carefully inspects") {
		newDoc = strings.ReplaceAll(fuzzyDoc, "\n", " ")
		newDoc = strings.Replace(newDoc,
			"an operator inspects the failing release and decides",
			"an operator carefully inspects the failing release, then decides", 1)
	}
	got, ok := fuzzyFindAnchor(newDoc, anchor)
	if !ok {
		t.Fatal("expected a match through a reflowed sentence")
	}
	if !strings.Contains(got, "inspects the failing release") {
		t.Errorf("matched the wrong passage: %q", got)
	}
}

func TestFuzzyFindAnchor_RefusesShortAnchors(t *testing.T) {
	if _, ok := fuzzyFindAnchor(fuzzyDoc, "the API"); ok {
		t.Error("short anchors must not fuzzy-match — too ambiguous")
	}
}

func TestFuzzyFindAnchor_RefusesWhenGone(t *testing.T) {
	anchor := "kubernetes cluster autoscaling misbehaves under sustained load"
	if got, ok := fuzzyFindAnchor(fuzzyDoc, anchor); ok {
		t.Errorf("matched %q for text that has no counterpart", got)
	}
}

func TestFuzzyFindAnchor_RefusesAmbiguous(t *testing.T) {
	// Two nearly-identical passages: the matcher must refuse rather
	// than guess which one the comment belonged to.
	doc := `The billing worker retries failed charges every six hours until settled.
Some filler text in between the two passages sits right here.
The billing worker retries failed charges every six hours until resolved.`
	anchor := "The billing worker retries failed charges every six hours until completed"
	if got, ok := fuzzyFindAnchor(doc, anchor); ok {
		t.Errorf("ambiguous anchor should refuse, matched %q", got)
	}
}

func TestReanchorComments_FuzzyFallback(t *testing.T) {
	// End-to-end through reanchorComments: exact match gone, fuzzy
	// recovers it with Fuzzy=true and the original preserved. The
	// markdown heading in the CONTENT exercises the PlainText path.
	content := "# Title\n\nThe deployment pipeline executes three stages in strict sequence every night.\n"
	anchor := "The deployment pipeline runs three stages in strict sequence every night"
	c := models.Comment{
		ID:     "c1",
		Anchor: models.Anchor{Exact: anchor},
	}
	res := reanchorComments([]models.Comment{c}, content)
	if res[0].Status != reanchorClean || !res[0].Fuzzy {
		t.Fatalf("expected fuzzy clean, got status=%v fuzzy=%v", res[0].Status, res[0].Fuzzy)
	}
	if res[0].OriginalExact != anchor {
		t.Errorf("original not preserved: %q", res[0].OriginalExact)
	}
	if !strings.Contains(res[0].Exact, "executes three stages") {
		t.Errorf("unexpected new anchor %q", res[0].Exact)
	}
}
