package api

// Superuser console (/admin). Gating: an env-var allowlist of GitHub
// logins (MARKUPMARKDOWN_ADMIN_LOGINS, comma-separated) — config, not
// data, so it survives DB restores and has no bootstrap problem.
// Cookie sessions ONLY: a leaked agent token must never read usage
// data, same posture as the credential endpoints (rule #14).
//
// The recent-docs feed is public-docs-only BY QUERY — private docs
// never leave the endpoint, so the console can't accidentally browse
// someone's private material. (The DB is always available for real
// investigations; the console keeps day-to-day honest by default.)

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"markupmarkdown/internal/models"
)

// isAdminUser: signed-in + login on the MARKUPMARKDOWN_ADMIN_LOGINS
// allowlist. Parsed per call (the env string is tiny) so tests can
// set the variable without fighting package-init ordering.
func isAdminUser(u *models.User) bool {
	if u == nil || u.Login == "" {
		return false
	}
	login := strings.ToLower(u.Login)
	for _, l := range strings.Split(os.Getenv("MARKUPMARKDOWN_ADMIN_LOGINS"), ",") {
		if strings.ToLower(strings.TrimSpace(l)) == login {
			return true
		}
	}
	return false
}

// requireAdmin gates every /api/admin/* handler. Returns nil after
// writing the error when the caller doesn't qualify. 404 (not 403)
// for non-admins so the route doesn't confirm its own existence.
func (a *API) requireAdmin(w http.ResponseWriter, r *http.Request) *models.User {
	user := a.currentUser(r)
	if user == nil {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return nil
	}
	if _, hasToken := tokenInfoFromRequest(r); hasToken {
		writeError(w, http.StatusForbidden, "the admin console is cookie-session only")
		return nil
	}
	if !isAdminUser(user) {
		writeError(w, http.StatusNotFound, "not found")
		return nil
	}
	return user
}

type adminOverview struct {
	Users            int64 `json:"users"`
	UsersThisWeek    int64 `json:"usersThisWeek"`
	UsersThisMonth   int64 `json:"usersThisMonth"`
	AgentTokens      int64 `json:"agentTokens"`
	DocsLive         int64 `json:"docsLive"`
	DocsTrashed      int64 `json:"docsTrashed"`
	DocsPublic       int64 `json:"docsPublic"`
	DocsPrivate      int64 `json:"docsPrivate"`
	DocsRevisions    int64 `json:"docsRevisions"` // live docs that are child revisions
	Indexes          int64 `json:"indexes"`
	Comments         int64 `json:"comments"`
	ReviewsApproved  int64 `json:"reviewsApproved"`
	ReviewsChanges   int64 `json:"reviewsChanges"`
	ReviewsCommented int64 `json:"reviewsCommented"`
	SuggestionsMade  int64 `json:"suggestionsMade"`
	SuggestionsUsed  int64 `json:"suggestionsUsed"`

	// DocsPerDay covers the last 30 days, oldest first.
	DocsPerDay []adminDayCount `json:"docsPerDay"`
	// RecentAgentActions is a sample of the last token events.
	RecentAgentActions []adminAgentAction `json:"recentAgentActions"`
}

type adminDayCount struct {
	Day   string `json:"day"` // YYYY-MM-DD (UTC)
	Count int64  `json:"count"`
}

type adminAgentAction struct {
	Action     string    `json:"action"`
	DocumentID string    `json:"documentId,omitempty"`
	At         time.Time `json:"at"`
}

