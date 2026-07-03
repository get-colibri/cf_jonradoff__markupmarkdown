package api_test

// Admin console guard tests: unauthenticated 401, Bearer-token 403
// (cookie-only surface), non-allowlisted user 404 (don't confirm the
// route exists), allowlisted user 200 with the overview shape.

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"markupmarkdown/internal/models"
	"markupmarkdown/internal/testutil"
)

func TestAdminOverview_Guards(t *testing.T) {
	srv, st, _ := newTestServer(t)
	user := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, user.ID)

	// Unauthenticated → 401.
	status, _ := doJSON(t, srv, "GET", "/api/admin/overview", nil)
	if status != 401 {
		t.Errorf("anon status=%d want 401", status)
	}

	// Non-admin user → 404 (route existence not confirmed).
	status, _ = doJSON(t, srv, "GET", "/api/admin/overview", nil, withCookie(sess))
	if status != 404 {
		t.Errorf("non-admin status=%d want 404", status)
	}

	// Allowlist the user, then Bearer token still refused (403) —
	// admin is cookie-session only even for the admin's own tokens.
	t.Setenv("MARKUPMARKDOWN_ADMIN_LOGINS", user.Login)
	plain, _ := testutil.NewAPIToken(t, st, user.ID, models.TokenScopeAdmin)
	status, _ = doJSON(t, srv, "GET", "/api/admin/overview", nil, withBearer(plain))
	if status != 403 {
		t.Errorf("token status=%d want 403", status)
	}

	// Cookie session + allowlisted → 200 with the overview shape.
	status, body := doJSON(t, srv, "GET", "/api/admin/overview", nil, withCookie(sess))
	if status != 200 {
		t.Fatalf("admin status=%d body=%s want 200", status, body)
	}
	for _, field := range []string{`"users"`, `"docsPublic"`, `"docsPrivate"`, `"docsPerDay"`} {
		if !strings.Contains(string(body), field) {
			t.Errorf("overview missing %s: %s", field, body)
		}
	}
}

func TestAdminRecentPublicDocs_ExcludesPrivate(t *testing.T) {
	srv, st, _ := newTestServer(t)
	admin := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, admin.ID)
	t.Setenv("MARKUPMARKDOWN_ADMIN_LOGINS", admin.Login)

	pub := testutil.NewTestDocument(t, st, admin.ID, "public content")
	priv := testutil.NewTestDocument(t, st, admin.ID, "private content")
	if _, err := st.Documents().UpdateOne(context.Background(),
		bson.M{"_id": priv.ID}, bson.M{"$set": bson.M{"private": true}}); err != nil {
		t.Fatalf("mark private: %v", err)
	}

	status, body := doJSON(t, srv, "GET", "/api/admin/recent-public-docs", nil, withCookie(sess))
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(string(body), pub.ID) {
		t.Errorf("public doc missing from feed")
	}
	if strings.Contains(string(body), priv.ID) {
		t.Errorf("PRIVATE doc leaked into the admin feed")
	}
}
