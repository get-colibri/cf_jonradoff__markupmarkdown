package api

// Backend-fulfilled reviews for auto-review tokens. When a review
// request targets a token whose owner opted into AutoReview, the
// server performs the review itself — event-driven (enqueued the
// moment the request is minted) with a periodic sweep as crash
// recovery. Runs on the always-on Fly machine so nothing depends on
// anyone's laptop or an external cron.
//
// Everything flows through the same internal surfaces an external MCP
// agent uses (AddSuggestion, SetReviewState with the token identity),
// so agent badges, review chips, push gates, and implicit fulfillment
// behave identically. Cost sits on the token owner's stored Anthropic
// key — same billing model as Revise with AI.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"markupmarkdown/internal/ai"
	"markupmarkdown/internal/httperr"
	"markupmarkdown/internal/models"
)

func newUUID() string        { return uuid.NewString() }
func timeNowUTC() time.Time  { return time.Now().UTC() }

// autoReviewSweepInterval is the crash-recovery cadence. The fast path
// is the enqueue channel; the sweep only catches requests minted right
// before a deploy/restart.
const autoReviewSweepInterval = 10 * time.Minute

// autoReviewFn is the seam tests use to stub the Claude call.
type autoReviewFn func(ctx context.Context, apiKey, title, content string, openThreads []ai.ResolvedComment) (*ai.ReviewResult, error)

// SetAutoReviewerForTest swaps the reviewer implementation. Test-only.
func (a *API) SetAutoReviewerForTest(fn func(ctx context.Context, apiKey, title, content string, openThreads []ai.ResolvedComment) (*ai.ReviewResult, error)) {
	a.reviewFn = fn
}

// StartAutoReviewWorker launches the fulfillment goroutine. Call once
// from main after the API is constructed. Lifetime = process lifetime,
// same convention as StartPurgeSweep.
func (a *API) StartAutoReviewWorker() {
	go func() {
		ticker := time.NewTicker(autoReviewSweepInterval)
		defer ticker.Stop()
		// Sweep once at startup to drain anything minted mid-deploy.
		a.sweepAutoReviews()
		for {
			select {
			case id := <-a.autoReviewCh:
				a.processAutoReview(id)
			case <-ticker.C:
				a.sweepAutoReviews()
			}
		}
	}()
}

// enqueueAutoReview hands a freshly-minted request to the worker when
// its target token has AutoReview set. Non-blocking: if the channel is
// full the sweep picks the request up instead — never stall a write
// path on review fulfillment.
func (a *API) enqueueAutoReview(requestID, tokenID string) {
	if tokenID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(contextDetached(), 5*time.Second)
	defer cancel()
	toks, err := a.store.GetAPITokensByIDs(ctx, []string{tokenID})
	if err != nil || toks[tokenID] == nil || !toks[tokenID].AutoReview {
		return
	}
	select {
	case a.autoReviewCh <- requestID:
	default:
	}
}

func (a *API) sweepAutoReviews() {
	// context.Background, NOT contextDetached — the latter carries a
	// baked-in 5s timeout meant for quick async lookups, and a parent
	// deadline always wins over a longer child one. (Cost of learning
	// this live: one drill review that died at exactly 5s.)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pending, err := a.store.ListPendingAutoReviewCandidates(ctx)
	if err != nil {
		_, _ = httperr.Log("autoreview.sweep", err)
		return
	}
	for _, rr := range pending {
		a.processAutoReview(rr.ID)
	}
}

