import { useEffect, useRef, useState } from "react";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import { useToast } from "./Toast";
import type {
  APIToken,
  MdDocument,
  MentionCandidate,
  Review,
  ReviewState,
} from "../types";

interface Props {
  doc: MdDocument;
  onDocRefresh: () => Promise<void>;
  onError: (err: APIError) => void;
}

/** Review-state buttons + agent-proposed banner. Rendered near the top
 * of the doc page so the coordination surface is visible before the
 * reviewer scrolls through the content. Kept intentionally minimal —
 * three buttons, a summary badge, and the accept-revision affordance
 * when applicable. No modal, no separate reviewer list; the aggregate
 * count is enough for the MVP. */
export default function ReviewBar({ doc, onDocRefresh, onError }: Props) {
  const [busy, setBusy] = useState(false);
  const my = doc.myReview;
  const summary = doc.reviews;

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

  const buttons: { state: ReviewState; label: string; tone: string }[] = [
    { state: "commented", label: "Comment", tone: "text-ink" },
    { state: "approved", label: "Approve", tone: "text-success" },
    { state: "changes_requested", label: "Request changes", tone: "text-danger" },
  ];

  return (
    <div className="mb-4 space-y-2">
      {doc.agentProposed && <AgentProposedBanner meta={doc.revisionMeta} onAccept={acceptRevision} busy={busy} />}
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
        <RequestReviewMenu doc={doc} onError={onError} />
        {summary && <SummaryBadge summary={summary} myReview={my} />}
      </div>
    </div>
  );
}

/** "Request review" popover — one click on a person or agent creates
 * the request. Candidates load lazily on first open: humans who've
 * touched the doc (mention candidates, minus yourself) + your own
 * agent tokens. No modal, no multi-step flow. */
function RequestReviewMenu({
  doc,
  onError,
}: {
  doc: MdDocument;
  onError: (err: APIError) => void;
}) {
  const { user } = useAuth();
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [humans, setHumans] = useState<MentionCandidate[]>([]);
  const [tokens, setTokens] = useState<APIToken[]>([]);
  const [sending, setSending] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  // Close on outside click.
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

  async function request(target: { reviewerLogin?: string; tokenId?: string }, label: string) {
    if (sending) return;
    setSending(true);
    try {
      await api.createReviewRequest(doc.id, target);
      setOpen(false);
      toast.success(`Review requested from ${label}`);
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
        <div className="absolute left-0 top-full mt-1 z-30 w-60 max-h-72 overflow-auto rounded-md border border-rule bg-card shadow-lg py-1">
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
              {humans.map((c) => (
                <button
                  key={c.login}
                  disabled={sending}
                  onClick={() =>
                    request({ reviewerLogin: c.login }, c.name || c.login)
                  }
                  className="w-full text-left px-3 py-1.5 hover:bg-soft flex items-center gap-2 disabled:opacity-50"
                >
                  {c.avatarUrl ? (
                    <img src={c.avatarUrl} alt="" className="w-5 h-5 rounded-full" />
                  ) : (
                    <span className="w-5 h-5 rounded-full bg-soft" />
                  )}
                  <span className="truncate">{c.name || c.login}</span>
                </button>
              ))}
            </>
          )}
          {tokens.length > 0 && (
            <>
              <div className="px-3 pt-1.5 pb-0.5 text-[10px] uppercase tracking-wide text-faint">
                Your agents
              </div>
              {tokens.map((tk) => (
                <button
                  key={tk.id}
                  disabled={sending}
                  onClick={() => request({ tokenId: tk.id }, tk.label)}
                  className="w-full text-left px-3 py-1.5 hover:bg-soft flex items-center gap-2 disabled:opacity-50"
                >
                  <span className="w-5 h-5 rounded-md bg-accent text-accent-fg flex items-center justify-center text-[10px]">
                    ⚙
                  </span>
                  <span className="truncate">{tk.label}</span>
                </button>
              ))}
            </>
          )}
        </div>
      )}
    </div>
  );
}

function SummaryBadge({
  summary,
  myReview,
}: {
  summary: NonNullable<MdDocument["reviews"]>;
  myReview?: Review;
}) {
  const parts: string[] = [];
  if (summary.approved > 0) parts.push(`${summary.approved} approved`);
  if (summary.changesRequested > 0)
    parts.push(`${summary.changesRequested} changes requested`);
  if (summary.commented > 0) parts.push(`${summary.commented} commented`);
  if (parts.length === 0) return null;
  return (
    <span className="ml-auto text-xs text-muted">
      {parts.join(" · ")}
      {myReview ? " (incl. you)" : ""}
    </span>
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
          — written by {author}. Accept it to allow Push to GitHub, or reject by opening a fresh
          revision.
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
