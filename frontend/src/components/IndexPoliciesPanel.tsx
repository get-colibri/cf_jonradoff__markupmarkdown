import { useEffect, useMemo, useState } from "react";
import { api, APIError } from "../api";
import { useToast } from "./Toast";
import type { CheckTemplate, IndexPolicyRule, MarkdownIndexItem } from "../types";

/** Index-creator-only panel: map filename patterns to named check
 * policies ("_PRD → PRD Standard"), preview exactly which files match
 * (with per-file exception unticks), then apply across the index in
 * one click. Files not yet opened in markupmarkdown link on first
 * open. Collapsed by default — one summary line until expanded. */
export default function IndexPoliciesPanel({
  indexId,
  items,
}: {
  indexId: string;
  items: MarkdownIndexItem[];
}) {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [rules, setRules] = useState<IndexPolicyRule[] | null>(null);
  const [templates, setTemplates] = useState<CheckTemplate[]>([]);
  const [busy, setBusy] = useState(false);
  const [expandedRule, setExpandedRule] = useState<string | null>(null);

  useEffect(() => {
    if (!open || rules !== null) return;
    Promise.all([
      api.getIndexPolicyRules(indexId).catch(() => ({ rules: [] })),
      api.listCheckTemplates().catch(() => [] as CheckTemplate[]),
    ]).then(([r, ts]) => {
      setRules(r.rules);
      setTemplates(ts);
    });
  }, [open, rules, indexId]);

  // Path key mirrors the backend: lowercased owner/repo/path from the
  // item URL. Matching is a case-insensitive substring on the path.
  const keyed = useMemo(
    () =>
      items
        .map((it) => {
          const m = /github\.com\/([^/]+)\/([^/]+)\/blob\/[^/]+\/(.+)$/.exec(it.url);
          if (!m) return null;
          return {
            item: it,
            path: m[3],
            key: `${m[1]}/${m[2]}/${m[3]}`.toLowerCase(),
          };
        })
        .filter(Boolean) as { item: MarkdownIndexItem; path: string; key: string }[],
    [items]
  );

  function matchesOf(rule: IndexPolicyRule) {
    const pat = rule.pattern.trim().toLowerCase();
    if (!pat) return [];
    return keyed.filter((k) => k.path.toLowerCase().includes(pat));
  }

  function update(i: number, patch: Partial<IndexPolicyRule>) {
    setRules((rs) => rs!.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }

  function toggleException(i: number, key: string) {
    setRules((rs) =>
      rs!.map((r, j) => {
        if (j !== i) return r;
        const ex = r.exceptions ?? [];
        return {
          ...r,
          exceptions: ex.includes(key)
            ? ex.filter((e) => e !== key)
            : [...ex, key],
        };
      })
    );
  }

  async function save(): Promise<boolean> {
    if (!rules || busy) return false;
    setBusy(true);
    try {
      const valid = rules.filter((r) => r.pattern.trim() && r.templateId);
      const saved = await api.putIndexPolicyRules(indexId, valid);
      setRules(saved.rules);
      toast.success("Policy rules saved");
      return true;
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Couldn't save the rules.");
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function applyNow() {
    if (busy) return;
    if (!(await save())) return;
    setBusy(true);
    try {
      const res = await api.applyIndexPolicyRules(indexId);
      const bits = [`${res.linked.length} linked`];
      if (res.skipped.length) bits.push(`${res.skipped.length} skipped (already have checks)`);
      if (res.pending) bits.push(`${res.pending} will link on first open`);
      toast.success(bits.join(" · "));
    } catch (err) {
      toast.error(err instanceof APIError ? err.message : "Couldn't apply the rules.");
    } finally {
      setBusy(false);
    }
  }

  const summary =
    rules && rules.length > 0
      ? `${rules.length} rule${rules.length === 1 ? "" : "s"}`
      : "none set";

  return (
    <div className="mb-6 border border-rule rounded-lg bg-card">
      <button
        onClick={() => setOpen((o) => !o)}
        className="w-full flex items-center justify-between px-4 py-2.5 text-sm"
      >
        <span className="font-medium text-ink">
          Check policies{" "}
          <span className="text-muted font-normal">· {open ? "" : summary}</span>
        </span>
        <span className={"text-faint text-xs transition " + (open ? "rotate-90" : "")}>▶</span>
      </button>

      {open && (
        <div className="px-4 pb-4 space-y-3 text-sm border-t border-rule pt-3">
          <p className="text-xs text-muted">
            Files matching a pattern get the policy automatically — applied
            now to opened docs, and on first open for the rest. Docs that
            already have checks are never overwritten. Untick a matched file
            to make it a permanent exception.
          </p>

          {rules === null && <div className="text-muted text-xs">Loading…</div>}

          {templates.length === 0 && rules !== null && (
            <div className="text-xs text-muted">
              You have no saved policies yet — create one from any doc's
              Checks editor ("Save as reusable policy…"), then come back.
            </div>
          )}

          {rules?.map((r, i) => {
            const matches = matchesOf(r);
            const excepted = new Set(r.exceptions ?? []);
            const active = matches.filter((m) => !excepted.has(m.key));
            const rid = r.id || String(i);
            return (
              <div key={rid} className="border border-rule rounded-lg p-3 space-y-2">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-xs text-muted">Files containing</span>
                  <input
                    type="text"
                    value={r.pattern}
                    onChange={(e) => update(i, { pattern: e.target.value })}
                    placeholder="_PRD"
                    className="text-xs font-mono border border-rule rounded px-2 py-1 bg-card text-ink w-32"
                  />
                  <span className="text-xs text-muted">get policy</span>
                  <select
                    value={r.templateId}
                    onChange={(e) => update(i, { templateId: e.target.value })}
                    className="text-xs border border-rule rounded px-2 py-1 bg-card text-ink"
                  >
                    <option value="">choose…</option>
                    {templates.map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name}
                      </option>
                    ))}
                  </select>
                  <button
                    onClick={() =>
                      setExpandedRule(expandedRule === rid ? null : rid)
                    }
                    className="text-xs text-accent hover:underline"
                  >
                    {active.length} match{active.length === 1 ? "" : "es"}
                    {excepted.size > 0 ? ` (+${excepted.size} excluded)` : ""}
                  </button>
                  <button
                    onClick={() => setRules((rs) => rs!.filter((_, j) => j !== i))}
                    className="ml-auto text-muted hover:text-danger text-xs"
                    title="Remove rule"
                  >
                    ✕
                  </button>
                </div>
                {expandedRule === rid && matches.length > 0 && (
                  <div className="pl-2 max-h-48 overflow-auto space-y-0.5">
                    {matches.map((m) => (
                      <label
                        key={m.key}
                        className="flex items-center gap-2 text-xs cursor-pointer text-muted hover:text-ink"
                      >
                        <input
                          type="checkbox"
                          checked={!excepted.has(m.key)}
                          onChange={() => toggleException(i, m.key)}
                        />
                        <span className={excepted.has(m.key) ? "line-through" : ""}>
                          {m.path}
                        </span>
                      </label>
                    ))}
                  </div>
                )}
              </div>
            );
          })}

          {rules !== null && templates.length > 0 && (
            <div className="flex items-center gap-2">
              <button
                onClick={() =>
                  setRules((rs) => [
                    ...(rs ?? []),
                    { id: "", pattern: "", templateId: "", exceptions: [] },
                  ])
                }
                className="text-xs px-2.5 py-1.5 rounded border border-dashed border-rule text-muted hover:text-ink hover:border-ink"
              >
                + Add rule
              </button>
              <span className="ml-auto" />
              <button
                onClick={save}
                disabled={busy || !rules}
                className="text-xs px-3 py-1.5 rounded border border-rule text-ink hover:border-ink disabled:opacity-50"
              >
                Save rules
              </button>
              <button
                onClick={applyNow}
                disabled={busy || !rules || rules.every((r) => !r.pattern.trim() || !r.templateId)}
                className="text-xs px-3 py-1.5 rounded bg-accent text-accent-fg font-medium hover:opacity-90 disabled:opacity-50"
              >
                {busy ? "Working…" : "Save + apply now"}
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
