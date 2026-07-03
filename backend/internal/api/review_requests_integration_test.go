package api_test

// Integration tests for review requests (Phase 2a). Covers: create
// for a human (notification fires), create for an own agent token,
// rejection of someone else's token, the self-review guard, implicit
// fulfillment via setReview, dismiss authorization, and the token
// poll surface.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"markupmarkdown/internal/models"
	"markupmarkdown/internal/testutil"
)

// extractJSONField pulls a top-level string field out of a JSON body.
func extractJSONField(t *testing.T, body []byte, field string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal %s: %v (body=%s)", field, err, body)
	}
	s, _ := m[field].(string)
	if s == "" {
		t.Fatalf("field %q missing or empty in %s", field, body)
	}
	return s
}

func TestCreateReviewRequest_HumanReviewer(t *testing.T) {
	srv, st, _ := newTestServer(t)
	requester := testutil.NewTestUser(t, st)
	reviewer := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, requester.ID)
	doc := testutil.NewTestDocument(t, st, requester.ID, "hello")

	status, body := doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/review-requests",
		map[string]string{"reviewerLogin": reviewer.Login}, withCookie(sess))
	if status != 201 {
		t.Fatalf("status=%d body=%s want 201", status, body)
	}
	if !strings.Contains(string(body), `"state":"pending"`) {
		t.Errorf("expected pending state, got %s", body)
	}

	// Reviewer's queue shows it.
	reviewerSess := testutil.NewTestSession(t, st, reviewer.ID)
	status, body = doJSON(t, srv, "GET", "/api/me/review-requests", nil, withCookie(reviewerSess))
	if status != 200 || !strings.Contains(string(body), doc.ID) {
		t.Errorf("reviewer queue status=%d body=%s — expected the request", status, body)
	}

	// Notification fan-out is async; poll briefly.
	found := false
	for i := 0; i < 50 && !found; i++ {
		ns, _, _ := st.ListNotificationsForUser(context.Background(), reviewer.ID, 10)
		for _, n := range ns {
			if n.Kind == models.NotifyReviewRequest {
				found = true
			}
		}
		if !found {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !found {
		t.Errorf("reviewer never received a review_request notification")
	}
}

func TestCreateReviewRequest_SelfReviewRejected(t *testing.T) {
	srv, st, _ := newTestServer(t)
	user := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, user.ID)
	doc := testutil.NewTestDocument(t, st, user.ID, "hello")
	status, _ := doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/review-requests",
		map[string]string{"reviewerLogin": user.Login}, withCookie(sess))
	if status != 400 {
		t.Errorf("status=%d want 400 for self-review", status)
	}
}

func TestCreateReviewRequest_OwnTokenOnly(t *testing.T) {
	srv, st, _ := newTestServer(t)
	requester := testutil.NewTestUser(t, st)
	other := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, requester.ID)
	doc := testutil.NewTestDocument(t, st, requester.ID, "hello")

	// Someone else's token → 404 (not 403, don't confirm existence).
	_, otherTok := testutil.NewAPIToken(t, st, other.ID, models.TokenScopeWrite)
	status, _ := doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/review-requests",
		map[string]string{"tokenId": otherTok.ID}, withCookie(sess))
	if status != 404 {
		t.Errorf("status=%d want 404 for other user's token", status)
	}

	// Own token → 201, and the token's pending queue carries it.
	_, mine := testutil.NewAPIToken(t, st, requester.ID, models.TokenScopeWrite)
	status, body := doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/review-requests",
		map[string]string{"tokenId": mine.ID}, withCookie(sess))
	if status != 201 {
		t.Fatalf("own-token status=%d body=%s want 201", status, body)
	}
	pending, err := st.ListPendingReviewRequestsForToken(context.Background(), mine.ID)
	if err != nil || len(pending) != 1 {
		t.Errorf("token queue len=%d err=%v want 1", len(pending), err)
	}
}

func TestReviewRequest_ImplicitFulfillmentViaSetReview(t *testing.T) {
	srv, st, _ := newTestServer(t)
	requester := testutil.NewTestUser(t, st)
	reviewer := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, requester.ID)
	reviewerSess := testutil.NewTestSession(t, st, reviewer.ID)
	doc := testutil.NewTestDocument(t, st, requester.ID, "hello")

	doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/review-requests",
		map[string]string{"reviewerLogin": reviewer.Login}, withCookie(sess))

	// Reviewer sets a state — the pending request must auto-complete.
	status, _ := doJSON(t, srv, "PUT", "/api/documents/"+doc.ID+"/review",
		map[string]string{"state": "approved"}, withCookie(reviewerSess))
	if status != 200 {
		t.Fatalf("setReview status=%d", status)
	}
	pending, _ := st.ListPendingReviewRequestsForUser(context.Background(), reviewer.ID)
	if len(pending) != 0 {
		t.Errorf("pending queue len=%d after review, want 0 (implicit fulfillment)", len(pending))
	}
}

func TestDismissReviewRequest_AuthzAndEffect(t *testing.T) {
	srv, st, _ := newTestServer(t)
	requester := testutil.NewTestUser(t, st)
	reviewer := testutil.NewTestUser(t, st)
	stranger := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, requester.ID)
	doc := testutil.NewTestDocument(t, st, requester.ID, "hello")

	_, body := doJSON(t, srv, "POST", "/api/documents/"+doc.ID+"/review-requests",
		map[string]string{"reviewerLogin": reviewer.Login}, withCookie(sess))
	id := extractJSONField(t, body, "id")

	// A stranger can't dismiss.
	strangerSess := testutil.NewTestSession(t, st, stranger.ID)
	status, _ := doJSON(t, srv, "POST", "/api/review-requests/"+id+"/dismiss", nil, withCookie(strangerSess))
	if status != 403 {
		t.Errorf("stranger dismiss status=%d want 403", status)
	}

	// The reviewer can.
	reviewerSess := testutil.NewTestSession(t, st, reviewer.ID)
	status, _ = doJSON(t, srv, "POST", "/api/review-requests/"+id+"/dismiss", nil, withCookie(reviewerSess))
	if status != 204 {
		t.Errorf("reviewer dismiss status=%d want 204", status)
	}
	pending, _ := st.ListPendingReviewRequestsForUser(context.Background(), reviewer.ID)
	if len(pending) != 0 {
		t.Errorf("still pending after dismiss")
	}
}
