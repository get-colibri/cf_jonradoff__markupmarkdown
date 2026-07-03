package api

// One-click apply for structured suggestions on anchored comments
// (P0-2). Empirically the highest-actionability review artifact in
// the doc-collaboration prior art (Brown & Parnin ESEC/FSE '20). A
// suggestion carries a replacement string; applying it creates a
// manual revision that swaps the comment's Anchor.Exact for the
// replacement, then resolves the comment. Doc-level comments (no
// anchor) can't carry suggestions — there's nothing to replace.

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"go.mongodb.org/mongo-driver/v2/bson"

	"markupmarkdown/internal/models"
)

// applySuggestion is POST /api/comments/:id/apply-suggestion. Reads
// the comment's Suggestion, replaces the first occurrence of
// Anchor.Exact in the doc content with Suggestion.Replacement, and
// creates a manual revision + resolves the comment. Idempotent-ish:
// once a suggestion is stamped applied (AppliedAt), a second call is
// a 409 (already applied) so double-clicks don't create duplicate
// revisions.
func (a *API) applySuggestion(w http.ResponseWriter, r *http.Request) {
	commentID := mux.Vars(r)["id"]
	comment, doc, accErr := a.checkCommentAccess(r, commentID)
	if accErr != nil {
		a.writeAccessError(w, r, accErr)
		return
	}
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	// Applying a suggestion creates a new doc — same admin bar as
	// acceptRevision and createManualRevision.
	if !a.enforceScope(w, r, models.TokenScopeAdmin) {
		return
	}
	if comment.Suggestion == nil {
		writeError(w, http.StatusBadRequest, "this comment has no suggestion attached")
		return
	}
	if comment.Suggestion.AppliedAt != nil {
		writeError(w, http.StatusConflict, "this suggestion was already applied")
		return
	}
	if comment.Anchor.Exact == "" {
		writeError(w, http.StatusBadRequest, "doc-level comments can't carry suggestions — there's nothing to replace")
		return
	}

	// Substitution against the source. First-occurrence replace on
	// the exact anchored span, same primitive stat federation of
	// GitHub's suggested changes uses.
	original := comment.Anchor.Exact
	replacement := comment.Suggestion.Replacement
	if !strings.Contains(doc.Content, original) {
		writeError(w, http.StatusUnprocessableEntity,
			"the anchored text no longer appears in the doc — re-anchor or re-write the suggestion")
		return
	}
	// Reject no-op replacements — nothing to commit.
	if original == replacement {
		writeError(w, http.StatusBadRequest, "suggestion replacement is identical to the anchored text")
		return
	}
	newContent := strings.Replace(doc.Content, original, replacement, 1)
	if !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}

	// New child doc — mirrors createManualRevision's shape. Author
	// name and actor kind reflect the *applier* (usually a human
	// clicking Apply), not the suggestion's original author.
	now := time.Now().UTC()
	authorName := user.Name
	if authorName == "" {
		authorName = user.Login
	}
	childID := uuid.NewString()
	child := &models.Document{
		ID:          childID,
		Title:       doc.Title,
		Origin:      doc.Origin,
		SourceURL:   doc.SourceURL,
		SourceKind:  doc.SourceKind,
		Content:     newContent,
		Private:     doc.Private,
		GitHubOwner: doc.GitHubOwner,
		GitHubRepo:  doc.GitHubRepo,
		GitHubRef:   doc.GitHubRef,
		GitHubPath:  doc.GitHubPath,
		SourceSHA:   doc.SourceSHA,
		ParentID:    doc.ID,
		CreatedByID: user.ID,
		RevisionMeta: &models.RevisionMeta{
			Model:             "suggestion",
			GeneratedBy:       authorName,
			GeneratedByID:     user.ID,
			GeneratedAt:       now,
			ActorKind:         actorKindFor(r),
			AncestorSourceSHA: doc.SourceSHA,
			AncestorContent:   doc.Content,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if info, ok := tokenInfoFromRequest(r); ok {
		child.RevisionMeta.TokenID = info.TokenID
	}
	if err := a.store.InsertDocument(r.Context(), child); err != nil {
		internalError(w, "store.insert_suggestion_child", err)
		return
	}

	// Carry unresolved comments (same primitive as manual revision).
	// The applied comment itself gets marked resolved BEFORE the
	// carry, so it doesn't ride along.
	if err := a.stampSuggestionApplied(r.Context(), comment.ID, user, authorName, child.ID); err != nil {
		// Don't roll back the child — the revision is real; the
		// stamp is metadata. Log and continue.
		internalError(w, "store.stamp_suggestion_applied", err)
		return
	}
	if carried := a.copyOpenCommentsToChild(r.Context(), doc.ID, child); carried > 0 {
		a.hub.Broadcast(child.ID, "comments-updated")
	}
	// Track the token action for observability.
	if info, ok := tokenInfoFromRequest(r); ok {
		a.logTokenAction(r.Context(), info.TokenID, "suggestion.apply", child.ID)
	}
	a.hub.Broadcast(doc.ID, "doc-updated")

		// Summon the chain's standing reviewers onto the new revision.
	authorTok := ""
	if info, ok := tokenInfoFromRequest(r); ok {
		authorTok = info.TokenID
	}
	a.fanOutRevisionEvents(child, user.ID, authorTok, authorName)

	writeJSON(w, http.StatusCreated, child)
}

// stampSuggestionApplied flips the suggestion's applied fields AND
// resolves the comment atomically. The comment stays on the parent
// (where it was written) — the "applied" state travels with the
// carry-forward pipeline via the standard resolve semantics.
func (a *API) stampSuggestionApplied(ctx context.Context, commentID string, user *models.User, appliedBy, childID string) error {
	now := time.Now().UTC()
	_, err := a.store.Comments().UpdateOne(ctx,
		bson.M{"_id": commentID},
		bson.M{"$set": bson.M{
			"suggestion.applied_at":     now,
			"suggestion.applied_by_id":  user.ID,
			"suggestion.applied_by":     appliedBy,
			"suggestion.applied_doc_id": childID,
			"resolved":                  true,
			"resolved_by":               appliedBy,
			"resolved_at":               now,
			"updated_at":                now,
		}})
	return err
}

// applyAllSuggestions is POST /api/documents/:id/apply-suggestions —
// applies every open suggestion on the doc in ONE new revision.
// Replacements run top-down in document order against a working copy;
// a suggestion whose anchor no longer matches (earlier replacement ate
// it, or the text changed) is skipped and reported, never guessed.
func (a *API) applyAllSuggestions(w http.ResponseWriter, r *http.Request) {
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
	if !a.enforceScope(w, r, models.TokenScopeAdmin) {
		return
	}

	comments, err := a.store.ListComments(r.Context(), doc.ID)
	if err != nil {
		internalError(w, "store.list_comments_for_batch_apply", err)
		return
	}
	type candidate struct {
		c   models.Comment
		pos int
	}
	var cands []candidate
	for _, c := range comments {
		if c.Resolved || c.Suggestion == nil || c.Suggestion.AppliedAt != nil {
			continue
		}
		if c.Anchor.Exact == "" || c.Anchor.Exact == c.Suggestion.Replacement {
			continue
		}
		pos := strings.Index(doc.Content, c.Anchor.Exact)
		cands = append(cands, candidate{c: c, pos: pos})
	}
	if len(cands) == 0 {
		writeError(w, http.StatusBadRequest, "no open suggestions to apply on this document")
		return
	}
	// Document order: anchors found earlier apply first; unlocatable
	// anchors (pos == -1) sort last and get skipped below.
	sortCandidates(cands, func(i, j int) bool {
		pi, pj := cands[i].pos, cands[j].pos
		if pi < 0 {
			return false
		}
		if pj < 0 {
			return true
		}
		return pi < pj
	})

	working := doc.Content
	var applied []models.Comment
	type skippedItem struct {
		CommentID string `json:"commentId"`
		Reason    string `json:"reason"`
	}
	var skipped []skippedItem
	for _, cand := range cands {
		exact := cand.c.Anchor.Exact
		if !strings.Contains(working, exact) {
			skipped = append(skipped, skippedItem{
				CommentID: cand.c.ID,
				Reason:    "anchored text not found (changed or consumed by an earlier suggestion)",
			})
			continue
		}
		working = strings.Replace(working, exact, cand.c.Suggestion.Replacement, 1)
		applied = append(applied, cand.c)
	}
	if len(applied) == 0 {
		writeError(w, http.StatusUnprocessableEntity,
			"none of the suggestions' anchors match the current content")
		return
	}
	if !strings.HasSuffix(working, "\n") {
		working += "\n"
	}

	now := time.Now().UTC()
	authorName := user.Name
	if authorName == "" {
		authorName = user.Login
	}
	child := &models.Document{
		ID:          uuid.NewString(),
		Title:       doc.Title,
		Origin:      doc.Origin,
		SourceURL:   doc.SourceURL,
		SourceKind:  doc.SourceKind,
		Content:     working,
		Private:     doc.Private,
		GitHubOwner: doc.GitHubOwner,
		GitHubRepo:  doc.GitHubRepo,
		GitHubRef:   doc.GitHubRef,
		GitHubPath:  doc.GitHubPath,
		SourceSHA:   doc.SourceSHA,
		ParentID:    doc.ID,
		CreatedByID: user.ID,
		RevisionMeta: &models.RevisionMeta{
			Model:             "suggestion",
			GeneratedBy:       authorName,
			GeneratedByID:     user.ID,
			GeneratedAt:       now,
			ActorKind:         actorKindFor(r),
			AncestorSourceSHA: doc.SourceSHA,
			AncestorContent:   doc.Content,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if info, ok := tokenInfoFromRequest(r); ok {
		child.RevisionMeta.TokenID = info.TokenID
	}
	if err := a.store.InsertDocument(r.Context(), child); err != nil {
		internalError(w, "store.insert_batch_suggestion_child", err)
		return
	}

	// Stamp every applied suggestion resolved BEFORE the carry so they
	// don't ride to the child. Per-comment UpdateOne, never UpdateMany.
	for _, c := range applied {
		if err := a.stampSuggestionApplied(r.Context(), c.ID, user, authorName, child.ID); err != nil {
			internalError(w, "store.stamp_batch_suggestion", err)
			return
		}
	}
	if carried := a.copyOpenCommentsToChild(r.Context(), doc.ID, child); carried > 0 {
		a.hub.Broadcast(child.ID, "comments-updated")
	}
	if info, ok := tokenInfoFromRequest(r); ok {
		a.logTokenAction(r.Context(), info.TokenID, "suggestion.apply_all", child.ID)
	}
	a.hub.Broadcast(doc.ID, "doc-updated")

	authorTok := ""
	if info, ok := tokenInfoFromRequest(r); ok {
		authorTok = info.TokenID
	}
	a.fanOutRevisionEvents(child, user.ID, authorTok, authorName)

	appliedIDs := make([]string, 0, len(applied))
	for _, c := range applied {
		appliedIDs = append(appliedIDs, c.ID)
	}
	if skipped == nil {
		skipped = []skippedItem{}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"document": child,
		"applied":  appliedIDs,
		"skipped":  skipped,
	})
}

// sortCandidates is a tiny wrapper so the batch handler reads cleanly.
func sortCandidates[T any](s []T, less func(i, j int) bool) {
	sort.Slice(s, less)
}
