package api

// Index-level check-policy application: the index CREATOR maps
// filename patterns to their named policies ("anything with _PRD →
// PRD Standard"), applies them across the index in one click, and
// declares per-file exceptions. New docs matching a pattern get the
// policy on first open, so the mapping stays true over time.
//
// Scoping (deliberate, per the free-for-all threat model): only the
// index creator sees or edits an index's rules, rules can only point
// at templates the creator OWNS, and Apply/auto-apply never replace
// a policy a doc already has — a doc's existing checks (or a fork)
// always win over pattern matching.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"markupmarkdown/internal/httperr"
	"markupmarkdown/internal/models"
)

// indexPolicyRuleset is the creator-facing GET/PUT payload.
type indexPolicyRuleset struct {
	Rules []models.IndexPolicyRule `json:"rules"`
}

// requireIndexCreator loads the index and verifies the caller created
// it. Non-creators get 404 (don't confirm the surface exists).
func (a *API) requireIndexCreator(w http.ResponseWriter, r *http.Request) (*models.Index, *models.User) {
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return nil, nil
	}
	idx, err := a.store.GetIndex(r.Context(), mux.Vars(r)["id"])
	if err != nil {
		internalError(w, "store.get_index", err)
		return nil, nil
	}
	if idx == nil || idx.CreatedByID != user.ID {
		writeError(w, http.StatusNotFound, "not found")
		return nil, nil
	}
	return idx, user
}

// getIndexPolicyRules is GET /api/indexes/{id}/policy-rules.
func (a *API) getIndexPolicyRules(w http.ResponseWriter, r *http.Request) {
	idx, _ := a.requireIndexCreator(w, r)
	if idx == nil {
		return
	}
	rules := idx.PolicyRules
	if rules == nil {
		rules = []models.IndexPolicyRule{}
	}
	writeJSON(w, http.StatusOK, indexPolicyRuleset{Rules: rules})
}

// putIndexPolicyRules is PUT /api/indexes/{id}/policy-rules —
// replaces the mapping set. Every referenced template must be owned
// by the caller.
func (a *API) putIndexPolicyRules(w http.ResponseWriter, r *http.Request) {
	idx, user := a.requireIndexCreator(w, r)
	if idx == nil {
		return
	}
	capBody(w, r, maxBodyDefault)
	var req indexPolicyRuleset
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Rules) > 20 {
		writeError(w, http.StatusBadRequest, "too many rules (max 20)")
		return
	}
	for i := range req.Rules {
		rule := &req.Rules[i]
		rule.Pattern = strings.TrimSpace(rule.Pattern)
		if rule.Pattern == "" || len(rule.Pattern) > 100 {
			writeError(w, http.StatusBadRequest, "each rule needs a pattern (max 100 chars)")
			return
		}
		t, err := a.store.GetCheckTemplate(r.Context(), user.ID, rule.TemplateID)
		if err != nil || t == nil {
			writeError(w, http.StatusBadRequest, "each rule must reference a policy you own")
			return
		}
		if rule.ID == "" {
			rule.ID = uuid.NewString()
		}
		for j, ex := range rule.Exceptions {
			rule.Exceptions[j] = strings.ToLower(strings.TrimSpace(ex))
		}
	}
	if err := a.store.SetIndexPolicyRules(r.Context(), idx.ID, req.Rules); err != nil {
		internalError(w, "store.set_index_policy_rules", err)
		return
	}
	writeJSON(w, http.StatusOK, indexPolicyRuleset{Rules: req.Rules})
}

