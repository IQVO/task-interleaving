import { useState } from "react";
import { Link } from "react-router-dom";
import { Card, DataTable } from "@warehouse/ui-kit";
import { listHints } from "../api";
import { Form, FormRow, SelectField, SubmitButton, TextField } from "../components/formkit";
import { PageHeader, Stack, StatePill } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { optionalIdProblem, utcLabel } from "../lib";
import { HINT_STATES } from "../types";
import type { Hint, HintState } from "../types";

const STATE_OPTIONS = HINT_STATES.map((s) => ({ value: s, label: s }));

interface Filters {
  state: "" | HintState;
  siteId: string;
  stationId: string;
}

const INITIAL: Filters = { state: "", siteId: "", stationId: "" };

/**
 * Hint history: GET /hints, newest first, a page at a time, optionally
 * narrowed to one station, one site and one state. Paging follows the opaque
 * `nextCursor`; the cursors already visited are kept so "Previous" walks back
 * without the API having to support it. Changing the filters starts again at
 * the first page.
 */
export function HintListScreen() {
  const [draft, setDraft] = useState<Filters>(INITIAL);
  const [applied, setApplied] = useState<Filters>(INITIAL);
  // cursors[i] is the cursor of page i+1 ("" = the first page).
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];
  const page = useRequest(
    () =>
      listHints({
        limit: 25,
        cursor: cursor || undefined,
        stationId: applied.stationId || undefined,
        siteId: applied.siteId || undefined,
        state: applied.state || undefined,
      }),
    `hints|${applied.state}|${applied.siteId}|${applied.stationId}|${cursor}`,
  );

  const siteProblem = optionalIdProblem("site", draft.siteId.trim());
  const stationProblem = optionalIdProblem("station", draft.stationId.trim());
  const apply = () => {
    if (siteProblem || stationProblem) return;
    setApplied({ state: draft.state, siteId: draft.siteId.trim(), stationId: draft.stationId.trim() });
    setCursors([""]);
  };

  return (
    <Stack gap={5}>
      <PageHeader
        title="Interleaving hints"
        subtitle="task-interleaving · the advice issued to stations and how each hint ended"
      />
      <Card title="Filters">
        <Form label="Hint filters" onSubmit={apply}>
          <FormRow>
            <SelectField
              label="State"
              value={draft.state}
              onChange={(v) => setDraft({ ...draft, state: v as Filters["state"] })}
              options={STATE_OPTIONS}
              emptyLabel="Any state"
            />
            <TextField
              label="Station"
              value={draft.stationId}
              onChange={(v) => setDraft({ ...draft, stationId: v })}
              placeholder="Any station"
              hint={stationProblem ?? "Optional"}
            />
            <TextField
              label="Site"
              value={draft.siteId}
              onChange={(v) => setDraft({ ...draft, siteId: v })}
              placeholder="Any site"
              hint={siteProblem ?? "Optional"}
            />
            <SubmitButton disabled={siteProblem !== null || stationProblem !== null}>Apply filters</SubmitButton>
          </FormRow>
        </Form>
      </Card>
      <Card
        title={`Page ${cursors.length}`}
        actions={
          <div style={{ display: "flex", gap: "var(--wh-space-2)" }}>
            <SubmitButton
              type="button"
              secondary
              disabled={cursors.length === 1 || page.status === "loading"}
              onClick={() => setCursors(cursors.slice(0, -1))}
            >
              Previous page
            </SubmitButton>
            <SubmitButton
              type="button"
              secondary
              disabled={page.status !== "success" || !page.data.nextCursor}
              onClick={() => {
                if (page.status === "success" && page.data.nextCursor) {
                  setCursors([...cursors, page.data.nextCursor]);
                }
              }}
            >
              Next page
            </SubmitButton>
          </div>
        }
      >
        <RequestView
          state={page}
          what="hints"
          isEmpty={(p) => p.items.length === 0}
          empty={cursors.length === 1 ? "No hints match these filters." : "No more hints on this page."}
        >
          {(p) => <HintTable hints={p.items} />}
        </RequestView>
      </Card>
    </Stack>
  );
}

function HintTable({ hints }: { hints: Hint[] }) {
  return (
    <DataTable
      rowKey={(h) => h.hintId}
      rows={hints}
      columns={[
        {
          key: "hint",
          header: "Hint",
          // Relative link: `..` is the parent route (this remote's mount
          // point), so it works under the console's /task-interleaving/*
          // and standalone at /.
          render: (h) => <Link to={`../hints/${encodeURIComponent(h.hintId)}`}>{h.hintId}</Link>,
        },
        {
          key: "station",
          header: "Station",
          render: (h) => <Link to={`../stations/${encodeURIComponent(h.stationId)}`}>{h.stationId}</Link>,
        },
        { key: "site", header: "Site", render: (h) => h.siteId },
        { key: "state", header: "State", render: (h) => <StatePill state={h.state} /> },
        { key: "issued", header: "Issued", render: (h) => utcLabel(h.issuedAt) },
        { key: "expires", header: "Expires", render: (h) => utcLabel(h.expiresAt) },
        { key: "candidates", header: "Candidates", align: "right", render: (h) => String(h.candidates.length) },
      ]}
    />
  );
}
