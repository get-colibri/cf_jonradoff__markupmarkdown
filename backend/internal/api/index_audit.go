package api

// Index-level agent audits: summon one of YOUR agent tokens to review
// every file in an index that matches a pattern — "review every _PRD
// with my auto-reviewer." Pure composition: it walks the cached index
// items and mints ordinary review requests (which auto-review tokens
// fulfill within minutes, rate-limited by the owner's revise bucket).
// Per-user by design: you can only summon tokens you own, and only
// onto docs you can access. Not creator-gated — auditing is a
// reading/commenting act, unlike the creator-owned policy rules.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"markupmarkdown/internal/models"
	"markupmarkdown/internal/store"
)

// maxAuditRequests caps one audit run. Fulfillment is rate-limited
// anyway (auto-review consults the owner's revise bucket before each
// claim), but an unbounded audit of a 500-file org index shouldn't
// mint 500 requests in one click either.
const maxAuditRequests = 50

type indexAuditRequest struct {
	TokenID string `json:"tokenId"`
	// Pattern optionally narrows the audit: case-insensitive substring
	// on the file path. Empty audits every file in the index.
	Pattern string `json:"pattern,omitempty"`
}

// auditIndex is POST /api/indexes/{id}/audit.
func (a *API) auditIndex(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	if !a.enforceScope(w, r, models.TokenScopeWrite) {
		return
	}
	idx, err := a.store.GetIndex(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		internalError(w, "store.get_index", err)
		return
	}
	if idx == nil {
		writeError(w, http.StatusNotFound, "index not found")
		return
	}
	if !a.gateIndexAccess(w, r, idx) {
		return
	}
	capBody(w, r, maxBodyDefault)
	var req indexAuditRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tok, err := a.store.GetAPITokenByID(r.Context(), user.ID, strings.TrimSpace(req.TokenID))
	if err != nil || tok == nil {
		writeError(w, http.StatusNotFound, "no active token of yours with that id")
		return
	}
	cached, err := a.store.GetCachedIndexItems(r.Context(), idx.ID)
	if err != nil || cached == nil {
		writeError(w, http.StatusConflict, "index hasn't been scanned yet — open it once first")
		return
	}
	var items []indexItem
	if err := json.Unmarshal(cached.ItemsJSON, &items); err != nil {
		internalError(w, "index_audit.decode_items", err)
		return
	}

	pattern := strings.ToLower(strings.TrimSpace(req.Pattern))
	requested := 0
	pending := 0
	capped := false
	for _, item := range items {
		owner, repo, ref, path, ok := parseGitHubBlobURL(item.URL)
		if !ok {
			continue
		}
		if pattern != "" && !strings.Contains(strings.ToLower(path), pattern) {
			continue
		}
		if requested >= maxAuditRequests {
			capped = true
			break
		}
		doc, err := a.store.FindLatestDocumentBySource(r.Context(), owner, repo, ref, path)
		if err != nil {
			continue
		}
		if doc == nil {
			pending++
			continue
		}
		// The requester must be able to access the doc — an audit can't
		// summon reviews onto private material the caller can't read.
		if _, accErr := a.checkDocAccess(r, doc.ID); accErr != nil {
			continue
		}
		rr := &models.ReviewRequest{
			ID:              store.ReviewRequestID(doc.ID, tok.ID),
			DocumentID:      doc.ID,
			DocumentTitle:   doc.Title,
			RequesterID:     user.ID,
			RequesterName:   preferName(user),
			ReviewerTokenID: tok.ID,
			ReviewerName:    tok.Label,
		}
		if err := a.store.UpsertReviewRequest(r.Context(), rr); err != nil {
			continue
		}
		a.enqueueAutoReview(rr.ID, tok.ID)
		a.hub.Broadcast(doc.ID, "reviews-updated")
		requested++
	}
	if info, ok := tokenInfoFromRequest(r); ok {
		a.logTokenAction(r.Context(), info.TokenID, "index.audit", idx.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requested": requested,
		"pending":   pending,
		"capped":    capped,
	})
}
