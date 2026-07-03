import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import ErrorBlock from "../components/ErrorBlock";
import type { AdminOverview, AdminRecentDoc } from "../types";
import TimeAgo from "../components/TimeAgo";

/** Superuser console. One page, one load: headline counts, a 30-day
 * docs-per-day sparkline, recent agent activity, and the global feed
 * of recent PUBLIC docs (private docs never leave the backend). */
export default function AdminPage() {
  const { user, isAdmin, loading: authLoading } = useAuth();
  const [ov, setOv] = useState<AdminOverview | null>(null);
  const [recent, setRecent] = useState<AdminRecentDoc[] | null>(null);
  const [error, setError] = useState<APIError | null>(null);

  useEffect(() => {
    if (authLoading || !user || !isAdmin) return;
    let cancelled = false;
    (async () => {
      try {
        const [o, r] = await Promise.all([
          api.adminOverview(),
          api.adminRecentPublicDocs(50),
        ]);
        if (cancelled) return;
        setOv(o);
        setRecent(r);
      } catch (err) {
        if (!cancelled && err instanceof APIError) setError(err);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [authLoading, user, isAdmin]);

  if (authLoading) return null;
  if (!user || !isAdmin) {
    return (
      <div className="max-w-3xl mx-auto px-4 py-16 text-center text-muted">
        Not found.
      </div>
    );
  }

  const maxDay = Math.max(1, ...(ov?.docsPerDay.map((d) => d.count) ?? [1]));

  return (
    <div className="max-w-5xl mx-auto px-4 py-8">
      <h1 className="text-xl font-semibold mb-6">Admin console</h1>
      {error && <ErrorBlock error={error} onDismiss={() => setError(null)} />}
      {!ov && !error && <div className="text-muted text-sm">Loading…</div>}

      {ov && (
        <>
          {/* Headline tiles */}
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-8">
            <Stat label="Users" value={ov.users} sub={`+${ov.usersThisWeek} this week · +${ov.usersThisMonth} this month`} />
            <Stat label="Live docs" value={ov.docsLive} sub={`${ov.docsRevisions} revisions · ${ov.docsTrashed} in trash`} />
            <Stat label="Public / private" value={`${ov.docsPublic} / ${ov.docsPrivate}`} sub={pct(ov.docsPublic, ov.docsPublic + ov.docsPrivate) + " public"} />
            <Stat label="Comments" value={ov.comments} sub={`${ov.suggestionsMade} suggestions · ${ov.suggestionsUsed} applied`} />
            <Stat label="Indexes" value={ov.indexes} />
            <Stat label="Active tokens" value={ov.agentTokens} />
            <Stat
              label="Reviews"
              value={ov.reviewsApproved + ov.reviewsChanges + ov.reviewsCommented}
              sub={`${ov.reviewsApproved} ✓ · ${ov.reviewsChanges} ± · ${ov.reviewsCommented} 💬`}
            />
          </div>

          {/* Docs per day — last 30 days */}
          <h2 className="text-sm font-semibold mb-2">Docs created — last 30 days</h2>
          <div className="flex items-end gap-[2px] h-20 mb-8 border-b border-rule pb-px">
            {ov.docsPerDay.length === 0 && (
              <div className="text-xs text-muted">No docs created in the window.</div>
            )}
            {ov.docsPerDay.map((d) => (
              <div
                key={d.day}
                title={`${d.day}: ${d.count}`}
                className="bg-accent/70 hover:bg-accent rounded-t w-full min-w-[4px]"
                style={{ height: `${Math.max(6, (d.count / maxDay) * 100)}%` }}
              />
            ))}
          </div>

          {/* Recent agent activity */}
          {ov.recentAgentActions.length > 0 && (
            <>
              <h2 className="text-sm font-semibold mb-2">Recent agent activity</h2>
              <div className="bg-card border border-rule rounded-lg divide-y divide-rule overflow-hidden mb-8 text-xs">
                {ov.recentAgentActions.map((a, i) => (
                  <div key={i} className="px-3 py-1.5 flex items-center justify-between gap-2">
                    <code className="text-ink">{a.action}</code>
                    <span className="text-muted shrink-0">
                      {a.documentId && (
                        <Link to={`/d/${a.documentId}`} className="text-accent hover:underline mr-2">
                          doc
                        </Link>
                      )}
                      <TimeAgo iso={a.at} />
                    </span>
                  </div>
                ))}
              </div>
            </>
          )}

          {/* Recent public docs */}
          <h2 className="text-sm font-semibold mb-2">Recent public docs</h2>
          {recent && recent.length === 0 && (
            <div className="text-xs text-muted">No public docs yet.</div>
          )}
          {recent && recent.length > 0 && (
            <div className="bg-card border border-rule rounded-lg overflow-x-auto">
              <table className="w-full text-xs">
                <thead>
                  <tr className="text-left text-muted border-b border-rule">
                    <th className="px-3 py-2 font-medium">Title</th>
                    <th className="px-3 py-2 font-medium">Creator</th>
                    <th className="px-3 py-2 font-medium">Comments</th>
                    <th className="px-3 py-2 font-medium">Created</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-rule">
                  {recent.map((d) => (
                    <tr key={d.id} className="hover:bg-soft">
                      <td className="px-3 py-2">
                        <Link to={`/d/${d.id}`} className="text-accent hover:underline">
                          {d.title}
                        </Link>
                        {d.isRevision && (
                          <span className="ml-1.5 text-[10px] text-faint uppercase">rev</span>
                        )}
                      </td>
                      <td className="px-3 py-2 text-muted">{d.createdBy || "—"}</td>
                      <td className="px-3 py-2 text-muted">{d.commentCount || ""}</td>
                      <td className="px-3 py-2 text-muted whitespace-nowrap">
                        <TimeAgo iso={d.createdAt} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function Stat({
  label,
  value,
  sub,
}: {
  label: string;
  value: number | string;
  sub?: string;
}) {
  return (
    <div className="bg-card border border-rule rounded-lg px-3 py-2.5">
      <div className="text-[10px] uppercase tracking-wide text-faint">{label}</div>
      <div className="text-lg font-semibold text-ink">{value}</div>
      {sub && <div className="text-[10px] text-muted mt-0.5">{sub}</div>}
    </div>
  );
}

function pct(part: number, total: number): string {
  if (total === 0) return "0%";
  return Math.round((part / total) * 100) + "%";
}
