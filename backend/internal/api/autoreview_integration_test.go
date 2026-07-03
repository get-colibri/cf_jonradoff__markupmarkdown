package api_test

// Integration test for backend-fulfilled reviews (auto-review tokens).
// The Claude call is stubbed via SetAutoReviewerForTest; everything
// else — claim, suggestion validation, review write, implicit
// fulfillment — runs for real against the test DB.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"markupmarkdown/internal/ai"
	"markupmarkdown/internal/models"
	"markupmarkdown/internal/testutil"
)

func TestAutoReview_FulfillsRequest(t *testing.T) {
	srv, st, a := newTestServer(t)
	owner := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, owner.ID)
	doc := testutil.NewTestDocument(t, st, owner.ID,
		"# Spec\n\nOur uptime is always perfect.\n")

	// Owner needs a stored Anthropic key (the reviewer bills to it).
	if err := a.StoreAnthropicKeyForTest(context.Background(), owner.ID, "sk-ant-test"); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	// Auto-review token, created through the REST surface.
	status, body := doJSON(t, srv, "POST", "/api/me/tokens",
		map[string]any{"label": "auto-bot", "scope": "admin", "autoReview": true},
		withCookie(sess))
	if status != 201 {
		t.Fatalf("create token status=%d body=%s", status, body)
	}
	tokID := extractNestedID(t, body)

	// Stub reviewer: one valid suggestion, one bogus quote (must be
	// dropped by anchor validation), state changes_requested.
	a.SetAutoReviewerForTest(func(_ context.Context, apiKey, title, content string, _ []ai.ResolvedComment) (*ai.ReviewResult, error) {
		if apiKey == "" || !strings.Contains(content, "uptime") {
			t.Errorf("stub called with wrong inputs: key=%q", apiKey)
		}
		return &ai.ReviewResult{
			State: "changes_requested",
			Note:  "One overclaim found.",
			Suggestions: []ai.ReviewSuggestion{
				{Quoted: "Our uptime is always perfect.", Replacement: "We target 99.9% uptime.", Rationale: "Overclaim."},
				{Quoted: "text that does not exist", Replacement: "whatever", Rationale: "bogus"},
			},
		}, nil
	})

	// Summon the token, then drive one synchronous sweep.
	status, body = doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/review-requests",
		map[string]string{"tokenId": tokID}, withCookie(sess))
	if status != 201 {
		t.Fatalf("request status=%d body=%s", status, body)
	}
	a.RunAutoReviewSweepForTest()

	// Request auto-completed via the review write.
	pending, _ := st.ListPendingReviewRequestsForToken(context.Background(), tokID)
	if len(pending) != 0 {
		t.Errorf("request still pending after auto-review: %+v", pending)
	}
	// Review exists with the stubbed state, attributed to the token.
	rv, err := st.GetReview(context.Background(), doc.ID, owner.ID)
	if err != nil || rv == nil {
		t.Fatalf("review missing: %v", err)
	}
	if rv.State != models.ReviewStateChangesRequested || rv.TokenID != tokID {
		t.Errorf("review state=%s token=%s want changes_requested/%s", rv.State, rv.TokenID, tokID)
	}
	// Exactly ONE suggestion landed (the bogus quote was dropped).
	comments, _ := st.ListComments(context.Background(), doc.ID)
	withSuggestion := 0
	for _, c := range comments {
		if c.Suggestion != nil {
			withSuggestion++
			if c.Suggestion.Replacement != "We target 99.9% uptime." {
				t.Errorf("unexpected replacement %q", c.Suggestion.Replacement)
			}
			if c.ActorKind != models.ActorAgent {
				t.Errorf("suggestion not agent-attributed")
			}
		}
	}
	if withSuggestion != 1 {
		t.Errorf("suggestions=%d want 1 (bogus quote dropped)", withSuggestion)
	}

	// A second sweep is a no-op: request completed, claim burned.
	a.RunAutoReviewSweepForTest()
	comments2, _ := st.ListComments(context.Background(), doc.ID)
	if len(comments2) != len(comments) {
		t.Errorf("second sweep duplicated work")
	}
}

func TestAutoReview_SkipsWithoutKey(t *testing.T) {
	srv, st, a := newTestServer(t)
	owner := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, owner.ID)
	doc := testutil.NewTestDocument(t, st, owner.ID, "hello world")

	status, body := doJSON(t, srv, "POST", "/api/me/tokens",
		map[string]any{"label": "auto-bot", "scope": "admin", "autoReview": true},
		withCookie(sess))
	if status != 201 {
		t.Fatalf("create token status=%d body=%s", status, body)
	}
	tokID := extractNestedID(t, body)

	called := false
	a.SetAutoReviewerForTest(func(_ context.Context, _, _, _ string, _ []ai.ResolvedComment) (*ai.ReviewResult, error) {
		called = true
		return &ai.ReviewResult{State: "approved"}, nil
	})
	doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/review-requests",
		map[string]string{"tokenId": tokID}, withCookie(sess))
	a.RunAutoReviewSweepForTest()

	if called {
		t.Error("reviewer ran without a stored Anthropic key")
	}
	// Request stays pending — an external agent could still fulfill it.
	pending, _ := st.ListPendingReviewRequestsForToken(context.Background(), tokID)
	if len(pending) != 1 {
		t.Errorf("pending=%d want 1 (unclaimed)", len(pending))
	}
}

// extractNestedID pulls metadata.id from the create-token response.
func extractNestedID(t *testing.T, body []byte) string {
	t.Helper()
	var m struct {
		Metadata struct {
			ID string `json:"id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Metadata.ID == "" {
		t.Fatalf("no metadata.id in %s", body)
	}
	return m.Metadata.ID
}
