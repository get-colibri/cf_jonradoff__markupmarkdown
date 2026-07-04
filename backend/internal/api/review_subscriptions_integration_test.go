package api_test

// Integration tests for standing reviewers (Phase 2b): the
// subscription lifecycle and — critically — the revision hook that
// auto-mints review requests when a new revision lands. Also covers
// the author-skip rule and the doc-scoped pending-requests listing.

import (
	"context"
	"strings"
	"testing"
	"time"

	"markupmarkdown/internal/models"
	"markupmarkdown/internal/testutil"
)

// waitForPending polls the token's pending queue — the revision hook
// runs on a background goroutine.
func waitForPending(t *testing.T, st interface {
	ListPendingReviewRequestsForToken(ctx context.Context, tokenID string) ([]models.ReviewRequest, error)
}, tokenID string, want int) []models.ReviewRequest {
	t.Helper()
	var got []models.ReviewRequest
	for i := 0; i < 100; i++ {
		got, _ = st.ListPendingReviewRequestsForToken(context.Background(), tokenID)
		if len(got) == want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pending queue len=%d want %d", len(got), want)
	return nil
}

func TestReviewSubscription_RevisionMintsRequest(t *testing.T) {
	srv, st, _ := newTestServer(t)
	owner := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, owner.ID)
	doc := testutil.NewTestDocument(t, st, owner.ID, "v1\n")
	_, tok := testutil.NewAPIToken(t, st, owner.ID, models.TokenScopeWrite)

	// Subscribe the agent token as a standing reviewer.
	status, body := doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/reviewers",
		map[string]string{"tokenId": tok.ID}, withCookie(sess))
	if status != 201 {
		t.Fatalf("subscribe status=%d body=%s", status, body)
	}

	// A manual revision should mint a pending request for the token
	// on the NEW doc.
	status, body = doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/manual-revisions",
		map[string]string{"content": "v2\n"}, withCookie(sess))
	if status != 201 {
		t.Fatalf("revision status=%d body=%s", status, body)
	}
	newDocID := extractJSONField(t, body, "id")

	pending := waitForPending(t, st, tok.ID, 1)
	if pending[0].DocumentID != newDocID {
		t.Errorf("request doc=%s want the new revision %s", pending[0].DocumentID, newDocID)
	}
}

func TestReviewSubscription_AuthorSkipped(t *testing.T) {
	srv, st, _ := newTestServer(t)
	owner := testutil.NewTestUser(t, st)
	reviewer := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, owner.ID)
	reviewerSess := testutil.NewTestSession(t, st, reviewer.ID)
	doc := testutil.NewTestDocument(t, st, owner.ID, "v1\n")

	// Subscribe the REVIEWER (human) as standing reviewer.
	doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/reviewers",
		map[string]string{"reviewerLogin": reviewer.Login}, withCookie(sess))

	// The reviewer themselves writes the next revision — they must NOT
	// be asked to review their own work.
	status, _ := doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/manual-revisions",
		map[string]string{"content": "v2 by reviewer\n"}, withCookie(reviewerSess))
	if status != 201 {
		t.Fatalf("revision status=%d", status)
	}
	// Give the async hook a moment, then assert the queue stayed empty.
	time.Sleep(300 * time.Millisecond)
	pending, _ := st.ListPendingReviewRequestsForUser(context.Background(), reviewer.ID)
	if len(pending) != 0 {
		t.Errorf("author got asked to review their own revision: %+v", pending)
	}
}

func TestReviewSubscription_FollowsChain(t *testing.T) {
	srv, st, _ := newTestServer(t)
	owner := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, owner.ID)
	doc := testutil.NewTestDocument(t, st, owner.ID, "v1\n")
	_, tok := testutil.NewAPIToken(t, st, owner.ID, models.TokenScopeWrite)

	// Revise FIRST, subscribe on the CHILD, then revise again — the
	// subscription must anchor to the chain root and still fire.
	_, body := doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/manual-revisions",
		map[string]string{"content": "v2\n"}, withCookie(sess))
	childID := extractJSONField(t, body, "id")

	doJSON(t, srv, "POST", "/api/documents/"+childID+"/reviewers",
		map[string]string{"tokenId": tok.ID}, withCookie(sess))

	// Listing reviewers from ANY doc in the chain sees the sub.
	status, body := doJSON(t, srv, "GET", "/api/documents/"+doc.ID+"/reviewers", nil, withCookie(sess))
	if status != 200 || !strings.Contains(string(body), tok.ID) {
		t.Errorf("root listing status=%d body=%s — expected the subscription", status, body)
	}

	_, body = doJSON(t, srv, "POST", "/api/documents/"+childID+"/manual-revisions",
		map[string]string{"content": "v3\n"}, withCookie(sess))
	v3ID := extractJSONField(t, body, "id")
	pending := waitForPending(t, st, tok.ID, 1)
	if pending[0].DocumentID != v3ID {
		t.Errorf("request doc=%s want v3 %s", pending[0].DocumentID, v3ID)
	}
}

