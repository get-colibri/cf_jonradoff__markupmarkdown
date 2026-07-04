import { useEffect, useState } from "react";
import { api, APIError } from "../api";
import { useToast } from "./Toast";
import type { APIToken } from "../types";

/** One-click agent audit across an index: "ask <my token> to review
 * every file containing <pattern>." Mints ordinary review requests;
 * auto-review tokens fulfill them within minutes (rate-limited), and
 * results land as review states + suggestions on each doc. Available
 * to any signed-in user — you can only summon tokens YOU own. */
export default function IndexAuditPanel({ indexId }: { indexId: string }) {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [tokens, setTokens] = useState<APIToken[] | null>(null);
  const [tokenId, setTokenId] = useState("");
  const [pattern, setPattern] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!open || tokens !== null) return;
    api
      .listTokens()
      .then((ts) => {
        setTokens(ts);
        // Preselect the obvious reviewer: an auto-review token if one
        // exists, otherwise the first token.
        const auto = ts.find((t) => t.autoReview);
        setTokenId((auto ?? ts[0])?.id ?? "");
      })
      .catch(() => setTokens([]));
  }, [open, tokens]);

  async function run() {
    if (!tokenId || busy) return;
    setBusy(true);
    try {
      const res = await api.auditIndex(indexId, tokenId, pattern.trim() || undefined);
      const bits = [`${res.requested} review${res.requested === 1 ? "" : "s"} requested`];
      if (res.pending) bits.push(`${res.pending} file${res.pending === 1 ? "" : "s"} not opened yet`);
      if (res.capped) bits.push("capped at 50 — run again for the rest");
      toast.success(bits.join(" · "));
      setOpen(false);
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Audit failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="mb-6 border border-rule rounded-lg bg-card">
      <button
        onClick={() => setOpen((o) => !o)}
        className="w-full flex items-center justify-between px-4 py-2.5 text-sm"
      >
        <span className="font-medium text-ink">
          Agent audit{" "}
          <span className="text-muted font-normal">
            · summon your reviewer across this index
          </span>
        </span>
        <span className={"text-faint text-xs transition " + (open ? "rotate-90" : "")}>▶</span>
      </button>
      {open && (
        <div className="px-4 pb-4 pt-3 border-t border-rule text-sm space-y-2">
          {tokens !== null && tokens.length === 0 ? (
            <p className="text-xs text-muted">
              You need an agent token first — avatar menu → Personal access
              tokens. Check "Auto-review" so the server fulfills the reviews
              itself.
            </p>
          ) : (
            <>
              <div className="flex items-center gap-2 flex-wrap text-xs">
                <span className="text-muted">Ask</span>
                <select
                  value={tokenId}
                  onChange={(e) => setTokenId(e.target.value)}
                  className="text-xs border border-rule rounded px-2 py-1 bg-card text-ink"
                >
                  {(tokens ?? []).map((t) => (
                    <option key={t.id} value={t.id}>
                      {t.label}
                      {t.autoReview ? " ⚡" : ""}
                    </option>
                  ))}
                </select>
                <span className="text-muted">to review files containing</span>
                <input
                  type="text"
                  value={pattern}
                  onChange={(e) => setPattern(e.target.value)}
                  placeholder="_PRD (empty = every file)"
                  className="text-xs font-mono border border-rule rounded px-2 py-1 bg-card text-ink w-44"
                />
                <button
                  onClick={run}
                  disabled={busy || !tokenId}
                  className="text-xs px-3 py-1.5 rounded bg-accent text-accent-fg font-medium hover:opacity-90 disabled:opacity-50"
                >
                  {busy ? "Summoning…" : "Run audit"}
                </button>
              </div>
              <p className="text-[11px] text-faint">
                Only files already opened in markupmarkdown get reviewed
                (max 50 per run). ⚡ auto-review tokens respond within
                minutes; other tokens wait for their agent to poll. Results
                appear as review states + suggestions on each doc.
              </p>
            </>
          )}
        </div>
      )}
    </div>
  );
}