// adminOverviewHandler is GET /api/admin/overview.
func (a *API) adminOverviewHandler(w http.ResponseWriter, r *http.Request) {
	if a.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()
	now := time.Now().UTC()
	live := bson.M{"deleted_at": bson.M{"$exists": false}}

	var ov adminOverview
	ov.Users, _ = a.store.Users().CountDocuments(ctx, bson.M{})
	ov.UsersThisWeek, _ = a.store.Users().CountDocuments(ctx,
		bson.M{"created_at": bson.M{"$gte": now.AddDate(0, 0, -7)}})
	ov.UsersThisMonth, _ = a.store.Users().CountDocuments(ctx,
		bson.M{"created_at": bson.M{"$gte": now.AddDate(0, -1, 0)}})
	ov.AgentTokens, _ = a.store.APITokens().CountDocuments(ctx,
		bson.M{"revoked_at": bson.M{"$exists": false}})
	ov.DocsLive, _ = a.store.Documents().CountDocuments(ctx, live)
	ov.DocsTrashed, _ = a.store.Documents().CountDocuments(ctx,
		bson.M{"deleted_at": bson.M{"$exists": true}})
	ov.DocsPublic, _ = a.store.Documents().CountDocuments(ctx,
		bson.M{"deleted_at": bson.M{"$exists": false}, "private": false})
	ov.DocsPrivate, _ = a.store.Documents().CountDocuments(ctx,
		bson.M{"deleted_at": bson.M{"$exists": false}, "private": true})
	ov.DocsRevisions, _ = a.store.Documents().CountDocuments(ctx,
		bson.M{"deleted_at": bson.M{"$exists": false}, "parent_id": bson.M{"$exists": true, "$ne": ""}})
	ov.Indexes, _ = a.store.Indexes().CountDocuments(ctx, bson.M{})
	ov.Comments, _ = a.store.Comments().CountDocuments(ctx, bson.M{})
	ov.ReviewsApproved, _ = a.store.Reviews().CountDocuments(ctx,
		bson.M{"state": string(models.ReviewStateApproved)})
	ov.ReviewsChanges, _ = a.store.Reviews().CountDocuments(ctx,
		bson.M{"state": string(models.ReviewStateChangesRequested)})
	ov.ReviewsCommented, _ = a.store.Reviews().CountDocuments(ctx,
		bson.M{"state": string(models.ReviewStateCommented)})
	ov.SuggestionsMade, _ = a.store.Comments().CountDocuments(ctx,
		bson.M{"suggestion": bson.M{"$exists": true}})
	ov.SuggestionsUsed, _ = a.store.Comments().CountDocuments(ctx,
		bson.M{"suggestion.applied_at": bson.M{"$exists": true}})

	// Docs created per day, last 30 days.
	since := now.AddDate(0, 0, -30)
	cur, err := a.store.Documents().Aggregate(ctx, []bson.M{
		{"$match": bson.M{"created_at": bson.M{"$gte": since}}},
		{"$group": bson.M{
			"_id":   bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$created_at"}},
			"count": bson.M{"$sum": 1},
		}},
		{"$sort": bson.M{"_id": 1}},
	})
	if err == nil {
		var rows []struct {
			ID    string `bson:"_id"`
			Count int64  `bson:"count"`
		}
		if err := cur.All(ctx, &rows); err == nil {
			for _, row := range rows {
				ov.DocsPerDay = append(ov.DocsPerDay, adminDayCount{Day: row.ID, Count: row.Count})
			}
		}
	}
	if ov.DocsPerDay == nil {
		ov.DocsPerDay = []adminDayCount{}
	}

	// Recent agent activity from the sampled token-events collection.
	evCur, err := a.store.TokenEvents().Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "at", Value: -1}}).SetLimit(25))
	if err == nil {
		var evs []struct {
			Action     string    `bson:"action"`
			DocumentID string    `bson:"document_id"`
			At         time.Time `bson:"at"`
		}
		if err := evCur.All(ctx, &evs); err == nil {
			for _, e := range evs {
				ov.RecentAgentActions = append(ov.RecentAgentActions,
					adminAgentAction{Action: e.Action, DocumentID: e.DocumentID, At: e.At})
			}
		}
	}
	if ov.RecentAgentActions == nil {
		ov.RecentAgentActions = []adminAgentAction{}
	}

	writeJSON(w, http.StatusOK, ov)
}

