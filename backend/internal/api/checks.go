package api

// Doc checks: deterministic lint rules for a revision chain, rendered
// like CI checks next to the review chips. Rules live on the chain
// root (same anchoring as review subscriptions); results are computed
// on demand — regex over content is cheap, and never-stored results
// can't go stale. Advisory for now: failing checks inform reviewers,
// they don't (yet) gate the push.

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"markupmarkdown/internal/models"
)

const (
	maxCheckRules      = 20
	maxCheckPatternLen = 500
	maxCheckSections   = 30
)

// evaluateChecks runs every rule against the content.
func evaluateChecks(rules []models.CheckRule, content string) []models.CheckResult {
	headings := extractHeadings(content)
	out := make([]models.CheckResult, 0, len(rules))
	for _, r := range rules {
		res := models.CheckResult{RuleID: r.ID, Label: r.Label, Kind: r.Kind}
		switch r.Kind {
		case "required_sections":
			var missing []string
			for _, want := range r.Sections {
				found := false
				for _, h := range headings {
					if strings.EqualFold(strings.TrimSpace(h.text), strings.TrimSpace(want)) {
						found = true
						break
					}
				}
				if !found {
					missing = append(missing, want)
				}
			}
			res.Pass = len(missing) == 0
			if !res.Pass {
				res.Detail = "missing: " + strings.Join(missing, ", ")
			}
		case "forbidden_text":
			re, err := compileCheckPattern(r.Pattern)
			if err != nil {
				res.Detail = err.Error()
				break
			}
			if locs := re.FindAllStringIndex(content, 4); len(locs) > 0 {
				res.Detail = fmt.Sprintf("found %d match(es) of %q", len(locs), r.Pattern)
			} else {
				res.Pass = true
			}
		case "required_text":
			re, err := compileCheckPattern(r.Pattern)
			if err != nil {
				res.Detail = err.Error()
				break
			}
			if re.MatchString(content) {
				res.Pass = true
			} else {
				res.Detail = fmt.Sprintf("no match for %q", r.Pattern)
			}
		case "max_heading_depth":
			depth := 0
			var worst string
			for _, h := range headings {
				if h.level > depth {
					depth = h.level
					worst = h.text
				}
			}
			if r.MaxDepth > 0 && depth > r.MaxDepth {
				res.Detail = fmt.Sprintf("heading %q is level %d (max %d)", worst, depth, r.MaxDepth)
			} else {
				res.Pass = true
			}
		case "forbidden_phrases":
			lower := strings.ToLower(content)
			var found []string
			for _, ph := range r.Phrases {
				if ph != "" && strings.Contains(lower, strings.ToLower(ph)) {
					found = append(found, ph)
				}
			}
			res.Pass = len(found) == 0
			if !res.Pass {
				res.Detail = "found: " + strings.Join(found, ", ")
			}
		case "term_spelling":
			re, err := regexp.Compile(`(?i)` + wordBoundary(r.Term))
			if err != nil {
				res.Detail = "invalid term"
				break
			}
			wrong := map[string]struct{}{}
			for _, m := range re.FindAllString(content, -1) {
				if m != r.Term {
					wrong[m] = struct{}{}
				}
			}
			res.Pass = len(wrong) == 0
			if !res.Pass {
				forms := make([]string, 0, len(wrong))
				for w := range wrong {
					forms = append(forms, w)
				}
				sortStrings(forms)
				res.Detail = fmt.Sprintf("should be %q — found %s", r.Term, strings.Join(forms, ", "))
			}
		default:
			res.Detail = "unknown rule kind"
		}
		out = append(out, res)
	}
	return out
}

func compileCheckPattern(p string) (*regexp.Regexp, error) {
	if strings.TrimSpace(p) == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %v", err)
	}
	return re, nil
}

type headingLine struct {
	level int
	text  string
}

// extractHeadings pulls ATX headings from markdown, skipping fenced
// code blocks so a `# comment` inside ``` doesn't count.
func extractHeadings(content string) []headingLine {
	var out []headingLine
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(trimmed, "#") {
			continue
		}
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level > 6 || level >= len(trimmed) || trimmed[level] != ' ' {
			continue
		}
		out = append(out, headingLine{level: level, text: strings.TrimSpace(trimmed[level:])})
	}
	return out
}

