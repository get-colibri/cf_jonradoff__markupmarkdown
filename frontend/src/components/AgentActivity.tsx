import { useCallback, useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../api";
import { useAuth } from "../auth";
import TimeAgo from "./TimeAgo";
import type { AgentActivityItem } from "../types";

/** Top-nav agent status: a ⚡ that pulses while your auto-reviewer is
 * working, with a dropdown of the last 24h of runs — running / done
 * (state + suggestion count, including "no issues found") / failed
 * (with the reason). Hidden entirely when there's nothing to report,
 * so it costs zero attention until an agent is actually doing
 * something on your behalf. */
export default function AgentActivity() {
  const { user } = useAuth();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<AgentActivityItem[]>([]);
  const [running, setRunning] = useState(0);
  const menuRef = useRef<HTMLDivElement>(null);

  const refresh = useCallback(async () => {
    if (!user) return;
    try {
      const res = await api.getAgentActivity();
      setItems(res.items);
      setRunning(res.running);
    } catch {
      /* transient — next poll reconciles */
    }
  }, [user]);

  // Poll fast while something is running (the whole point is watching
  // it finish), slow otherwise.
  useEffect(() => {
    if (!user) {
      setItems([]);
      setRunning(0);
      return;
    }
    refresh();
    const onFocus = () => refresh();
    window.addEventListener("focus", onFocus);
    const id = window.setInterval(
      () => {
        if (document.visibilityState === "visible") refresh();
      },
      running > 0 ? 8_000 : 60_000
    );
    return () => {
      window.removeEventListener("focus", onFocus);
      window.clearInterval(id);
    };
  }, [user, refresh, running]);

  useEffect(() => {
    function onClick(e: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", onClick);
    return () => document.removeEventListener("mousedown", onClick);
  }, []);

  if (!user || items.length === 0) return null;

  return (
    <div className="relative" ref={menuRef}>
      <button
        onClick={() => {
          setOpen((o) => !o);
          if (!open) refresh();
        }}
        title={
          running > 0
            ? `${running} auto-review${running === 1 ? "" : "s"} running`
            : "Agent activity (last 24h)"
        }
        className="w-8 h-8 rounded-md flex items-center justify-center text-muted hover:text-ink hover:bg-soft relative"
      >
        <span className={running > 0 ? "animate-pulse text-accent" : ""}>⚡</span>
        {running > 0 && (
          <span className="absolute top-0 right-0 -mt-1 -mr-1 min-w-[1.1rem] h-[1.1rem] px-1 rounded-full bg-accent text-accent-fg text-[10px] font-semibold flex items-center justify-center tabular-nums">
            {running}
          </span>
        )}
      </button>

      {open && (
        <div className="absolute right-0 mt-2 w-96 max-h-[26rem] bg-card border border-rule rounded-lg shadow-lg overflow-hidden z-30 flex flex-col">
          <div className="px-3 py-2 border-b border-rule font-medium text-sm shrink-0">
            Agent activity
          </div>
          <div className="flex-1 min-h-0 overflow-auto">
            <ul className="divide-y divide-rule">
              {items.map((it) => (
                <li key={it.id}>
                  <button
                    onClick={() => {
                      setOpen(false);
                      navigate(`/d/${it.documentId}`);
                    }}
                    className="w-full text-left px-3 py-2.5 hover:bg-soft"
                  >
                    <div className="flex items-center gap-2 text-sm">
                      <StatusDot status={it.autoStatus} />
                      <span className="text-ink truncate flex-1">
                        {it.documentTitle}
                      </span>
                      <span className="text-[10px] text-faint shrink-0">
                        <TimeAgo iso={it.createdAt} />
                      </span>
                    </div>
                    <div className="text-xs text-muted mt-0.5 pl-5">
                      {it.reviewerName} ·{" "}
                      {it.autoStatus === "running"
                        ? "reviewing now…"
                        : it.autoStatus === "failed"
                          ? `failed — ${it.autoError || "unknown error"}`
                          : describeResult(it)}
                    </div>
                  </button>
                </li>
              ))}
            </ul>
          </div>
        </div>
      )}
    </div>
  );
}

function describeResult(it: AgentActivityItem): string {
  const state =
    it.autoResultState === "approved"
      ? "approved"
      : it.autoResultState === "changes_requested"
        ? "requested changes"
        : "commented";
  if (it.autoSuggestions && it.autoSuggestions > 0) {
    return `${state} · ${it.autoSuggestions} suggestion${it.autoSuggestions === 1 ? "" : "s"}`;
  }
  return it.autoResultState === "approved"
    ? "approved · no issues found"
    : state;
}

function StatusDot({ status }: { status: string }) {
  return (
    <span
      className={
        "w-2.5 h-2.5 rounded-full shrink-0 " +
        (status === "running"
          ? "bg-accent animate-pulse"
          : status === "failed"
            ? "bg-danger"
            : "bg-success")
      }
      aria-hidden
    />
  );
}
