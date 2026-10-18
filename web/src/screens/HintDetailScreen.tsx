import { Link, useParams } from "react-router-dom";
import { Card } from "@warehouse/ui-kit";
import { getHint } from "../api";
import { CandidateTable, Facts, PageHeader, Stack, StatePill } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { utcLabel } from "../lib";
import type { Hint } from "../types";

/** The route element of `hints/:hintId`. */
export function HintDetailRoute() {
  const { hintId } = useParams();
  return hintId ? <HintDetailScreen hintId={hintId} /> : null;
}

/**
 * One hint: GET /hints/{hintId} -- where it was issued, the ranked candidates
 * and how it ended (the candidate taken, or the instant it was superseded).
 * A hint is advice, never an assignment.
 */
export function HintDetailScreen({ hintId }: { hintId: string }) {
  const hint = useRequest(() => getHint(hintId), `hint|${hintId}`);
  return (
    <Stack gap={5}>
      <PageHeader title={`Hint ${hintId}`} subtitle="An advisory ranking of the open tasks nearest to where the operator stands" />
      <RequestView state={hint} what="the hint">
        {(h) => <HintBody hint={h} />}
      </RequestView>
      <Link to="../hints">← All hints</Link>
    </Stack>
  );
}

function HintBody({ hint }: { hint: Hint }) {
  return (
    <>
      <Card title="Hint">
        <Facts
          items={[
            { label: "State", value: <StatePill state={hint.state} /> },
            { label: "Station", value: <Link to={`../stations/${encodeURIComponent(hint.stationId)}`}>{hint.stationId}</Link> },
            { label: "Site", value: hint.siteId },
            { label: "After task", value: hint.afterTaskId },
            { label: "Issued from", value: hint.originLocation },
            { label: "Issued", value: utcLabel(hint.issuedAt) },
            { label: "Expires", value: utcLabel(hint.expiresAt) },
            { label: "Policy version", value: String(hint.policyVersion) },
            ...(hint.consumedTaskId ? [{ label: "Task taken", value: hint.consumedTaskId }] : []),
            ...(hint.consumedAt ? [{ label: "Taken at", value: utcLabel(hint.consumedAt) }] : []),
            ...(hint.supersededAt ? [{ label: "Superseded at", value: utcLabel(hint.supersededAt) }] : []),
            { label: "Version", value: String(hint.version) },
          ]}
        />
      </Card>
      <Card title="Candidates">
        {hint.candidates.length === 0 ? (
          "This hint carries no candidates."
        ) : (
          <CandidateTable candidates={hint.candidates} highlightTaskId={hint.consumedTaskId} />
        )}
      </Card>
    </>
  );
}
