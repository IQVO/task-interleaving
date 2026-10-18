import type { ReactNode } from "react";
import { DataTable, StatusPill } from "@warehouse/ui-kit";
import { metres } from "../lib";
import type { Candidate, HintState } from "../types";

export function PageHeader({ title, subtitle }: { title: string; subtitle: ReactNode }) {
  return (
    <div>
      <h1 style={{ fontSize: "var(--wh-font-size-2xl)", margin: 0 }}>{title}</h1>
      <p style={{ color: "var(--wh-color-text-muted)", marginTop: 4 }}>{subtitle}</p>
    </div>
  );
}

export function Stack({ children, gap = 4 }: { children: ReactNode; gap?: 3 | 4 | 5 }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: `var(--wh-space-${gap})` }}>
      {children}
    </div>
  );
}

/** A label/value list (dl) for a record's scalar fields. */
export function Facts({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl
      style={{
        display: "grid",
        gridTemplateColumns: "max-content 1fr",
        gap: "var(--wh-space-2) var(--wh-space-4)",
        margin: 0,
        fontSize: "var(--wh-font-size-sm)",
      }}
    >
      {items.map((item) => (
        <div key={item.label} style={{ display: "contents" }}>
          <dt style={{ color: "var(--wh-color-text-muted)" }}>{item.label}</dt>
          <dd style={{ margin: 0 }}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

type Tone = "neutral" | "progress" | "success" | "warning" | "danger";

/** A hint's lifecycle is task-interleaving's own, so StatusPill's tone
 *  override carries it. */
const STATE_TONE: Record<HintState, Tone> = {
  Active: "progress",
  Consumed: "success",
  Expired: "warning",
  Superseded: "neutral",
};

export function StatePill({ state }: { state: HintState }) {
  return <StatusPill status={state} tone={STATE_TONE[state]} size="sm" />;
}

/** The ranked candidates of a hint, nearest first. */
export function CandidateTable({ candidates, highlightTaskId }: { candidates: Candidate[]; highlightTaskId?: string }) {
  return (
    <DataTable
      rowKey={(c) => c.taskId}
      rows={candidates}
      columns={[
        { key: "rank", header: "Rank", align: "right", render: (c) => String(c.rank) },
        {
          key: "task",
          header: "Task",
          render: (c) => (c.taskId === highlightTaskId ? <strong>{c.taskId} (taken)</strong> : c.taskId),
        },
        { key: "type", header: "Type", render: (c) => c.taskType },
        { key: "location", header: "Location", render: (c) => c.location },
        { key: "detour", header: "Detour", align: "right", render: (c) => metres(c.detourM) },
      ]}
    />
  );
}
