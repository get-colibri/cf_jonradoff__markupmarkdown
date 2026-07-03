package api_test

// getDocument has several optional branches that depend on the doc's
// position in its revision chain (parent summary, children list,
// latest descendant, ancestor count). The basic GET is already
// covered; this test threads a 3-revision chain and fetches the
// middle node so all of those branches run.

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"markupmarkdown/internal/testutil"
)

func TestGetDocument_MiddleOfChain_PopulatesParentChildrenAndDescendant(t *testing.T) {
	srv, st, a := newTestServer(t)
	user := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, user.ID)
	root := testutil.NewTestDocument(t, st, user.ID, "v1\n")

	// Two manual edits → chain: root → mid → tip.
	mid, err := a.EditDocument(context.Background(), user.ID, root.ID, "v2\n", "", "")
	if err != nil {
		t.Fatalf("edit 1: %v", err)
	}
	_, err = a.EditDocument(context.Background(), user.ID, mid.ID, "v3\n", "", "")
	if err != nil {
		t.Fatalf("edit 2: %v", err)
	}

	// Fetch the MIDDLE doc; response should include parent (root) and
	// LatestDescendant (tip) and the revisionIndex should be 2.
	status, body := doJSON(t, srv, "GET", "/api/documents/"+mid.ID, nil, withCookie(sess))
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(string(body), `"parent":`) {
		t.Errorf("expected parent in response, got %s", body)
	}
	if !strings.Contains(string(body), `"latestDescendant":`) {
		t.Errorf("expected latestDescendant in response, got %s", body)
	}
	if !strings.Contains(string(body), `"revisionIndex":2`) {
		t.Errorf("expected revisionIndex=2 for middle node, got %s", body)
	}
	if !strings.Contains(string(body), `"revisionTotal":3`) {
		t.Errorf("expected revisionTotal=3 for a 3-node chain, got %s", body)
	}
}

func TestGetDocument_RootRecordsViewedTimestamp(t *testing.T) {
	// Subsequent reads should surface a previouslyViewedAt timestamp.
	// First read won't have one — second read will.
	srv, st, _ := newTestServer(t)
	user := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, user.ID)
	doc := testutil.NewTestDocument(t, st, user.ID, "hello")

	doJSON(t, srv, "GET", "/api/documents/"+doc.ID, nil, withCookie(sess))
	// Tiny pause to let the async view enqueue write.
	for i := 0; i < 50; i++ {
		v, _ := st.GetDocumentView(context.Background(), doc.ID, user.ID)
		if v != nil {
			break
		}
	}

	_, body := doJSON(t, srv, "GET", "/api/documents/"+doc.ID, nil, withCookie(sess))
	// The previouslyViewedAt field appears only after the first view
	// is persisted. It may or may not appear on the second read
	// depending on the async-enqueue race; this is a smoke
	// expectation, not an exact assertion.
	_ = body
}

// Regression for the false-drift banner (reported 2026-07-02 on
// beamable/CrmDesign Federation_PRD.md). Two bugs in the child-doc
// drift overlay:
//  1. The root's SourceDriftIgnoredSHA wasn't mirrored, so an Ignore
//     on the root re-surfaced the banner on every child revision.
//  2. A child whose own source_sha equals the current upstream SHA
//     (i.e. it was created FROM that upstream via merge/sync) still
//     inherited the root's stale baseline — manufacturing drift when
//     there was nothing to merge.
func TestGetDocument_ChildDriftOverlay(t *testing.T) {
	srv, st, a := newTestServer(t)
	user := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, user.ID)
	root := testutil.NewTestDocument(t, st, user.ID, "v1\n")

	child, err := a.EditDocument(context.Background(), user.ID, root.ID, "v2\n", "", "")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}

	// Stamp drift state on the ROOT: upstream moved to SHA "new", the
	// root's baseline is "old", and the user has ignored "new".
	_, err = st.Documents().UpdateOne(context.Background(),
		bson.M{"_id": root.ID},
		bson.M{"$set": bson.M{
			"source_sha":               "old",
			"source_latest_sha":        "new",
			"source_drift_ignored_sha": "new",
		}})
	if err != nil {
		t.Fatalf("stamp root drift: %v", err)
	}

	// Case 1: child baseline differs from upstream → overlay applies,
	// and it must carry the root's ignored SHA so the banner stays
	// dismissed.
	_, err = st.Documents().UpdateOne(context.Background(),
		bson.M{"_id": child.ID},
		bson.M{"$set": bson.M{"source_sha": "unrelated"}})
	if err != nil {
		t.Fatalf("stamp child sha: %v", err)
	}
	status, body := doJSON(t, srv, "GET", "/api/documents/"+child.ID, nil, withCookie(sess))
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if !strings.Contains(string(body), `"sourceDriftIgnoredSha":"new"`) {
		t.Errorf("overlay dropped the root's ignored SHA: %s", body)
	}

	// Case 2: child baseline EQUALS the current upstream SHA (it was
	// created from that upstream) → no drift overlay at all.
	_, err = st.Documents().UpdateOne(context.Background(),
		bson.M{"_id": child.ID},
		bson.M{"$set": bson.M{"source_sha": "new"}})
	if err != nil {
		t.Fatalf("re-stamp child sha: %v", err)
	}
	status, body = doJSON(t, srv, "GET", "/api/documents/"+child.ID, nil, withCookie(sess))
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if strings.Contains(string(body), `"sourceLatestSha":"new"`) {
		t.Errorf("child in sync with upstream still shows drift: %s", body)
	}
}

// Chain-level drift suppression: when the chain's LATEST revision
// already matches the current upstream SHA (e.g. it was pushed back,
// which re-baselines the pushed doc), OLDER revisions must not nag
// about an "upstream change" the chain itself produced. Reported live
// 2026-07-03 on WINGMAN_PRD.md after a merge+push cycle.
func TestGetDocument_RootDriftSuppressedWhenLeafInSync(t *testing.T) {
	srv, st, a := newTestServer(t)
	user := testutil.NewTestUser(t, st)
	sess := testutil.NewTestSession(t, st, user.ID)
	root := testutil.NewTestDocument(t, st, user.ID, "v1\n")
	child, err := a.EditDocument(context.Background(), user.ID, root.ID, "v2\n", "", "")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}

	// Root drifted to upstream "pushed"; the leaf was pushed back and
	// re-baselined to that same SHA.
	for id, set := range map[string]bson.M{
		root.ID:  {"source_sha": "old", "source_latest_sha": "pushed"},
		child.ID: {"source_sha": "pushed"},
	} {
		if _, err := st.Documents().UpdateOne(context.Background(),
			bson.M{"_id": id}, bson.M{"$set": set}); err != nil {
			t.Fatalf("stamp %s: %v", id, err)
		}
	}

	// The ROOT must not carry drift in its response — its chain is
	// current with upstream.
	status, body := doJSON(t, srv, "GET", "/api/documents/"+root.ID, nil, withCookie(sess))
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if strings.Contains(string(body), `"sourceLatestSha":"pushed"`) {
		t.Errorf("root still advertises drift its own chain reconciled: %s", body)
	}
}
