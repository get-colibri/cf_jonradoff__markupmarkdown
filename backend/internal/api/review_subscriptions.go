package api

// Standing reviewers (Phase 2b — "CI for prose"). A subscription
// anchors a reviewer (human or one of your agent tokens) to a
// revision CHAIN: every new revision automatically mints a
// ReviewRequest for each subscriber, so agents get summoned without
// anyone asking. Subscriptions live on the chain root; the fan-out
// hook (fanOutRevisionEvents) fires from every child-revision
// creation path.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"markupmarkdown/internal/httperr"
	"markupmarkdown/internal/models"
	"markupmarkdown/internal/store"
)

// chainRootID resolves the chain root for any doc in the chain.
func (a *API) chainRootID(ctx context.Context, doc *models.Document) string {
	if doc.ParentID == "" {
		return doc.ID
	}
	if root, err := a.store.RootDocument(ctx, doc.ID); err == nil && root != nil {
		return root.ID
	}
	return doc.ID
}

// createReviewSubscription is POST /api/documents/:id/reviewers —
// adds a standing reviewer to this doc's chain. Same target shape as
// review requests: reviewerLogin XOR tokenId, own tokens only.
func (a *API) createReviewSubscription(w http.ResponseWriter, r *http.Request) {
	docID := mux.Vars(r)["id"]
	doc, accErr := a.checkDocAccess(r, docID)
	if accErr != nil {
		a.writeAccessError(w, r, accErr)
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	if !a.enforceScope(w, r, models.TokenScopeWrite) {
		return
	}
	capBody(w, r, maxBodyDefault)

	var req createReviewRequestBody
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.ReviewerLogin = strings.TrimSpace(req.ReviewerLogin)
	req.TokenID = strings.TrimSpace(req.TokenID)
	if (req.ReviewerLogin == "") == (req.TokenID == "") {
		writeError(w, http.StatusBadRequest,
			"set exactly one of reviewerLogin (a human) or tokenId (one of your agent tokens)")
		return
	}

	sub := &models.ReviewSubscription{
		RootDocumentID: a.chainRootID(r.Context(), doc),
		CreatedByID:    user.ID,
	}
	if req.ReviewerLogin != "" {
		users, err := a.store.FindUsersByLogins(r.Context(), []string{req.ReviewerLogin})
		if err != nil || len(users) == 0 {
			writeError(w, http.StatusNotFound, "no user with that login has used markupmarkdown")
			return
		}
		sub.ReviewerUserID = users[0].ID
		sub.ReviewerName = preferName(&users[0])
	} else {
		tok, err := a.store.GetAPITokenByID(r.Context(), user.ID, req.TokenID)
		if err != nil || tok == nil {
			writeError(w, http.StatusNotFound, "no active token of yours with that id")
			return
		}
		sub.ReviewerTokenID = tok.ID
		sub.ReviewerName = tok.Label
	}

	if err := a.store.UpsertReviewSubscription(r.Context(), sub); err != nil {
		internalError(w, "store.upsert_review_subscription", err)
		return
	}
	a.hub.Broadcast(doc.ID, "reviews-updated")
	writeJSON(w, http.StatusCreated, sub)
}

// listReviewSubscriptions is GET /api/documents/:id/reviewers.
func (a *API) listReviewSubscriptions(w http.ResponseWriter, r *http.Request) {
	docID := mux.Vars(r)["id"]
	doc, accErr := a.checkDocAccess(r, docID)
	if accErr != nil {
		a.writeAccessError(w, r, accErr)
		return
	}
	subs, err := a.store.ListReviewSubscriptions(r.Context(), a.chainRootID(r.Context(), doc))
	if err != nil {
		internalError(w, "store.list_review_subscriptions", err)
		return
	}
	if subs == nil {
		subs = []models.ReviewSubscription{}
	}
	writeJSON(w, http.StatusOK, subs)
}

// deleteReviewSubscription is DELETE /api/review-subscriptions/:id.
// The subscription creator or the subscribed human can remove it.
func (a *API) deleteReviewSubscription(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	id := mux.Vars(r)["id"]
	sub, err := a.store.GetReviewSubscription(r.Context(), id)
	if err != nil {
		internalError(w, "store.get_review_subscription", err)
		return
	}
	if sub == nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	allowed := sub.CreatedByID == user.ID || sub.ReviewerUserID == user.ID
	if !allowed && sub.ReviewerTokenID != "" {
		if tok, _ := a.store.GetAPITokenByID(r.Context(), user.ID, sub.ReviewerTokenID); tok != nil {
			allowed = true
		}
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "only the subscriber or whoever added them can remove a standing reviewer")
		return
	}
	if err := a.store.DeleteReviewSubscription(r.Context(), id); err != nil {
		internalError(w, "store.delete_review_subscription", err)
		return
	}
	a.hub.Broadcast(sub.RootDocumentID, "reviews-updated")
	w.WriteHeader(http.StatusNoContent)
}

