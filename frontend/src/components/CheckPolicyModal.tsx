import { useEffect, useMemo, useRef, useState } from "react";
import { api, APIError } from "../api";
import { useToast } from "./Toast";
import type { CheckResult, CheckRule, CheckTemplate } from "../types";

/** Preset-driven editor for the chain's check policy. Design goals,
 * in order: (1) zero required regex — the common cases are wizards
 * over plain words; (2) live pass/fail preview against THIS doc while
 * composing, so you see a rule work before saving; (3) presets
 * harvested from the doc itself (section checkboxes come from its
 * actual headings). Raw regex survives as one "Advanced" preset. */
export default function CheckPolicyModal({
  documentId,
  docContent,
  onClose,
  onSaved,
}: {
  documentId: string;
  docContent: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [rules, setRules] = useState<CheckRule[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [adding, setAdding] = useState(false);
  const [preview, setPreview] = useState<CheckResult[]>([]);
  const previewTimer = useRef<number | null>(null);
  // Named-policy state: which template this chain is linked to (if
  // any), the user's template library, and dirty-tracking so the
  // save button can be honest about blast radius.
  const [templates, setTemplates] = useState<CheckTemplate[]>([]);
  const [linkedId, setLinkedId] = useState<string>("");
  const [linkedName, setLinkedName] = useState<string>("");
  const [docsUsing, setDocsUsing] = useState<number>(0);
  const [dirty, setDirty] = useState(false);
  const [savingAs, setSavingAs] = useState(false);
  const [newName, setNewName] = useState("");

  useEffect(() => {
    let cancelled = false;
    Promise.all([
      api.getCheckPolicy(documentId),
      api.listCheckTemplates().catch(() => [] as CheckTemplate[]),
    ])
      .then(([p, ts]) => {
        if (cancelled) return;
        setRules(p.rules ?? []);
        setAdding((p.rules ?? []).length === 0);
        setTemplates(ts);
        setLinkedId(p.templateId ?? "");
        setLinkedName(p.templateName ?? "");
        setDocsUsing(p.docsUsingTemplate ?? 0);
        setDirty(false);
      })
      .catch(() => {
        if (!cancelled) {
          setRules([]);
          setAdding(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [documentId]);

  // Any rule mutation marks the editor dirty (drives the split-save).
  function mutateRules(fn: (rs: CheckRule[]) => CheckRule[]) {
    setRules((rs) => (rs ? fn(rs) : rs));
    setDirty(true);
  }

  // Switching policy in the dropdown: load its rules into the editor.
  function selectPolicy(id: string) {
    if (id === linkedId) return;
    if (id === "") {
      setLinkedId("");
      setLinkedName("");
      setDocsUsing(0);
      setDirty(true); // custom now — saving writes inline rules
      return;
    }
    const t = templates.find((x) => x.id === id);
    if (!t) return;
    setRules(t.rules);
    setLinkedId(t.id);
    setLinkedName(t.name);
    setDocsUsing(t.docsUsing ?? 0);
    setDirty(false); // freshly loaded from the template
    setAdding(false);
  }

  // Live preview: every edit re-evaluates the whole candidate rule
  // set against the doc, debounced. Results align to rules by index.
  useEffect(() => {
    if (!rules || rules.length === 0) {
      setPreview([]);
      return;
    }
    if (previewTimer.current != null) window.clearTimeout(previewTimer.current);
    previewTimer.current = window.setTimeout(() => {
      api
        .previewChecks(documentId, rules)
        .then((r) => setPreview(r.results))
        .catch(() => setPreview([]));
    }, 350);
    return () => {
      if (previewTimer.current != null) window.clearTimeout(previewTimer.current);
    };
  }, [rules, documentId]);

  const headings = useMemo(() => extractHeadings(docContent), [docContent]);

  function addRule(r: CheckRule) {
    mutateRules((rs) => [...rs, r]);
    setRules((rs) => rs ?? [r]);
    setAdding(false);
  }

  async function finish(msg: string) {
    toast.success(msg);
    onSaved();
    onClose();
  }

  function fail(err: unknown) {
    toast.error(err instanceof APIError ? err.message : "Couldn't save the checks.");
  }

  // Save for THIS doc only (inline rules; forks if it was linked).
  async function saveInline() {
    if (!rules || busy) return;
    setBusy(true);
    try {
      await api.putCheckPolicy(documentId, rules);
      await finish(
        rules.length === 0
          ? "Checks removed"
          : linkedId
            ? "Forked — this doc no longer follows the shared policy"
            : "Checks saved"
      );
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  }

  // Save edits INTO the shared policy (all linked docs update).
  async function saveToTemplate() {
    if (!rules || busy || !linkedId) return;
    setBusy(true);
    try {
      await api.updateCheckTemplate(linkedId, { rules });
      await api.linkCheckPolicy(documentId, linkedId); // ensure link
      await finish(`Saved to “${linkedName}” — every linked doc updated`);
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  }

  // Link an unmodified template selection.
  async function saveLink() {
    if (busy || !linkedId) return;
    setBusy(true);
    try {
      await api.linkCheckPolicy(documentId, linkedId);
      await finish(`Following “${linkedName}”`);
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  }

  // Promote current rules to a new named policy + link this doc.
  async function saveAsTemplate() {
    if (!rules || rules.length === 0 || busy || !newName.trim()) return;
    setBusy(true);
    try {
      const t = await api.createCheckTemplate(newName.trim(), rules);
      await api.linkCheckPolicy(documentId, t.id);
      await finish(`Policy “${t.name}” created — reusable on any doc`);
    } catch (err) {
      fail(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="fixed inset-0 z-40 bg-black/40 flex items-center justify-center p-4">
      <div className="bg-card border border-rule rounded-lg shadow-xl max-w-2xl w-full max-h-[90vh] flex flex-col overflow-hidden">
        <div className="px-5 py-3 border-b border-rule shrink-0">
          <div className="flex items-center justify-between">
            <h2 className="text-lg font-semibold">Checks</h2>
            <button
              onClick={onClose}
              disabled={busy}
              className="text-muted hover:text-ink text-sm"
            >
              ✕
            </button>
          </div>
          <div className="flex items-center gap-2 mt-1.5 text-xs">
            <span className="text-muted">Policy:</span>
            <select
              value={linkedId}
              onChange={(e) => selectPolicy(e.target.value)}
              className="text-xs border border-rule rounded px-2 py-1 bg-card text-ink max-w-[16rem]"
            >
              <option value="">Custom (this doc only)</option>
              {templates.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                  {t.docsUsing ? ` · ${t.docsUsing} doc${t.docsUsing === 1 ? "" : "s"}` : ""}
                </option>
              ))}
            </select>
            {linkedId && dirty && (
              <span className="text-amber-600 dark:text-amber-400">
                editing “{linkedName}” — shared with {Math.max(docsUsing, 1)} doc
                {docsUsing === 1 ? "" : "s"}
              </span>
            )}
            {linkedId && !dirty && (
              <span className="text-faint">
                edits here can update every linked doc
              </span>
            )}
          </div>
        </div>

        <div className="flex-1 min-h-0 overflow-auto p-5 space-y-3 text-sm">
          {rules === null && <div className="text-muted">Loading…</div>}

          {rules?.map((r, i) => (
            <RuleCard
              key={r.id ?? i}
              rule={r}
              result={preview[i]}
              headings={headings}
              onChange={(patch) =>
                mutateRules((rs) => rs.map((x, j) => (j === i ? { ...x, ...patch } : x)))
              }
              onRemove={() => mutateRules((rs) => rs.filter((_, j) => j !== i))}
            />
          ))}

          {rules !== null &&
            rules.length === 0 &&
            !linkedId &&
            templates.length > 0 && (
              <div className="mb-1">
                <div className="text-xs font-semibold uppercase tracking-wide text-muted mb-1.5">
                  Apply one of your policies
                </div>
                <div className="flex flex-wrap gap-1.5">
                  {templates.map((t) => (
                    <button
                      key={t.id}
                      onClick={() => selectPolicy(t.id)}
                      className="text-xs px-2.5 py-1 rounded-full border border-rule text-ink hover:border-accent hover:bg-accent-soft/40"
                    >
                      {t.name}
                      {t.docsUsing ? (
                        <span className="text-faint"> · {t.docsUsing}</span>
                      ) : null}
                    </button>
                  ))}
                </div>
              </div>
            )}

          {rules !== null &&
            (adding ? (
              <PresetGallery
                headings={headings}
                onPick={addRule}
                onCancel={rules.length > 0 ? () => setAdding(false) : undefined}
              />
            ) : (
              <button
                onClick={() => setAdding(true)}
                className="text-xs px-2.5 py-1.5 rounded border border-dashed border-rule text-muted hover:text-ink hover:border-ink w-full"
              >
                + Add a check
              </button>
            ))}
        </div>

        <div className="px-5 py-3 border-t border-rule flex items-center gap-2 shrink-0">
          {/* Save-as: promote current rules to a reusable named policy. */}
          {!linkedId && (rules?.length ?? 0) > 0 && !savingAs && (
            <button
              onClick={() => setSavingAs(true)}
              disabled={busy}
              className="text-xs text-accent hover:underline mr-auto"
            >
              Save as reusable policy…
            </button>
          )}
          {savingAs && (
            <span className="flex items-center gap-1.5 mr-auto">
              <input
                autoFocus
                type="text"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && saveAsTemplate()}
                placeholder="Policy name — e.g. PRD Standard"
                className="text-xs border border-rule rounded px-2 py-1 bg-card text-ink w-48"
              />
              <button
                onClick={saveAsTemplate}
                disabled={busy || !newName.trim()}
                className="text-xs px-2 py-1 rounded bg-accent text-accent-fg disabled:opacity-50"
              >
                Create
              </button>
              <button
                onClick={() => setSavingAs(false)}
                className="text-xs text-muted hover:text-ink"
              >
                cancel
              </button>
            </span>
          )}
          <span className="ml-auto" />
          <button
            onClick={onClose}
            disabled={busy}
            className="text-sm px-3 py-1.5 rounded text-muted hover:text-ink"
          >
            Cancel
          </button>
          {linkedId && dirty ? (
            <>
              <button
                onClick={saveInline}
                disabled={busy || rules === null}
                className="text-sm px-3 py-1.5 rounded border border-rule text-ink hover:border-ink"
                title="Detach from the shared policy and keep these rules just here"
              >
                This doc only
              </button>
              <button
                onClick={saveToTemplate}
                disabled={busy || rules === null}
                className="text-sm px-4 py-1.5 rounded bg-accent text-accent-fg font-medium hover:opacity-90 disabled:opacity-50"
              >
                {busy
                  ? "Saving…"
                  : `Save to “${linkedName}”${docsUsing > 1 ? ` (${docsUsing} docs)` : ""}`}
              </button>
            </>
          ) : linkedId ? (
            <button
              onClick={saveLink}
              disabled={busy}
              className="text-sm px-4 py-1.5 rounded bg-accent text-accent-fg font-medium hover:opacity-90 disabled:opacity-50"
            >
              {busy ? "Saving…" : `Follow “${linkedName}”`}
            </button>
          ) : (
            <button
              onClick={saveInline}
              disabled={busy || rules === null}
              className="text-sm px-4 py-1.5 rounded bg-accent text-accent-fg font-medium hover:opacity-90 disabled:opacity-50"
            >
              {busy ? "Saving…" : "Save checks"}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

/* ---------- preset gallery ---------- */

function PresetGallery({
  headings,
  onPick,
  onCancel,
}: {
  headings: string[];
  onPick: (r: CheckRule) => void;
  onCancel?: () => void;
}) {
  const presets: { title: string; desc: string; make: () => CheckRule }[] = [
    {
      title: "📑 Required sections",
      desc: "Pick headings this doc must always keep.",
      make: () => ({
        kind: "required_sections",
        label: "Sections",
        // Start with the doc's current headings pre-ticked — editing
        // is unticking, not typing.
        sections: headings.slice(0, 8),
      }),
    },
    {
      title: "🚫 No placeholder text",
      desc: "Fails on TBD, TODO, FIXME, XXX, lorem ipsum.",
      make: () => ({
        kind: "forbidden_phrases",
        label: "No placeholders",
        phrases: ["TBD", "TODO", "FIXME", "XXX", "lorem ipsum"],
      }),
    },
    {
      title: "🔤 Exact spelling of a term",
      desc: "e.g. always “Beamable”, never “beamable”.",
      make: () => ({ kind: "term_spelling", label: "Spelling", term: "" }),
    },
    {
      title: "🙊 Banned words or phrases",
      desc: "Plain words, one per line — no patterns needed.",
      make: () => ({ kind: "forbidden_phrases", label: "Banned phrases", phrases: [] }),
    },
    {
      title: "🪜 Heading depth limit",
      desc: "Keep structure sane — no h5/h6 rabbit holes.",
      make: () => ({ kind: "max_heading_depth", label: "Heading depth", maxDepth: 4 }),
    },
    {
      title: "⚙️ Advanced (regex)",
      desc: "Require or forbid a custom pattern.",
      make: () => ({ kind: "forbidden_text", label: "Custom rule", pattern: "" }),
    },
  ];
  return (
    <div>
      <div className="flex items-center justify-between mb-2">
        <div className="text-xs font-semibold uppercase tracking-wide text-muted">
          Add a check
        </div>
        {onCancel && (
          <button onClick={onCancel} className="text-xs text-muted hover:text-ink">
            cancel
          </button>
        )}
      </div>
      <div className="grid sm:grid-cols-2 gap-2">
        {presets.map((p) => (
          <button
            key={p.title}
            onClick={() => onPick(p.make())}
            className="text-left border border-rule rounded-lg p-3 hover:border-accent hover:bg-accent-soft/30 transition"
          >
            <div className="text-sm font-medium text-ink">{p.title}</div>
            <div className="text-xs text-muted mt-0.5">{p.desc}</div>
          </button>
        ))}
      </div>
    </div>
  );
}

/* ---------- one rule card with live status ---------- */

function RuleCard({
  rule,
  result,
  headings,
  onChange,
  onRemove,
}: {
  rule: CheckRule;
  result?: CheckResult;
  headings: string[];
  onChange: (patch: Partial<CheckRule>) => void;
  onRemove: () => void;
}) {
  return (
    <div className="border border-rule rounded-lg p-3 space-y-2">
      <div className="flex items-center gap-2">
        <input
          type="text"
          value={rule.label}
          onChange={(e) => onChange({ label: e.target.value })}
          className="flex-1 text-sm font-medium border-0 bg-transparent text-ink focus:outline-none"
          placeholder="Name this check"
        />
        {result && (
          <span
            title={result.detail || "Passing on the current text"}
            className={
              "text-[11px] px-2 py-0.5 rounded-full border cursor-help " +
              (result.pass
                ? "border-success/40 text-success"
                : "border-danger/40 text-danger")
            }
          >
            {result.pass ? "✓ passing now" : "✗ failing now"}
          </span>
        )}
        <button
          onClick={onRemove}
          className="text-muted hover:text-danger text-xs shrink-0"
          title="Remove this check"
        >
          ✕
        </button>
      </div>
      {result && !result.pass && result.detail && (
        <div className="text-[11px] text-danger/90">{result.detail}</div>
      )}

      {rule.kind === "required_sections" && (
        <SectionPicker
          headings={headings}
          selected={rule.sections ?? []}
          onChange={(sections) => onChange({ sections })}
        />
      )}
      {rule.kind === "forbidden_phrases" && (
        <div>
          <div className="text-[11px] text-faint mb-1">
            Words or phrases that must NOT appear (case doesn't matter). One
            per line.
          </div>
          <textarea
            rows={3}
            value={(rule.phrases ?? []).join("\n")}
            onChange={(e) =>
              onChange({ phrases: e.target.value.split("\n") })
            }
            onBlur={(e) =>
              onChange({
                phrases: e.target.value
                  .split("\n")
                  .map((s) => s.trim())
                  .filter(Boolean),
              })
            }
            placeholder={"TBD\nas we all know"}
            className="w-full text-xs border border-rule rounded px-2 py-1 bg-card text-ink resize-y"
          />
        </div>
      )}
      {rule.kind === "term_spelling" && (
        <div>
          <div className="text-[11px] text-faint mb-1">
            The exact spelling and capitalization. Any other casing of the
            same word fails the check.
          </div>
          <input
            type="text"
            value={rule.term ?? ""}
            onChange={(e) => onChange({ term: e.target.value })}
            placeholder="Beamable"
            className="w-full text-xs border border-rule rounded px-2 py-1 bg-card text-ink"
          />
        </div>
      )}
      {rule.kind === "max_heading_depth" && (
        <div className="flex items-center gap-2 text-xs text-muted">
          Deepest heading allowed:
          <select
            value={rule.maxDepth ?? 4}
            onChange={(e) => onChange({ maxDepth: Number(e.target.value) })}
            className="text-xs border border-rule rounded px-2 py-1 bg-card text-ink"
          >
            {[1, 2, 3, 4, 5, 6].map((d) => (
              <option key={d} value={d}>
                h{d}
              </option>
            ))}
          </select>
        </div>
      )}
      {(rule.kind === "forbidden_text" || rule.kind === "required_text") && (
        <div className="space-y-1">
          <div className="flex items-center gap-2 text-[11px] text-faint">
            <select
              value={rule.kind}
              onChange={(e) => onChange({ kind: e.target.value as CheckRule["kind"] })}
              className="text-xs border border-rule rounded px-1.5 py-0.5 bg-card text-ink"
            >
              <option value="forbidden_text">Must NOT match</option>
              <option value="required_text">Must match</option>
            </select>
            <span>
              Go regex — the live chip above tells you if it works.
            </span>
          </div>
          <input
            type="text"
            value={rule.pattern ?? ""}
            onChange={(e) => onChange({ pattern: e.target.value })}
            placeholder={"\\bdeprecated\\b"}
            className="w-full text-xs font-mono border border-rule rounded px-2 py-1 bg-card text-ink"
          />
        </div>
      )}
    </div>
  );
}

/* ---------- section picker: checkboxes over the doc's own headings ---------- */

function SectionPicker({
  headings,
  selected,
  onChange,
}: {
  headings: string[];
  selected: string[];
  onChange: (sections: string[]) => void;
}) {
  const [extra, setExtra] = useState("");
  const known = new Set(headings.map((h) => h.toLowerCase()));
  const custom = selected.filter((s) => !known.has(s.toLowerCase()));

  function toggle(h: string) {
    const has = selected.some((s) => s.toLowerCase() === h.toLowerCase());
    onChange(
      has
        ? selected.filter((s) => s.toLowerCase() !== h.toLowerCase())
        : [...selected, h]
    );
  }

  return (
    <div>
      <div className="text-[11px] text-faint mb-1">
        Tick the headings every revision must keep — pulled from this doc.
      </div>
      <div className="flex flex-wrap gap-1.5">
        {headings.map((h) => {
          const on = selected.some((s) => s.toLowerCase() === h.toLowerCase());
          return (
            <button
              key={h}
              onClick={() => toggle(h)}
              className={
                "text-xs px-2 py-0.5 rounded-full border transition " +
                (on
                  ? "border-accent bg-accent-soft text-ink"
                  : "border-rule text-muted hover:text-ink")
              }
            >
              {on ? "✓ " : ""}
              {h}
            </button>
          );
        })}
        {custom.map((h) => (
          <button
            key={h}
            onClick={() => toggle(h)}
            className="text-xs px-2 py-0.5 rounded-full border border-accent bg-accent-soft text-ink"
            title="Required but not present in the current doc"
          >
            ✓ {h} ⚠
          </button>
        ))}
      </div>
      <div className="flex items-center gap-1.5 mt-1.5">
        <input
          type="text"
          value={extra}
          onChange={(e) => setExtra(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && extra.trim()) {
              onChange([...selected, extra.trim()]);
              setExtra("");
            }
          }}
          placeholder="Require a heading that doesn't exist yet…"
          className="flex-1 text-xs border border-rule rounded px-2 py-1 bg-card text-ink"
        />
        <button
          onClick={() => {
            if (extra.trim()) {
              onChange([...selected, extra.trim()]);
              setExtra("");
            }
          }}
          className="text-xs px-2 py-1 rounded border border-rule text-muted hover:text-ink"
        >
          Add
        </button>
      </div>
    </div>
  );
}

/* ---------- tiny client-side heading extractor (mirrors backend) ---------- */

function extractHeadings(content: string): string[] {
  const out: string[] = [];
  let inFence = false;
  for (const line of content.split("\n")) {
    const t = line.trim();
    if (t.startsWith("```") || t.startsWith("~~~")) {
      inFence = !inFence;
      continue;
    }
    if (inFence) continue;
    const m = /^(#{1,6})\s+(.+)$/.exec(t);
    if (m) out.push(m[2].trim());
  }
  // Dedup, keep order.
  return [...new Set(out)];
}