type adminRecentDoc struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	SourceURL    string    `json:"sourceUrl,omitempty"`
	CreatedBy    string    `json:"createdBy,omitempty"`
	CommentCount int64     `json:"commentCount"`
	IsRevision   bool      `json:"isRevision"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// adminRecentPublicDocs is GET /api/admin/recent-public-docs?limit=N.
// PUBLIC docs only, enforced in the query — private material never
// leaves this endpoint.
func (a *API) adminRecentPublicDocs(w http.ResponseWriter, r *http.Request) {
	if a.requireAdmin(w, r) == nil {
		return
	}
	limit := int64(50)
	if v, err := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	ctx := r.Context()
	cur, err := a.store.Documents().Find(ctx,
		bson.M{"deleted_at": bson.M{"$exists": false}, "private": false},
		options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}).SetLimit(limit))
	if err != nil {
		internalError(w, "admin.recent_public", err)
		return
	}
	var docs []models.Document
	if err := cur.All(ctx, &docs); err != nil {
		internalError(w, "admin.recent_public.decode", err)
		return
	}

	out := make([]adminRecentDoc, 0, len(docs))
	ids := make([]string, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	// One aggregate for all comment counts instead of N queries.
	counts := map[string]int64{}
	if len(ids) > 0 {
		cc, err := a.store.Comments().Aggregate(ctx, []bson.M{
			{"$match": bson.M{"document_id": bson.M{"$in": ids}}},
			{"$group": bson.M{"_id": "$document_id", "count": bson.M{"$sum": 1}}},
		})
		if err == nil {
			var rows []struct {
				ID    string `bson:"_id"`
				Count int64  `bson:"count"`
			}
			if err := cc.All(ctx, &rows); err == nil {
				for _, row := range rows {
					counts[row.ID] = row.Count
				}
			}
		}
	}
	// Resolve creator logins in one pass.
	creatorIDs := map[string]struct{}{}
	for _, d := range docs {
		if d.CreatedByID != "" {
			creatorIDs[d.CreatedByID] = struct{}{}
		}
	}
	logins := map[string]string{}
	for uid := range creatorIDs {
		if u, _ := a.store.GetUser(ctx, uid); u != nil {
			logins[uid] = u.Login
		}
	}
	for _, d := range docs {
		out = append(out, adminRecentDoc{
			ID:           d.ID,
			Title:        d.Title,
			SourceURL:    d.SourceURL,
			CreatedBy:    logins[d.CreatedByID],
			CommentCount: counts[d.ID],
			IsRevision:   d.ParentID != "",
			CreatedAt:    d.CreatedAt,
			UpdatedAt:    d.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type adminUserRow struct {
	ID           string     `json:"id"`
	Login        string     `json:"login"`
	Name         string     `json:"name,omitempty"`
	AvatarURL    string     `json:"avatarUrl,omitempty"`
	JoinedAt     time.Time  `json:"joinedAt"`
	LastActiveAt *time.Time `json:"lastActiveAt,omitempty"`
	DocsPublic   int64      `json:"docsPublic"`
	DocsPrivate  int64      `json:"docsPrivate"`
	Comments     int64      `json:"comments"`
	AgentTokens  int64      `json:"agentTokens"`
}

// adminRecentUsers is GET /api/admin/recent-users — users ordered by
// most recent activity (their newest document view), with public
// per-user aggregates. Deliberately excludes anything private: no
// email, no key status, no token names, and private docs appear only
// as a COUNT (titles stay hidden even from the console).
func (a *API) adminRecentUsers(w http.ResponseWriter, r *http.Request) {
	if a.requireAdmin(w, r) == nil {
		return
	}
	ctx := r.Context()

	// Last activity per user from the view markers.
	lastActive := map[string]time.Time{}
	if cur, err := a.store.DocumentViews().Aggregate(ctx, []bson.M{
		{"$group": bson.M{"_id": "$user_id", "last": bson.M{"$max": "$last_viewed_at"}}},
	}); err == nil {
		var rows []struct {
			ID   string    `bson:"_id"`
			Last time.Time `bson:"last"`
		}
		if err := cur.All(ctx, &rows); err == nil {
			for _, row := range rows {
				lastActive[row.ID] = row.Last
			}
		}
	}
	// Docs created per user, split public/private.
	type docAgg struct {
		Pub, Priv int64
	}
	docCounts := map[string]*docAgg{}
	if cur, err := a.store.Documents().Aggregate(ctx, []bson.M{
		{"$match": bson.M{"deleted_at": bson.M{"$exists": false}, "created_by_id": bson.M{"$ne": ""}}},
		{"$group": bson.M{
			"_id":   bson.M{"u": "$created_by_id", "p": "$private"},
			"count": bson.M{"$sum": 1},
		}},
	}); err == nil {
		var rows []struct {
			ID struct {
				U string `bson:"u"`
				P bool   `bson:"p"`
			} `bson:"_id"`
			Count int64 `bson:"count"`
		}
		if err := cur.All(ctx, &rows); err == nil {
			for _, row := range rows {
				agg := docCounts[row.ID.U]
				if agg == nil {
					agg = &docAgg{}
					docCounts[row.ID.U] = agg
				}
				if row.ID.P {
					agg.Priv += row.Count
				} else {
					agg.Pub += row.Count
				}
			}
		}
	}
	// Comments authored per user.
	commentCounts := map[string]int64{}
	if cur, err := a.store.Comments().Aggregate(ctx, []bson.M{
		{"$match": bson.M{"author_id": bson.M{"$ne": ""}}},
		{"$group": bson.M{"_id": "$author_id", "count": bson.M{"$sum": 1}}},
	}); err == nil {
		var rows []struct {
			ID    string `bson:"_id"`
			Count int64  `bson:"count"`
		}
		if err := cur.All(ctx, &rows); err == nil {
			for _, row := range rows {
				commentCounts[row.ID] = row.Count
			}
		}
	}
	// Active tokens per user.
	tokenCounts := map[string]int64{}
	if cur, err := a.store.APITokens().Aggregate(ctx, []bson.M{
		{"$match": bson.M{"revoked_at": bson.M{"$exists": false}}},
		{"$group": bson.M{"_id": "$user_id", "count": bson.M{"$sum": 1}}},
	}); err == nil {
		var rows []struct {
			ID    string `bson:"_id"`
			Count int64  `bson:"count"`
		}
		if err := cur.All(ctx, &rows); err == nil {
			for _, row := range rows {
				tokenCounts[row.ID] = row.Count
			}
		}
	}

	cur, err := a.store.Users().Find(ctx, bson.M{})
	if err != nil {
		internalError(w, "admin.users", err)
		return
	}
	var users []models.User
	if err := cur.All(ctx, &users); err != nil {
		internalError(w, "admin.users.decode", err)
		return
	}
	out := make([]adminUserRow, 0, len(users))
	for _, u := range users {
		row := adminUserRow{
			ID:          u.ID,
			Login:       u.Login,
			Name:        u.Name,
			AvatarURL:   u.AvatarURL,
			JoinedAt:    u.CreatedAt,
			Comments:    commentCounts[u.ID],
			AgentTokens: tokenCounts[u.ID],
		}
		if agg := docCounts[u.ID]; agg != nil {
			row.DocsPublic, row.DocsPrivate = agg.Pub, agg.Priv
		}
		if t, ok := lastActive[u.ID]; ok {
			tt := t
			row.LastActiveAt = &tt
		}
		out = append(out, row)
	}
	// Most recently active first; never-active users sink to the
	// bottom ordered by join date.
	sortCandidates(out, func(i, j int) bool {
		a, b := out[i].LastActiveAt, out[j].LastActiveAt
		if a != nil && b != nil {
			return a.After(*b)
		}
		if a != nil {
			return true
		}
		if b != nil {
			return false
		}
		return out[i].JoinedAt.After(out[j].JoinedAt)
	})
	if len(out) > 100 {
		out = out[:100]
	}
	writeJSON(w, http.StatusOK, out)
}

// adminUserDocs is GET /api/admin/users/{id}/docs — the drill-down.
// PUBLIC docs only (query-enforced, same posture as the recent feed);
// private material surfaces only as a count.
func (a *API) adminUserDocs(w http.ResponseWriter, r *http.Request) {
	if a.requireAdmin(w, r) == nil {
		return
	}
	userID := mux.Vars(r)["id"]
	ctx := r.Context()
	cur, err := a.store.Documents().Find(ctx,
		bson.M{"created_by_id": userID, "deleted_at": bson.M{"$exists": false}, "private": false},
		options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}).SetLimit(100))
	if err != nil {
		internalError(w, "admin.user_docs", err)
		return
	}
	var docs []models.Document
	if err := cur.All(ctx, &docs); err != nil {
		internalError(w, "admin.user_docs.decode", err)
		return
	}
	privateCount, _ := a.store.Documents().CountDocuments(ctx,
		bson.M{"created_by_id": userID, "deleted_at": bson.M{"$exists": false}, "private": true})

	out := make([]adminRecentDoc, 0, len(docs))
	for _, d := range docs {
		out = append(out, adminRecentDoc{
			ID:         d.ID,
			Title:      d.Title,
			SourceURL:  d.SourceURL,
			IsRevision: d.ParentID != "",
			CreatedAt:  d.CreatedAt,
			UpdatedAt:  d.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"docs":         out,
		"privateCount": privateCount,
	})
}