func TestListDocReviewRequests_ShowsPending(t *testing.T) {
	srv, st, _ := newTestServer(t)
	owner := testutil.NewTestUser(t, st)
	reviewer := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, owner.ID)
	doc := testutil.NewTestDocument(t, st, owner.ID, "hello")

	doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/review-requests",
		map[string]string{"reviewerLogin": reviewer.Login}, withCookie(sess))

	status, body := doJSON(t, srv, "GET", "/api/documents/"+doc.ID+"/review-requests", nil, withCookie(sess))
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(string(body), `"state":"pending"`) {
		t.Errorf("expected the pending request in doc listing, got %s", body)
	}
}

func TestDeleteReviewSubscription_Authz(t *testing.T) {
	srv, st, _ := newTestServer(t)
	owner := testutil.NewTestUser(t, st)
	stranger := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, owner.ID)
	doc := testutil.NewTestDocument(t, st, owner.ID, "hello")
	_, tok := testutil.NewAPIToken(t, st, owner.ID, models.TokenScopeWrite)

	_, body := doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/reviewers",
		map[string]string{"tokenId": tok.ID}, withCookie(sess))
	subID := extractJSONField(t, body, "id")

	strangerSess := testutil.NewTestSession(t, st, stranger.ID)
	status, _ := doJSON(t, srv, "DELETE", "/api/review-subscriptions/"+subID, nil, withCookie(strangerSess))
	if status != 403 {
		t.Errorf("stranger delete status=%d want 403", status)
	}
	status, _ = doJSON(t, srv, "DELETE", "/api/review-subscriptions/"+subID, nil, withCookie(sess))
	if status != 204 {
		t.Errorf("creator delete status=%d want 204", status)
	}
}

func TestCheckPolicy_RoundTripAndChainScope(t *testing.T) {
	srv, st, a := newTestServer(t)
	user := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, user.ID)
	doc := testutil.NewTestDocument(t, st, user.ID,
		"# Overview\n\nAll data is beamable-ready.\n")

	// Set a policy on the root.
	status, body := doJSON(t, srv, "PUT", "/api/documents/"+doc.ID+"/check-policy",
		map[string]any{"rules": []map[string]any{
			{"kind": "required_sections", "label": "Sections", "sections": []string{"Overview", "Security"}},
			{"kind": "forbidden_text", "label": "Terminology", "pattern": `\bbeamable\b`},
		}}, withCookie(sess))
	if status != 200 {
		t.Fatalf("put policy status=%d body=%s", status, body)
	}

	// Checks on the root: Sections fails (no Security), Terminology fails.
	status, body = doJSON(t, srv, "GET", "/api/documents/"+doc.ID+"/checks", nil, withCookie(sess))
	if status != 200 {
		t.Fatalf("checks status=%d body=%s", status, body)
	}
	if !strings.Contains(string(body), `"hasPolicy":true`) ||
		!strings.Contains(string(body), "missing: Security") {
		t.Errorf("unexpected checks payload: %s", body)
	}

	// A child revision that fixes both must pass — and the POLICY must
	// follow the chain without being re-created.
	child, err := a.EditDocument(context.Background(), user.ID, doc.ID,
		"# Overview\n\n# Security\n\nAll data is Beamable-ready.\n", "", "")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	status, body = doJSON(t, srv, "GET", "/api/documents/"+child.ID+"/checks", nil, withCookie(sess))
	if status != 200 {
		t.Fatalf("child checks status=%d", status)
	}
	if strings.Contains(string(body), `"pass":false`) {
		t.Errorf("child should pass all checks: %s", body)
	}

	// Empty rules deletes the policy.
	doJSON(t, srv, "PUT", "/api/documents/"+doc.ID+"/check-policy",
		map[string]any{"rules": []map[string]any{}}, withCookie(sess))
	status, body = doJSON(t, srv, "GET", "/api/documents/"+doc.ID+"/checks", nil, withCookie(sess))
	if status != 200 || !strings.Contains(string(body), `"hasPolicy":false`) {
		t.Errorf("policy not deleted: %s", body)
	}
}

