import { useEffect, useState } from "react";
import { api, APIError } from "../api";
import { useToast } from "./Toast";
import type { CheckRule } from "../types";

const KIND_META: Record<
  CheckRule["kind"],
  { label: string; hint: string }
> = {
  required_sections: {
    label: "Required sections",
    hint: "Headings that must exist (any level, case-insensitive). One per line.",
  },
  forbidden_text: {
    label: "Forbidden text",
    hint: "Go regex that must NOT match — e.g. \\bbeamable\\b to ban the lowercase brand.",
  },
  required_text: {
    label: "Required text",
    hint: "Go regex that MUST match somewhere in the doc.",
  },
  max_heading_depth: {
    label: "Max heading depth",
    hint: "Fail when any heading is deeper than this level (1–6).",
  },
};

/** Editor for the chain's check policy — the lint rules rendered as
 * CI-style chips on every revision. Saving an empty list removes the
 * policy. */
export default function CheckPolicyModal({
  documentId,
  onClose,
  onSaved,
}: {
  documentId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [rules, setRules] = useState<CheckRule[] | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    api
      .getCheckPolicy(documentId)
      .then((p) => {
        if (!cancelled) setRules(p.rules ?? []);
      })
      .catch(() => {
        if (!cancelled) setRules([]);
      });
    return () => {
      cancelled = true;
    };
  }, [documentId]);

  function update(i: number, patch: Partial<CheckRule>) {
    setRules((rs) => rs!.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }

  function addRule() {
    setRules((rs) => [
      ...(rs ?? []),
      { kind: "required_sections", label: "", sections: [] },
    ]);
  }

  async function save() {
    if (!rules || busy) return;
    setBusy(true);
    try {
      await api.putCheckPolicy(documentId, rules);
      toast.success(
        rules.length === 0 ? "Checks removed" : "Checks saved"
      );
      onSaved();
      onClose();
    } catch (err) {
      toast.error(
        err instanceof APIError ? err.message : "Couldn't save the checks."
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="fixed inset-0 z-40 bg-black/40 flex items-center justify-center p-4">
      <div className="bg-card border border-rule rounded-lg shadow-xl max-w-2xl w-full max-h-[90vh] flex flex-col overflow-hidden">
        <div className="px-5 py-3 border-b border-rule flex items-center justify-between shrink-0">
          <div>
            <h2 className="text-lg font-semibold">Checks</h2>
            <p className="text-xs text-muted">
              Lint rules for this doc and all its revisions — results show as
              pass/fail chips next to the review states.
            </p>
          </div>
          <button
            onClick={onClose}
            disabled={busy}
            className="text-muted hover:text-ink text-sm"
          >
            ✕
          </button>
        </div>

        <div className="flex-1 min-h-0 overflow-auto p-5 space-y-3 text-sm">
          {rules === null && <div className="text-muted">Loading…</div>}
          {rules !== null && rules.length === 0 && (
            <div className="text-muted text-xs">
              No rules yet — add one below. Ideas: required sections
              (Overview, Security…), a terminology ban
              (<code className="bg-soft px-1 rounded">\bbeamable\b</code>),
              or a heading-depth cap.
            </div>
          )}
          {rules?.map((r, i) => (
            <div key={r.id ?? i} className="border border-rule rounded-lg p-3 space-y-2">
              <div className="flex items-center gap-2">
                <select
                  value={r.kind}
                  onChange={(e) =>
                    update(i, {
                      kind: e.target.value as CheckRule["kind"],
                      sections: [],
                      pattern: "",
                      maxDepth: e.target.value === "max_heading_depth" ? 3 : undefined,
                    })
                  }
                  className="text-xs border border-rule rounded px-2 py-1 bg-card text-ink"
                >
                  {(Object.keys(KIND_META) as CheckRule["kind"][]).map((k) => (
                    <option key={k} value={k}>
                      {KIND_META[k].label}
                    </option>
                  ))}
                </select>
                <input
                  type="text"
                  placeholder="Label (shown on the chip)"
                  value={r.label}
                  onChange={(e) => update(i, { label: e.target.value })}
                  className="flex-1 text-xs border border-rule rounded px-2 py-1 bg-card text-ink"
                />
                <button
                  onClick={() => setRules((rs) => rs!.filter((_, j) => j !== i))}
                  className="text-muted hover:text-danger text-xs shrink-0"
                  title="Remove rule"
                >
                  ✕
                </button>
              </div>
              <div className="text-[11px] text-faint">{KIND_META[r.kind].hint}</div>
              {r.kind === "required_sections" && (
                <textarea
                  rows={2}
                  value={(r.sections ?? []).join("\n")}
                  onChange={(e) =>
                    update(i, {
                      sections: e.target.value
                        .split("\n")
                        .map((s) => s.trim())
                        .filter(Boolean),
                    })
                  }
                  placeholder={"Overview\nSecurity"}
                  className="w-full text-xs font-mono border border-rule rounded px-2 py-1 bg-card text-ink resize-y"
                />
              )}
              {(r.kind === "forbidden_text" || r.kind === "required_text") && (
                <input
                  type="text"
                  value={r.pattern ?? ""}
                  onChange={(e) => update(i, { pattern: e.target.value })}
                  placeholder={"\\bbeamable\\b"}
                  className="w-full text-xs font-mono border border-rule rounded px-2 py-1 bg-card text-ink"
                />
              )}
              {r.kind === "max_heading_depth" && (
                <select
                  value={r.maxDepth ?? 3}
                  onChange={(e) => update(i, { maxDepth: Number(e.target.value) })}
                  className="text-xs border border-rule rounded px-2 py-1 bg-card text-ink"
                >
                  {[1, 2, 3, 4, 5, 6].map((d) => (
                    <option key={d} value={d}>
                      up to h{d}
                    </option>
                  ))}
                </select>
              )}
            </div>
          ))}
          {rules !== null && (
            <button
              onClick={addRule}
              className="text-xs px-2.5 py-1.5 rounded border border-dashed border-rule text-muted hover:text-ink hover:border-ink w-full"
            >
              + Add rule
            </button>
          )}
        </div>

        <div className="px-5 py-3 border-t border-rule flex items-center justify-end gap-2 shrink-0">
          <button
            onClick={onClose}
            disabled={busy}
            className="text-sm px-3 py-1.5 rounded text-muted hover:text-ink"
          >
            Cancel
          </button>
          <button
            onClick={save}
            disabled={busy || rules === null}
            className="text-sm px-4 py-1.5 rounded bg-accent text-accent-fg font-medium hover:opacity-90 disabled:opacity-50"
          >
            {busy ? "Saving…" : "Save checks"}
          </button>
        </div>
      </div>
    </div>
  );
}