// processAutoReview claims and fulfills one request. Every early
// return is deliberate: wrong states are silent no-ops, transient
// conditions (rate limit) leave the claim unstamped so the sweep
// retries, and hard failures burn the daily claim so we never loop.
func (a *API) processAutoReview(requestID string) {
	// Background, not contextDetached — see sweepAutoReviews.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	rr, err := a.store.GetReviewRequest(ctx, requestID)
	if err != nil || rr == nil || rr.State != models.ReviewRequestPending || rr.ReviewerTokenID == "" {
		return
	}
	toks, err := a.store.GetAPITokensByIDs(ctx, []string{rr.ReviewerTokenID})
	if err != nil || toks[rr.ReviewerTokenID] == nil {
		return
	}
	token := toks[rr.ReviewerTokenID]
	if !token.AutoReview {
		return // external agent will poll for it
	}
	owner, err := a.store.GetUser(ctx, token.UserID)
	if err != nil || owner == nil {
		return
	}
	apiKey, err := a.decryptedAnthropicKey(ctx, owner.ID)
	if err != nil || apiKey == "" {
		// No Anthropic key → cannot fulfill. Leave pending (unclaimed)
		// so an external agent can still pick it up; the sweep will
		// retry if the owner adds a key. Cheap check, no burn risk.
		return
	}
	// Rate limit BEFORE claiming: a rate-limited attempt should retry
	// on the next sweep, not burn the daily claim.
	if !a.rlRevise.Allow("u:" + owner.ID) {
		return
	}
	won, err := a.store.ClaimAutoReviewAttempt(ctx, requestID)
	if err != nil || !won {
		return
	}

	doc, err := a.store.GetDocument(ctx, rr.DocumentID)
	if err != nil || doc == nil || doc.DeletedAt != nil {
		_ = a.store.MarkAutoReviewFailed(ctx, requestID, "document unavailable")
		return
	}

	// Open threads give the reviewer context so it doesn't repeat
	// feedback humans already left.
	var openThreads []ai.ResolvedComment
	if comments, err := a.store.ListComments(ctx, doc.ID); err == nil {
		for _, c := range comments {
			if c.Resolved {
				continue
			}
			openThreads = append(openThreads, ai.ResolvedComment{
				Quoted: c.Anchor.Exact,
				Author: c.Author,
				Body:   c.Body,
			})
			if len(openThreads) >= 20 {
				break
			}
		}
	}

	reviewFn := a.reviewFn
	if reviewFn == nil {
		reviewFn = ai.ReviewDoc
	}
	result, err := reviewFn(ctx, apiKey, doc.Title, doc.Content, openThreads)
	if err != nil {
		_, _ = httperr.Log("autoreview.claude", err)
		// Claim already burned — no retry loop on a failing doc. But
		// the failure must be VISIBLE: status + a notification, so a
		// summoned review never just silently evaporates.
		_ = a.store.MarkAutoReviewFailed(ctx, requestID, shortAIError(err))
		a.notifyAutoReview(ctx, rr, owner.ID, token.Label,
			"auto-review failed: "+shortAIError(err))
		return
	}

	// Apply suggestions through the same validated path MCP uses. A
	// suggestion whose quote isn't verbatim in the doc fails validation
	// and is dropped — the model was warned.
	applied := 0
	for _, s := range result.Suggestions {
		if applied >= 5 {
			break
		}
		body := strings.TrimSpace(s.Rationale)
		if body == "" {
			body = "Suggested change."
		}
		if _, err := a.AddSuggestion(ctx, owner.ID, doc.ID, body, s.Quoted, 1, s.Replacement, token.ID, token.Label); err != nil {
			continue
		}
		applied++
	}

	note := strings.TrimSpace(result.Note)
	if len(note) > 2000 {
		note = note[:2000]
	}
	if _, err := a.SetReviewState(ctx, owner.ID, doc.ID, result.State, note, token.ID); err != nil {
		_, _ = httperr.Log("autoreview.set_state", err)
		_ = a.store.MarkAutoReviewFailed(ctx, requestID, "couldn't record the review state")
		return
	}
	_ = a.store.MarkAutoReviewDone(ctx, requestID, result.State, applied)
	a.logTokenAction(ctx, token.ID, "review.auto", doc.ID)

	// Tell the summoner what happened — including "finished with no
	// comments". The generic review fan-out skips reviewer==requester
	// (right for humans, wrong for a bot acting on your behalf), so
	// the auto path notifies directly.
	verb := map[string]string{
		"approved":          "approved",
		"changes_requested": "requested changes",
		"commented":         "left comments",
	}[result.State]
	summary := "auto-review " + verb
	if applied > 0 {
		summary += fmt.Sprintf(" · %d suggestion(s)", applied)
	} else if result.State == "approved" {
		summary += " · no issues found"
	}
	a.notifyAutoReview(ctx, rr, owner.ID, token.Label, summary)
}

// notifyAutoReview inserts the summoner's bell notification for a
// finished (or failed) auto-run.
func (a *API) notifyAutoReview(ctx context.Context, rr *models.ReviewRequest, requesterID, tokenLabel, preview string) {
	n := &models.Notification{
		ID:            newUUID(),
		UserID:        requesterID,
		Kind:          models.NotifyAutoReview,
		DocumentID:    rr.DocumentID,
		DocumentTitle: rr.DocumentTitle,
		ActorName:     tokenLabel,
		Preview:       preview,
		CreatedAt:     timeNowUTC(),
	}
	if err := a.store.InsertNotification(ctx, n); err != nil {
		_, _ = httperr.Log("notifications.auto_review", err)
	}
}

// shortAIError trims an AI-client error to a bell-notification-sized
// human string.
func shortAIError(err error) string {
	msg := err.Error()
	if len(msg) > 140 {
		msg = msg[:140] + "…"
	}
	return msg
}

// RunAutoReviewSweepForTest runs one synchronous sweep. Test-only —
// lets integration tests drive fulfillment deterministically instead
// of racing the worker goroutine.
func (a *API) RunAutoReviewSweepForTest() { a.sweepAutoReviews() }

// StoreAnthropicKeyForTest seeds an encrypted key without the live
// Anthropic validation the REST endpoint performs. Test-only.
func (a *API) StoreAnthropicKeyForTest(ctx context.Context, userID, key string) error {
	ciphertext, err := a.vault.Encrypt(key)
	if err != nil {
		return err
	}
	return a.store.UpsertAnthropicKey(ctx, userID, ciphertext, "test")
}

// MaybeAutoApplyIndexPoliciesForTest exposes the first-open policy
// hook synchronback-door for integration tests.
func (a *API) MaybeAutoApplyIndexPoliciesForTest(doc *models.Document) {
	a.maybeAutoApplyIndexPolicies(doc)
}