// fanOutRevisionEvents fires after ANY new child revision lands
// (manual edit, AI-revision accept, apply-suggestion, MCP edit,
// merge). It walks the chain's standing reviewers and mints a
// pending ReviewRequest per subscriber on the NEW revision, skipping
// whoever authored it. Best-effort background — a fan-out failure
// must never fail the revision itself.
//
// authorTokenID is non-empty when the revision was written under a
// Bearer token; used to skip a subscribed agent reviewing its own
// output.
func (a *API) fanOutRevisionEvents(newDoc *models.Document, authorUserID, authorTokenID, authorName string) {
	if newDoc == nil || newDoc.ParentID == "" {
		return
	}
	docID := newDoc.ID
	docTitle := newDoc.Title
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		root, err := a.store.RootDocument(ctx, docID)
		rootID := docID
		if err == nil && root != nil {
			rootID = root.ID
		}
		subs, err := a.store.ListReviewSubscriptions(ctx, rootID)
		if err != nil || len(subs) == 0 {
			return
		}
		now := time.Now().UTC()
		for _, sub := range subs {
			// Don't ask the revision's author to review their own work
			// — neither as a human nor via the token that wrote it.
			if sub.ReviewerUserID != "" && sub.ReviewerUserID == authorUserID {
				continue
			}
			if sub.ReviewerTokenID != "" && sub.ReviewerTokenID == authorTokenID {
				continue
			}
			rr := &models.ReviewRequest{
				ID:              store.ReviewRequestID(docID, firstNonEmpty(sub.ReviewerUserID, sub.ReviewerTokenID)),
				DocumentID:      docID,
				DocumentTitle:   docTitle,
				RequesterID:     sub.CreatedByID,
				RequesterName:   authorName,
				ReviewerUserID:  sub.ReviewerUserID,
				ReviewerTokenID: sub.ReviewerTokenID,
				ReviewerName:    sub.ReviewerName,
			}
			if err := a.store.UpsertReviewRequest(ctx, rr); err != nil {
				_, _ = httperr.Log("review_subscriptions.fanout", err)
				continue
			}
			// Auto-review tokens: the backend fulfills immediately.
			if sub.ReviewerTokenID != "" {
				a.enqueueAutoReview(rr.ID, sub.ReviewerTokenID)
			}
			// Human subscribers get a bell notification; agents poll.
			if sub.ReviewerUserID != "" {
				n := &models.Notification{
					ID:            uuid.NewString(),
					UserID:        sub.ReviewerUserID,
					Kind:          models.NotifyReviewRequest,
					DocumentID:    docID,
					DocumentTitle: docTitle,
					ActorName:     authorName,
					Preview:       "published a new revision for your review",
					CreatedAt:     now,
				}
				if err := a.store.InsertNotification(ctx, n); err != nil {
					_, _ = httperr.Log("notifications.revision_review", err)
				}
			}
		}
		a.hub.Broadcast(docID, "reviews-updated")
	}()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
