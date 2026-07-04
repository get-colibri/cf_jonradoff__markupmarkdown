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
	policy, err := a.store.GetCheckPolicy(r.Context(), a.chainRootID(r.Context(), doc))
	if err != nil {
		internalError(w, "store.get_check_policy", err)
		return
	}
	if policy == nil || len(policy.Rules) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"hasPolicy": false, "results": []models.CheckResult{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hasPolicy": true,
		"results":   evaluateChecks(policy.Rules, doc.Content),
	})
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
	policy, err := a.store.GetCheckPolicy(r.Context(), a.chainRootID(r.Context(), doc))
	if err != nil {
		internalError(w, "store.get_check_policy", err)
		return
	}
	if policy == nil {
		policy = &models.CheckPolicy{Rules: []models.CheckRule{}}
	}
	writeJSON(w, http.StatusOK, policy)
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
		Rules []models.CheckRule `json:"rules"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rootID := a.chainRootID(r.Context(), doc)
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