func TestCheckTemplates_LinkEditForkDelete(t *testing.T) {
	srv, st, _ := newTestServer(t)
	user := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, user.ID)
	docA := testutil.NewTestDocument(t, st, user.ID, "# Overview\n\nBody TBD.\n")
	docB := testutil.NewTestDocument(t, st, user.ID, "# Overview\n\nClean body.\n")

	// Create the named policy.
	status, body := doJSON(t, srv, "POST", "/api/me/check-templates",
		map[string]any{"name": "PRD Standard", "rules": []map[string]any{
			{"kind": "forbidden_phrases", "label": "No placeholders", "phrases": []string{"TBD"}},
		}}, withCookie(sess))
	if status != 201 {
		t.Fatalf("create template status=%d body=%s", status, body)
	}
	tplID := extractJSONField(t, body, "id")

	// Link BOTH docs.
	for _, id := range []string{docA.ID, docB.ID} {
		status, body = doJSON(t, srv, "PUT", "/api/documents/"+id+"/check-policy",
			map[string]any{"templateId": tplID}, withCookie(sess))
		if status != 200 {
			t.Fatalf("link %s status=%d body=%s", id, status, body)
		}
	}

	// A fails (TBD present), B passes.
	_, bodyA := doJSON(t, srv, "GET", "/api/documents/"+docA.ID+"/checks", nil, withCookie(sess))
	if !strings.Contains(string(bodyA), `"pass":false`) {
		t.Errorf("docA should fail placeholder check: %s", bodyA)
	}

	// EDIT the template (add a section rule) → docB now fails via the
	// link, with no per-doc update.
	status, _ = doJSON(t, srv, "PUT", "/api/me/check-templates/"+tplID,
		map[string]any{"rules": []map[string]any{
			{"kind": "forbidden_phrases", "label": "No placeholders", "phrases": []string{"TBD"}},
			{"kind": "required_sections", "label": "Sections", "sections": []string{"Security"}},
		}}, withCookie(sess))
	if status != 200 {
		t.Fatalf("update template status=%d", status)
	}
	_, bodyB := doJSON(t, srv, "GET", "/api/documents/"+docB.ID+"/checks", nil, withCookie(sess))
	if !strings.Contains(string(bodyB), "missing: Security") {
		t.Errorf("template edit did not propagate to linked docB: %s", bodyB)
	}

	// FORK docA: save inline rules → detaches; later template edits
	// must not affect it.
	doJSON(t, srv, "PUT", "/api/documents/"+docA.ID+"/check-policy",
		map[string]any{"rules": []map[string]any{
			{"kind": "max_heading_depth", "label": "Depth", "maxDepth": 3},
		}}, withCookie(sess))
	doJSON(t, srv, "PUT", "/api/me/check-templates/"+tplID,
		map[string]any{"rules": []map[string]any{
			{"kind": "required_sections", "label": "Sections", "sections": []string{"Compliance"}},
		}}, withCookie(sess))
	_, bodyA = doJSON(t, srv, "GET", "/api/documents/"+docA.ID+"/checks", nil, withCookie(sess))
	if strings.Contains(string(bodyA), "Compliance") {
		t.Errorf("forked docA still follows template: %s", bodyA)
	}

	// DELETE the template → docB keeps working with materialized rules.
	status, _ = doJSON(t, srv, "DELETE", "/api/me/check-templates/"+tplID, nil, withCookie(sess))
	if status != 204 {
		t.Fatalf("delete template status=%d", status)
	}
	_, bodyB = doJSON(t, srv, "GET", "/api/documents/"+docB.ID+"/checks", nil, withCookie(sess))
	if !strings.Contains(string(bodyB), "Compliance") {
		t.Errorf("materialize-on-delete lost docB's rules: %s", bodyB)
	}
}
