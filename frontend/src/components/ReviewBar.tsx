import { useEffect, useRef, useState } from "react";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import { useToast } from "./Toast";
import type {
  APIToken,
  MdDocument,
  MentionCandidate,
  Review,
  ReviewRequest,
  ReviewState,
  ReviewSubscription,
} from "../types";

interface Props {
  doc: MdDocument;
  onDocRefresh: () => Promise<void>;
  onError: (err: APIError) => void;
}

const STATE_LABEL: Record<ReviewState, string> = {
  approved: "approved",
  changes_requested: "requested changes",
  commented: "commented",
};

/** Review coordination bar: state buttons + who-reviewed-what chips +
 * pending "awaiting X" chips + standing reviewers. All names, no bare
 * counts — "1 changes requested" told you nothing; "You requested
 * changes" does. Everything loads/refreshes off the doc object the
 * page already maintains via SSE. */
export default function ReviewBar({ doc, onDocRefresh, onError }: Props) {
  const [busy, setBusy] = useState(false);
  const my = doc.myReview;

  // Coordination state fetched per doc: full review list (for named
  // chips), pending requests on this doc, standing reviewers.
  const [reviews, setReviews] = useState<Review[]>([]);
  const [pending, setPending] = useState<ReviewRequest[]>([]);
  const [subs, setSubs] = useState<ReviewSubscription[]>([]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [rv, rq, sb] = await Promise.all([
          api.listReviews(doc.id),
          api.listDocReviewRequests(doc.id).catch(() => [] as ReviewRequest[]),
          api.listReviewSubscriptions(doc.id).catch(
            () => [] as ReviewSubscription[]
          ),
        ]);
        if (cancelled) return;
        setReviews(rv);
        setPending(rq);
        setSubs(sb);
      } catch {
        // Non-critical decoration — the state buttons still work.
      }
    })();
    return () => {
      cancelled = true;
    };
    // `doc` object identity changes on every SSE-driven refetch, so
    // this stays live without its own event plumbing.
  }, [doc]);

  async function setState(state: ReviewState) {
    if (busy) return;
    setBusy(true);
    try {
      // Clicking the button you already have set toggles it off.
      if (my?.state === state) {
        await api.deleteReview(doc.id);
      } else {
        await api.setReview(doc.id, state);
      }
      await onDocRefresh();
    } catch (err) {
      if (err instanceof APIError) onError(err);
    } finally {
      setBusy(false);
    }
  }

  async function acceptRevision() {
    if (busy) return;
    setBusy(true);
    try {
      await api.acceptAgentRevision(doc.id);
      await onDocRefresh();
    } catch (err) {
      if (err instanceof APIError) onError(err);
    } finally {
      setBusy(false);
    }
  }

  async function removeSubscription(id: string) {
    try {
      await api.deleteReviewSubscription(id);
      setSubs((s) => s.filter((x) => x.id !== id));
    } catch (err) {
      if (err instanceof APIError) onError(err);
    }
  }

  async function cancelRequest(id: string) {
    try {
      await api.dismissReviewRequest(id);
      setPending((p) => p.filter((x) => x.id !== id));
    } catch (err) {
      if (err instanceof APIError) onError(err);
    }
  }

  const buttons: { state: ReviewState; label: string; tone: string }[] = [
    { state: "commented", label: "Comment", tone: "text-ink" },
    { state: "approved", label: "Approve", tone: "text-success" },
    { state: "changes_requested", label: "Request changes", tone: "text-danger" },
  ];

  const hasChips = reviews.length > 0 || pending.length > 0 || subs.length > 0;

  return (
    <div className="mb-4 space-y-2">
      {doc.agentProposed && (
        <AgentProposedBanner
          meta={doc.revisionMeta}
          onAccept={acceptRevision}
          busy={busy}
        />
      )}
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span className="text-muted">Review:</span>
        {buttons.map((b) => {
          const active = my?.state === b.state;
          return (
            <button
              key={b.state}
              onClick={() => setState(b.state)}
              disabled={busy}
              className={
                "px-2 py-1 rounded border transition " +
                (active
                  ? `border-accent bg-accent-soft ${b.tone} font-medium`
                  : "border-rule text-muted hover:text-ink hover:border-ink") +
                " disabled:opacity-50"
              }
              title={active ? "Click again to clear your review" : undefined}
            >
              {b.label}
            </button>
          );
        })}
        <RequestReviewMenu
          doc={doc}
          pending={pending}
          subs={subs}
          onError={onError}
          onRequested={(rq) =>
            setPending((p) => [rq, ...p.filter((x) => x.id !== rq.id)])
          }
          onSubscribed={(sb) =>
            setSubs((s) => [...s.filter((x) => x.id !== sb.id), sb])
          }
        />
      </div>

      {/* Who-said-what chips. Named, never counted — this line is the
          answer to "what does the review status actually mean?" */}
      {hasChips && (
        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          {reviews.map((rv) => (
            <span
              key={(rv.author || "") + rv.updatedAt}
              title={rv.note || undefined}
              className={
                "inline-flex items-center gap-1 px-2 py-0.5 rounded-full border " +
                (rv.state === "approved"
                  ? "border-success/40 text-success"
                  : rv.state === "changes_requested"
                    ? "border-danger/40 text-danger"
                    : "border-rule text-muted")
              }
            >
              {rv.state === "approved" ? "✓" : rv.state === "changes_requested" ? "±" : "💬"}{" "}
              {rv.mine ? "You" : rv.author || "someone"}{" "}
              {STATE_LABEL[rv.state]}
            </span>
          ))}
          {pending.map((rq) => (
            <span
              key={rq.id}
              className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full border border-dashed border-rule text-muted"
              title={`Requested by ${rq.requesterName}`}
            >
              awaiting {rq.reviewerName}
              <button
                onClick={() => cancelRequest(rq.id)}
                className="hover:text-ink ml-0.5"
                title="Cancel this review request"
              >
                ✕
              </button>
            </span>
          ))}
          {subs.map((sb) => (
            <span
              key={sb.id}
              className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full border border-rule text-faint"
              title="Standing reviewer — asked again on every new revision"
            >
              ⟳ {sb.reviewerName}
              <button
                onClick={() => removeSubscription(sb.id)}
                className="hover:text-ink ml-0.5"
                title="Remove standing reviewer"
              >
                ✕
              </button>
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

/** "Request review" popover. One click on a person or agent creates
 * the request; entries with a request already pending show
 * "requested" and won't double-fire. The footer toggle upgrades the
 * click to a STANDING subscription (review every future revision). */
function RequestReviewMenu({
  doc,
  pending,
  subs,
  onError,
  onRequested,
  onSubscribed,
}: {
  doc: MdDocument;
  pending: ReviewRequest[];
  subs: ReviewSubscription[];
  onError: (err: APIError) => void;
  onRequested: (rq: ReviewRequest) => void;
  onSubscribed: (sb: ReviewSubscription) => void;
}) {
  const { user } = useAuth();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [humans, setHumans] = useState<MentionCandidate[]>([]);
  const [tokens, setTokens] = useState<APIToken[]>([]);
  const [sending, setSending] = useState(false);
  const [standing, setStanding] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  async function toggle() {
    if (open) {
      setOpen(false);
      return;
    }
    setOpen(true);
    if (humans.length || tokens.length || loading) return;
    setLoading(true);
    try {
      const [cands, toks] = await Promise.all([
        api.listMentionCandidates(doc.id),
        api.listTokens().catch(() => [] as APIToken[]),
      ]);
      setHumans(cands.filter((c) => c.login !== user?.login));
      setTokens(toks);
    } catch (err) {
      if (err instanceof APIError) onError(err);
    } finally {
      setLoading(false);
    }
  }

  const pendingLogins = new Set(
    pending.filter((p) => !p.reviewerTokenId).map((p) => p.reviewerName)
  );
  const pendingTokenIds = new Set(
    pending.map((p) => p.reviewerTokenId).filter(Boolean)
  );
  const subTokenIds = new Set(subs.map((s) => s.reviewerTokenId).filter(Boolean));

  async function request(
    target: { reviewerLogin?: string; tokenId?: string },
    label: string
  ) {
    if (sending) return;
    setSending(true);
    try {
      const rq = await api.createReviewRequest(doc.id, target);
      onRequested(rq);
      if (standing) {
        const sb = await api.createReviewSubscription(doc.id, target);
        onSubscribed(sb);
      }
      setOpen(false);
      toast.success(
        standing
          ? `${label} will review this and every future revision`
          : `Review requested from ${label}`
      );
    } catch (err) {
      if (err instanceof APIError) onError(err);
    } finally {
      setSending(false);
    }
  }

  const empty = !loading && humans.length === 0 && tokens.length === 0;

  return (
    <div className="relative" ref={rootRef}>
      <button
        onClick={toggle}
        className="px-2 py-1 rounded border border-rule text-muted hover:text-ink hover:border-ink transition"
      >
        Request review
      </button>
      {open && (
        <div className="absolute left-0 top-full mt-1 z-30 w-64 max-h-80 overflow-auto rounded-md border border-rule bg-card shadow-lg py-1">
          {loading && <div className="px-3 py-2 text-muted">Loading…</div>}
          {empty && (
            <div className="px-3 py-2 text-muted">
              Nobody to ask yet — collaborators appear here once they've
              opened this doc; agents once you've created a token.
            </div>
          )}
          {humans.length > 0 && (
            <>
              <div className="px-3 pt-1.5 pb-0.5 text-[10px] uppercase tracking-wide text-faint">
                People
              </div>
              {humans.map((c) => {
                const requested =
                  pendingLogins.has(c.name) || pendingLogins.has(c.login);
                return (
                  <button
                    key={c.login}
                    disabled={sending || requested}
                    onClick={() =>
                      request({ reviewerLogin: c.login }, c.name || c.login)
                    }
                    className="w-full text-left px-3 py-1.5 hover:bg-soft flex items-center gap-2 disabled:opacity-50"
                  >
                    {c.avatarUrl ? (
                      <img
                        src={c.avatarUrl}
                        alt=""
                        className="w-5 h-5 rounded-full"
                      />
                    ) : (
                      <span className="w-5 h-5 rounded-full bg-soft" />
                    )}
                    <span className="truncate flex-1">{c.name || c.login}</span>
                    {requested && (
                      <span className="text-[10px] text-faint">requested</span>
                    )}
                  </button>
                );
              })}
            </>
          )}
          {tokens.length > 0 && (
            <>
              <div className="px-3 pt-1.5 pb-0.5 text-[10px] uppercase tracking-wide text-faint">
                Your agents
              </div>
              {tokens.map((tk) => {
                const requested = pendingTokenIds.has(tk.id);
                const subscribed = subTokenIds.has(tk.id);
                return (
                  <button
                    key={tk.id}
                    disabled={sending || requested || subscribed}
                    onClick={() => request({ tokenId: tk.id }, tk.label)}
                    className="w-full text-left px-3 py-1.5 hover:bg-soft flex items-center gap-2 disabled:opacity-50"
                  >
                    <span className="w-5 h-5 rounded-md bg-accent text-accent-fg flex items-center justify-center text-[10px]">
                      ⚙
                    </span>
                    <span className="truncate flex-1">{tk.label}</span>
                    {subscribed ? (
                      <span className="text-[10px] text-faint">standing</span>
                    ) : requested ? (
                      <span className="text-[10px] text-faint">requested</span>
                    ) : null}
                  </button>
                );
              })}
            </>
          )}
          {!empty && !loading && (
            <label className="flex items-center gap-2 px-3 pt-2 pb-1.5 mt-1 border-t border-rule cursor-pointer text-muted">
              <input
                type="checkbox"
                checked={standing}
                onChange={(e) => setStanding(e.target.checked)}
              />
              <span>Also review every future revision</span>
            </label>
          )}
        </div>
      )}
    </div>
  );
}

function AgentProposedBanner({
  meta,
  onAccept,
  busy,
}: {
  meta: MdDocument["revisionMeta"];
  onAccept: () => Promise<void>;
  busy: boolean;
}) {
  const author = meta?.generatedBy || "an agent";
  return (
    <div className="rounded-md border border-accent bg-accent-soft px-3 py-2 flex items-center justify-between gap-3">
      <div className="text-sm">
        <span className="font-medium">Agent-proposed revision</span>
        <span className="text-muted">
          {" "}
          — written by {author}. Accept it to allow Push to GitHub, or reject
          by opening a fresh revision.
        </span>
      </div>
      <button
        onClick={onAccept}
        disabled={busy}
        className="shrink-0 text-xs px-3 py-1.5 rounded bg-accent text-accent-fg hover:opacity-90 disabled:opacity-50"
      >
        Accept revision
      </button>
    </div>
  );
}
