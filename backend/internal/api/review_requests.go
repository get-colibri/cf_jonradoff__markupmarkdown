package api

// Review requests (Phase 2a of the review-coordination work): "please
// review this doc." A request targets either a human user (by login)
// or one of the requester's own agent tokens. Fulfillment is implicit
// — when the reviewer sets a review state on the doc, any pending
// request they hold auto-completes (hooked in setReview). No explicit
// "submit review" step, keeping the flow one-click on both ends.
//
// Delivery: humans get a bell notification; agents poll via the MCP
// list_review_requests tool (or GET /api/me/review-requests with
// their Bearer token). No outbound webhooks — polling proves the
// demand before we take on a new failure domain.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"markupmarkdown/internal/httperr"
	"markupmarkdown/internal/models"
)

// createReviewRequestBody is the body of
// POST /api/documents/:id/review-requests. Exactly one of
// reviewerLogin / tokenId must be set.
type createReviewRequestBody struct {
	ReviewerLogin string `json:"reviewerLogin,omitempty"`
	TokenID       string `json:"tokenId,omitempty"`
}

// createReviewRequest asks a reviewer to look at this doc. Requester
// must be signed in; write scope for token callers (an agent may
// request a human's review of its own work).
func (a *API) createReviewRequest(w http.ResponseWriter, r *http.Request) {
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

	rr := &models.ReviewRequest{
		DocumentID:    doc.ID,
		DocumentTitle: doc.Title,
		RequesterID:   user.ID,
		RequesterName: preferName(user),
	}

	if req.ReviewerLogin != "" {
		users, err := a.store.FindUsersByLogins(r.Context(), []string{req.ReviewerLogin})
		if err != nil || len(users) == 0 {
			writeError(w, http.StatusNotFound, "no user with that login has used markupmarkdown")
			return
		}
		reviewer := users[0]
		if reviewer.ID == user.ID {
			writeError(w, http.StatusBadRequest, "you can't request a review from yourself")
			return
		}
		rr.ReviewerUserID = reviewer.ID
		rr.ReviewerName = preferName(&reviewer)
	} else {
		// Agent reviewer: the token must belong to the requester —
		// summoning someone ELSE's bot would let you drain their
		// owner's API budget. GetAPITokenByID scopes to owner and
		// filters revoked tokens.
		tok, err := a.store.GetAPITokenByID(r.Context(), user.ID, req.TokenID)
		if err != nil || tok == nil {
			writeError(w, http.StatusNotFound, "no active token of yours with that id")
			return
		}
		rr.ReviewerTokenID = tok.ID
		rr.ReviewerName = tok.Label
	}

	if err := a.store.UpsertReviewRequest(r.Context(), rr); err != nil {
		internalError(w, "store.upsert_review_request", err)
		return
	}
	if info, ok := tokenInfoFromRequest(r); ok {
		a.logTokenAction(r.Context(), info.TokenID, "review_request.create", doc.ID)
	}

	// Humans get a bell notification. Agent tokens poll — no push.
	if rr.ReviewerUserID != "" {
		a.notifyReviewRequest(rr, user)
	}
	a.hub.Broadcast(doc.ID, "reviews-updated")
	writeJSON(w, http.StatusCreated, rr)
}

// listMyReviewRequests is GET /api/me/review-requests — the pending
// queue for the calling identity. Token callers get requests targeted
// at that token; cookie sessions get requests targeted at the user.
func (a *API) listMyReviewRequests(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	var (
		out []models.ReviewRequest
		err error
	)
	if info, ok := tokenInfoFromRequest(r); ok {
		out, err = a.store.ListPendingReviewRequestsForToken(r.Context(), info.TokenID)
	} else {
		out, err = a.store.ListPendingReviewRequestsForUser(r.Context(), user.ID)
	}
	if err != nil {
		internalError(w, "store.list_review_requests", err)
		return
	}
	if out == nil {
		out = []models.ReviewRequest{}
	}
	writeJSON(w, http.StatusOK, out)
}

// dismissReviewRequest is POST /api/review-requests/:id/dismiss. The
// targeted reviewer or the original requester can dismiss.
func (a *API) dismissReviewRequest(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	id := mux.Vars(r)["id"]
	rr, err := a.store.GetReviewRequest(r.Context(), id)
	if err != nil {
		internalError(w, "store.get_review_request", err)
		return
	}
	if rr == nil {
		writeError(w, http.StatusNotFound, "review request not found")
		return
	}
	allowed := rr.RequesterID == user.ID || rr.ReviewerUserID == user.ID
	if !allowed && rr.ReviewerTokenID != "" {
		// The owner of the targeted token can dismiss on its behalf.
		// GetAPITokenByID is owner-scoped: non-nil ⇒ user owns it.
		if tok, _ := a.store.GetAPITokenByID(r.Context(), user.ID, rr.ReviewerTokenID); tok != nil {
			allowed = true
		}
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "only the reviewer or requester can dismiss this request")
		return
	}
	if err := a.store.DismissReviewRequest(r.Context(), id); err != nil {
		internalError(w, "store.dismiss_review_request", err)
		return
	}
	a.hub.Broadcast(rr.DocumentID, "reviews-updated")
	w.WriteHeader(http.StatusNoContent)
}

// notifyReviewRequest sends the "X requested your review" bell
// notification. Best-effort background write, same shape as the
// comment fan-out.
func (a *API) notifyReviewRequest(rr *models.ReviewRequest, actor *models.User) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		n := &models.Notification{
			ID:             uuid.NewString(),
			UserID:         rr.ReviewerUserID,
			Kind:           models.NotifyReviewRequest,
			DocumentID:     rr.DocumentID,
			DocumentTitle:  rr.DocumentTitle,
			ActorID:        actor.ID,
			ActorName:      preferName(actor),
			ActorAvatarURL: actor.AvatarURL,
			Preview:        "requested your review",
			CreatedAt:      time.Now().UTC(),
		}
		if err := a.store.InsertNotification(ctx, n); err != nil {
			_, _ = httperr.Log("notifications.review_request", err)
		}
	}()
}

// fanOutReviewStateNotifications runs after a review state lands:
// notifies requesters whose pending requests just completed, and the
// doc owner (when they aren't the reviewer). Best-effort background.
func (a *API) fanOutReviewStateNotifications(doc *models.Document, reviewer *models.User, state models.ReviewState, completed []models.ReviewRequest) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		verb := map[models.ReviewState]string{
			models.ReviewStateApproved:         "approved this document",
			models.ReviewStateChangesRequested: "requested changes",
			models.ReviewStateCommented:        "left a review comment",
		}[state]

		recipients := map[string]struct{}{}
		for _, rr := range completed {
			if rr.RequesterID != "" && rr.RequesterID != reviewer.ID {
				recipients[rr.RequesterID] = struct{}{}
			}
		}
		if doc.CreatedByID != "" && doc.CreatedByID != reviewer.ID {
			recipients[doc.CreatedByID] = struct{}{}
		}

		now := time.Now().UTC()
		for uid := range recipients {
			n := &models.Notification{
				ID:             uuid.NewString(),
				UserID:         uid,
				Kind:           models.NotifyReviewState,
				DocumentID:     doc.ID,
				DocumentTitle:  doc.Title,
				ActorID:        reviewer.ID,
				ActorName:      preferName(reviewer),
				ActorAvatarURL: reviewer.AvatarURL,
				Preview:        verb,
				CreatedAt:      now,
			}
			if err := a.store.InsertNotification(ctx, n); err != nil {
				_, _ = httperr.Log("notifications.review_state", err)
			}
		}
	}()
}
