import { useMemo, useState } from "react";
import { inlineWordDiff } from "../utils/diff";
import type { Comment } from "../types";

/** The suggested-change card inside a comment (P0-2, upgraded).
 * Default view is a tracked-changes inline diff — removed words in
 * red strikethrough, added words in green — so the reviewer reads
 * the change in one pass instead of eyeballing two blocks. A toggle
 * flips to the clean "result" text. Apply is one click, per Brown &
 * Parnin's actionability finding. */
export default function SuggestionBlock({
  comment,
  onApply,
}: {
  comment: Comment;
  onApply?: () => Promise<void>;
}) {
  const [view, setView] = useState<"diff" | "result">("diff");
  const [applying, setApplying] = useState(false);
  const suggestion = comment.suggestion;

  const segments = useMemo(
    () =>
      suggestion
        ? inlineWordDiff(comment.anchor.exact, suggestion.replacement)
        : [],
    [comment.anchor.exact, suggestion]
  );

  if (!suggestion || !comment.anchor.exact) return null;
  const applied = Boolean(suggestion.appliedAt);

  return (
    <div className="mt-3 rounded-md border border-rule bg-soft overflow-hidden">
      <div className="flex items-center justify-between px-3 py-1.5 border-b border-rule">
        <span className="text-[11px] uppercase tracking-wide text-muted">
          Suggested change
        </span>
        {!applied && (
          <div className="flex text-[11px] rounded overflow-hidden border border-rule">
            {(["diff", "result"] as const).map((v) => (
              <button
                key={v}
                onClick={(e) => {
                  e.stopPropagation();
                  setView(v);
                }}
                className={
                  "px-2 py-0.5 transition " +
                  (view === v
                    ? "bg-accent text-accent-fg"
                    : "bg-card text-muted hover:text-ink")
                }
              >
                {v === "diff" ? "Changes" : "Result"}
              </button>
            ))}
          </div>
        )}
      </div>

      <div className="px-3 py-2 text-xs whitespace-pre-wrap break-words text-ink leading-relaxed">
        {applied || view === "result"
          ? suggestion.replacement
          : segments.map((s, i) =>
              s.kind === "same" ? (
                <span key={i}>{s.text}</span>
              ) : s.kind === "removed" ? (
                <del
                  key={i}
                  className="bg-danger/10 text-danger line-through decoration-danger/60 rounded-[2px] px-[1px]"
                >
                  {s.text}
                </del>
              ) : (
                <ins
                  key={i}
                  className="bg-success/15 text-success no-underline font-medium rounded-[2px] px-[1px]"
                >
                  {s.text}
                </ins>
              )
            )}
      </div>

      <div className="flex items-center justify-between gap-2 px-3 py-2 border-t border-rule bg-card">
        {applied ? (
          <span className="text-xs text-muted">
            ✓ Applied
            {suggestion.appliedBy ? ` by ${suggestion.appliedBy}` : ""}
          </span>
        ) : (
          <>
            <span className="text-xs text-muted">
              Applying creates a new revision with this change.
            </span>
            <button
              onClick={async (e) => {
                e.stopPropagation();
                if (!onApply || applying) return;
                setApplying(true);
                try {
                  await onApply();
                } finally {
                  setApplying(false);
                }
              }}
              disabled={applying || !onApply}
              className="shrink-0 text-xs px-2.5 py-1 rounded bg-accent text-accent-fg hover:opacity-90 disabled:opacity-50"
            >
              {applying ? "Applying…" : "Apply"}
            </button>
          </>
        )}
      </div>
    </div>
  );
}