// validateCheckRules normalizes + validates a submitted rule set.
func validateCheckRules(rules []models.CheckRule) ([]models.CheckRule, error) {
	if len(rules) > maxCheckRules {
		return nil, fmt.Errorf("too many rules (max %d)", maxCheckRules)
	}
	out := make([]models.CheckRule, 0, len(rules))
	for i, r := range rules {
		r.Label = strings.TrimSpace(r.Label)
		if r.Label == "" {
			return nil, fmt.Errorf("rule %d: label is required", i+1)
		}
		if len(r.Label) > 60 {
			return nil, fmt.Errorf("rule %d: label too long (max 60)", i+1)
		}
		if r.ID == "" {
			r.ID = uuid.NewString()
		}
		switch r.Kind {
		case "required_sections":
			if len(r.Sections) == 0 || len(r.Sections) > maxCheckSections {
				return nil, fmt.Errorf("rule %q: needs 1-%d sections", r.Label, maxCheckSections)
			}
			r.Pattern, r.MaxDepth = "", 0
		case "forbidden_text", "required_text":
			if len(r.Pattern) > maxCheckPatternLen {
				return nil, fmt.Errorf("rule %q: pattern too long (max %d)", r.Label, maxCheckPatternLen)
			}
			if _, err := compileCheckPattern(r.Pattern); err != nil {
				return nil, fmt.Errorf("rule %q: %v", r.Label, err)
			}
			r.Sections, r.MaxDepth = nil, 0
		case "max_heading_depth":
			if r.MaxDepth < 1 || r.MaxDepth > 6 {
				return nil, fmt.Errorf("rule %q: maxDepth must be 1-6", r.Label)
			}
			r.Sections, r.Pattern = nil, ""
		case "forbidden_phrases":
			var clean []string
			for _, ph := range r.Phrases {
				ph = strings.TrimSpace(ph)
				if ph == "" {
					continue
				}
				if len(ph) > 100 {
					return nil, fmt.Errorf("rule %q: phrase too long (max 100)", r.Label)
				}
				clean = append(clean, ph)
			}
			if len(clean) == 0 || len(clean) > maxCheckSections {
				return nil, fmt.Errorf("rule %q: needs 1-%d phrases", r.Label, maxCheckSections)
			}
			r.Phrases = clean
			r.Sections, r.Pattern, r.MaxDepth = nil, "", 0
		case "term_spelling":
			r.Term = strings.TrimSpace(r.Term)
			if len(r.Term) < 2 || len(r.Term) > 60 {
				return nil, fmt.Errorf("rule %q: term must be 2-60 characters", r.Label)
			}
			r.Sections, r.Pattern, r.MaxDepth, r.Phrases = nil, "", 0, nil
		default:
			return nil, fmt.Errorf("rule %q: unknown kind %q", r.Label, r.Kind)
		}
		out = append(out, r)
	}
	return out, nil
}

// getDocChecks is GET /api/documents/:id/checks — the policy's rules
// evaluated against THIS doc's content. 200 with hasPolicy=false when
// no policy exists (the UI hides the row).
func (a *API) getDocChecks(w http.ResponseWriter, r *http.Request) {
	docID := mux.Vars(r)["id"]
	doc, accErr := a.checkDocAccess(r, docID)
	if accErr != nil {
		a.writeAccessError(w, r, accErr)
		return
	}
	rules, _, err := a.resolvedCheckRules(r, doc)
	if err != nil {
		internalError(w, "store.get_check_policy", err)
		return
	}
	if len(rules) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"hasPolicy": false, "results": []models.CheckResult{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hasPolicy": true,
		"results":   evaluateChecks(rules, doc.Content),
	})
}

// resolvedCheckRules loads the chain's policy and, when linked to a
// template, resolves the template's CURRENT rules — the mechanism
// that makes editing a named policy update every linked doc.
func (a *API) resolvedCheckRules(r *http.Request, doc *models.Document) ([]models.CheckRule, *models.CheckPolicy, error) {
	policy, err := a.store.GetCheckPolicy(r.Context(), a.chainRootID(r.Context(), doc))
	if err != nil || policy == nil {
		return nil, policy, err
	}
	if policy.TemplateID != "" {
		t, err := a.store.GetCheckTemplateAny(r.Context(), policy.TemplateID)
		if err != nil {
			return nil, policy, err
		}
		if t != nil {
			return t.Rules, policy, nil
		}
		// Dangling link (template deleted without materialize — should
		// not happen, but fail soft to the inline rules).
	}
	return policy.Rules, policy, nil
}