// applyIndexPolicyRules is POST /api/indexes/{id}/policy-rules/apply.
// Walks the cached index items, matches patterns, and links every
// matched EXISTING doc whose chain has no policy yet. Docs not yet
// opened in markupmarkdown are reported as pending — they'll link on
// first open via the auto-apply hook.
func (a *API) applyIndexPolicyRules(w http.ResponseWriter, r *http.Request) {
	idx, user := a.requireIndexCreator(w, r)
	if idx == nil {
		return
	}
	if len(idx.PolicyRules) == 0 {
		writeError(w, http.StatusBadRequest, "no policy rules configured on this index")
		return
	}
	cached, err := a.store.GetCachedIndexItems(r.Context(), idx.ID)
	if err != nil || cached == nil {
		writeError(w, http.StatusConflict, "index hasn't been scanned yet — open it once first")
		return
	}
	var items []indexItem
	if err := json.Unmarshal(cached.ItemsJSON, &items); err != nil {
		internalError(w, "index_policies.decode_items", err)
		return
	}

	type skipped struct {
		Path   string `json:"path"`
		Reason string `json:"reason"`
	}
	linked := []string{}
	skippedOut := []skipped{}
	pending := 0

	for _, item := range items {
		owner, repo, ref, path, ok := parseGitHubBlobURL(item.URL)
		if !ok {
			continue
		}
		rule := matchPolicyRule(idx.PolicyRules, pathKey(owner, repo, path), path)
		if rule == nil {
			continue
		}
		doc, err := a.store.FindLatestDocumentBySource(r.Context(), owner, repo, ref, path)
		if err != nil {
			continue
		}
		if doc == nil {
			pending++
			continue
		}
		rootID := a.chainRootID(r.Context(), doc)
		if existing, _ := a.store.GetCheckPolicy(r.Context(), rootID); existing != nil {
			if existing.TemplateID == rule.TemplateID {
				continue // already following this policy — nothing to report
			}
			skippedOut = append(skippedOut, skipped{Path: path, Reason: "already has checks"})
			continue
		}
		if err := a.store.UpsertCheckPolicy(r.Context(), &models.CheckPolicy{
			RootDocumentID: rootID,
			TemplateID:     rule.TemplateID,
			UpdatedByID:    user.ID,
		}); err != nil {
			skippedOut = append(skippedOut, skipped{Path: path, Reason: "link failed"})
			continue
		}
		a.hub.Broadcast(doc.ID, "reviews-updated")
		linked = append(linked, path)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"linked":  linked,
		"skipped": skippedOut,
		"pending": pending,
	})
}

// matchPolicyRule returns the first rule whose pattern matches the
// path (case-insensitive substring), unless the file is an exception.
func matchPolicyRule(rules []models.IndexPolicyRule, key, path string) *models.IndexPolicyRule {
	lowerPath := strings.ToLower(path)
	for i := range rules {
		r := &rules[i]
		if !strings.Contains(lowerPath, strings.ToLower(r.Pattern)) {
			continue
		}
		excepted := false
		for _, ex := range r.Exceptions {
			if ex == key {
				excepted = true
				break
			}
		}
		if excepted {
			continue
		}
		return r
	}
	return nil
}

func pathKey(owner, repo, path string) string {
	return strings.ToLower(owner + "/" + repo + "/" + path)
}

// maybeAutoApplyIndexPolicies runs after a github-blob doc is created:
// if an index with policy rules covers this repo and a pattern
// matches, the doc links to the rule's policy on first open — the
// "anything with _PRD stays true over time" behavior. Never replaces
// an existing policy. Best-effort background.
func (a *API) maybeAutoApplyIndexPolicies(doc *models.Document) {
	if doc == nil || doc.GitHubOwner == "" || doc.GitHubRepo == "" || doc.GitHubPath == "" {
		return
	}
	d := *doc
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		idxs, err := a.store.FindIndexesWithPolicyRules(ctx, d.GitHubOwner, d.GitHubRepo)
		if err != nil || len(idxs) == 0 {
			return
		}
		key := pathKey(d.GitHubOwner, d.GitHubRepo, d.GitHubPath)
		for _, idx := range idxs {
			rule := matchPolicyRule(idx.PolicyRules, key, d.GitHubPath)
			if rule == nil {
				continue
			}
			// The template must still exist AND belong to the index
			// creator — a rule can never smuggle someone else's policy.
			t, err := a.store.GetCheckTemplate(ctx, idx.CreatedByID, rule.TemplateID)
			if err != nil || t == nil {
				continue
			}
			rootID := d.ID
			if d.ParentID != "" {
				if root, _ := a.store.RootDocument(ctx, d.ID); root != nil {
					rootID = root.ID
				}
			}
			if existing, _ := a.store.GetCheckPolicy(ctx, rootID); existing != nil {
				return // doc already governed — existing checks always win
			}
			if err := a.store.UpsertCheckPolicy(ctx, &models.CheckPolicy{
				RootDocumentID: rootID,
				TemplateID:     rule.TemplateID,
				UpdatedByID:    idx.CreatedByID,
			}); err != nil {
				_, _ = httperr.Log("index_policies.auto_apply", err)
				return
			}
			a.hub.Broadcast(d.ID, "reviews-updated")
			return // first matching index+rule wins
		}
	}()
}