// getCheckPolicy is GET /api/documents/:id/check-policy — the raw
// rule set, for the editor UI.
func (a *API) getCheckPolicy(w http.ResponseWriter, r *http.Request) {
	docID := mux.Vars(r)["id"]
	doc, accErr := a.checkDocAccess(r, docID)
	if accErr != nil {
		a.writeAccessError(w, r, accErr)
		return
	}
	rules, policy, err := a.resolvedCheckRules(r, doc)
	if err != nil {
		internalError(w, "store.get_check_policy", err)
		return
	}
	out := map[string]any{"rules": []models.CheckRule{}}
	if rules != nil {
		out["rules"] = rules
	}
	if policy != nil && policy.TemplateID != "" {
		out["templateId"] = policy.TemplateID
		if t, _ := a.store.GetCheckTemplateAny(r.Context(), policy.TemplateID); t != nil {
			out["templateName"] = t.Name
		}
		if n, err := a.store.CountPoliciesUsingTemplate(r.Context(), policy.TemplateID); err == nil {
			out["docsUsingTemplate"] = n
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// putCheckPolicy is PUT /api/documents/:id/check-policy — replaces
// the chain's rule set. An empty rules array deletes the policy.
func (a *API) putCheckPolicy(w http.ResponseWriter, r *http.Request) {
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
	capBody(w, r, maxBodyDefault)
	var req struct {
		Rules      []models.CheckRule `json:"rules"`
		TemplateID string             `json:"templateId,omitempty"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rootID := a.chainRootID(r.Context(), doc)
	// Link mode: attach this chain to a named policy (must be yours).
	if req.TemplateID != "" {
		t, err := a.store.GetCheckTemplate(r.Context(), user.ID, req.TemplateID)
		if err != nil || t == nil {
			writeError(w, http.StatusNotFound, "no policy of yours with that id")
			return
		}
		policy := &models.CheckPolicy{
			RootDocumentID: rootID,
			TemplateID:     t.ID,
			Rules:          nil,
			UpdatedByID:    user.ID,
		}
		if err := a.store.UpsertCheckPolicy(r.Context(), policy); err != nil {
			internalError(w, "store.link_check_policy", err)
			return
		}
		a.hub.Broadcast(doc.ID, "reviews-updated")
		writeJSON(w, http.StatusOK, map[string]any{
			"templateId": t.ID, "templateName": t.Name, "rules": t.Rules,
		})
		return
	}
	if len(req.Rules) == 0 {
		if err := a.store.DeleteCheckPolicy(r.Context(), rootID); err != nil {
			internalError(w, "store.delete_check_policy", err)
			return
		}
		a.hub.Broadcast(doc.ID, "reviews-updated")
		writeJSON(w, http.StatusOK, map[string]any{"rules": []models.CheckRule{}})
		return
	}
	rules, err := validateCheckRules(req.Rules)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	policy := &models.CheckPolicy{
		RootDocumentID: rootID,
		Rules:          rules,
		UpdatedByID:    user.ID,
	}
	if err := a.store.UpsertCheckPolicy(r.Context(), policy); err != nil {
		internalError(w, "store.upsert_check_policy", err)
		return
	}
	a.hub.Broadcast(doc.ID, "reviews-updated")
	writeJSON(w, http.StatusOK, policy)
}

// wordBoundary wraps a literal term with word boundaries where the
// term's edges are word characters (\b next to punctuation never
// matches, so only add it where it can).
func wordBoundary(term string) string {
	q := regexp.QuoteMeta(term)
	if term == "" {
		return q
	}
	if isWordChar(rune(term[0])) {
		q = `\b` + q
	}
	if isWordChar(rune(term[len(term)-1])) {
		q += `\b`
	}
	return q
}

func isWordChar(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func sortStrings(s []string) { sort.Strings(s) }

// previewDocChecks is POST /api/documents/:id/check-preview — runs a
// candidate rule set against the doc WITHOUT saving, so the editor
// can show live pass/fail while the user composes. Per-rule
// validation: one broken rule reports its own error instead of
// blocking the preview of the others.
func (a *API) previewDocChecks(w http.ResponseWriter, r *http.Request) {
	docID := mux.Vars(r)["id"]
	doc, accErr := a.checkDocAccess(r, docID)
	if accErr != nil {
		a.writeAccessError(w, r, accErr)
		return
	}
	if a.currentUser(r) == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	capBody(w, r, maxBodyDefault)
	var req struct {
		Rules []models.CheckRule `json:"rules"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Rules) > maxCheckRules {
		writeError(w, http.StatusBadRequest, "too many rules")
		return
	}
	out := make([]models.CheckResult, 0, len(req.Rules))
	for i, rule := range req.Rules {
		valid, err := validateCheckRules([]models.CheckRule{rule})
		if err != nil {
			out = append(out, models.CheckResult{
				RuleID: rule.ID, Label: rule.Label, Kind: rule.Kind,
				Pass: false, Detail: err.Error(),
			})
			continue
		}
		res := evaluateChecks(valid, doc.Content)[0]
		if res.RuleID == "" {
			res.RuleID = fmt.Sprintf("preview-%d", i)
		}
		out = append(out, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// listCheckTemplates is GET /api/me/check-templates.
func (a *API) listCheckTemplates(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	ts, err := a.store.ListCheckTemplatesForUser(r.Context(), user.ID)
	if err != nil {
		internalError(w, "store.list_check_templates", err)
		return
	}
	out := make([]map[string]any, 0, len(ts))
	for _, t := range ts {
		n, _ := a.store.CountPoliciesUsingTemplate(r.Context(), t.ID)
		out = append(out, map[string]any{
			"id": t.ID, "name": t.Name, "rules": t.Rules,
			"updatedAt": t.UpdatedAt, "docsUsing": n,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type checkTemplateRequest struct {
	Name  string             `json:"name"`
	Rules []models.CheckRule `json:"rules"`
}

// createCheckTemplate is POST /api/me/check-templates.
func (a *API) createCheckTemplate(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	if !a.enforceScope(w, r, models.TokenScopeAdmin) {
		return
	}
	capBody(w, r, maxBodyDefault)
	var req checkTemplateRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 60 {
		writeError(w, http.StatusBadRequest, "name is required (max 60 chars)")
		return
	}
	rules, err := validateCheckRules(req.Rules)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(rules) == 0 {
		writeError(w, http.StatusBadRequest, "a policy needs at least one rule")
		return
	}
	t := &models.CheckTemplate{
		ID:      uuid.NewString(),
		Name:    name,
		OwnerID: user.ID,
		Rules:   rules,
	}
	if err := a.store.UpsertCheckTemplate(r.Context(), t); err != nil {
		internalError(w, "store.create_check_template", err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

// updateCheckTemplate is PUT /api/me/check-templates/{id} — edits
// propagate to every linked doc by construction (they resolve the
// template at read time).
func (a *API) updateCheckTemplate(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	if !a.enforceScope(w, r, models.TokenScopeAdmin) {
		return
	}
	capBody(w, r, maxBodyDefault)
	id := mux.Vars(r)["id"]
	existing, err := a.store.GetCheckTemplate(r.Context(), user.ID, id)
	if err != nil || existing == nil {
		writeError(w, http.StatusNotFound, "no policy of yours with that id")
		return
	}
	var req checkTemplateRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		if len(name) > 60 {
			writeError(w, http.StatusBadRequest, "name too long (max 60)")
			return
		}
		existing.Name = name
	}
	if req.Rules != nil {
		rules, err := validateCheckRules(req.Rules)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(rules) == 0 {
			writeError(w, http.StatusBadRequest, "a policy needs at least one rule")
			return
		}
		existing.Rules = rules
	}
	if err := a.store.UpsertCheckTemplate(r.Context(), existing); err != nil {
		internalError(w, "store.update_check_template", err)
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

// deleteCheckTemplate is DELETE /api/me/check-templates/{id}. Linked
// docs keep working: their policies get the rules materialized inline
// before the template goes away.
func (a *API) deleteCheckTemplate(w http.ResponseWriter, r *http.Request) {
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	if !a.enforceScope(w, r, models.TokenScopeAdmin) {
		return
	}
	id := mux.Vars(r)["id"]
	t, err := a.store.GetCheckTemplate(r.Context(), user.ID, id)
	if err != nil || t == nil {
		writeError(w, http.StatusNotFound, "no policy of yours with that id")
		return
	}
	if err := a.store.MaterializeTemplateIntoPolicies(r.Context(), t); err != nil {
		internalError(w, "store.materialize_template", err)
		return
	}
	if err := a.store.DeleteCheckTemplate(r.Context(), user.ID, id); err != nil {
		internalError(w, "store.delete_check_template", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
