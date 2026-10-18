import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Card } from "@warehouse/ui-kit";
import { getStation } from "../api";
import { Form, FormRow, SubmitButton, TextField } from "../components/formkit";
import { CandidateTable, Facts, PageHeader, Stack, StatePill } from "../components/layout";
import { RequestView } from "../components/ProblemAlert";
import { useRequest } from "../hooks/useRequest";
import { idProblem, metres, utcLabel } from "../lib";
import type { Point, Station } from "../types";

/** `stations`: a lookup by station id, then `stations/:stationId`. */
export function StationLookupScreen() {
  const [stationId, setStationId] = useState("");
  const navigate = useNavigate();
  const id = stationId.trim();
  const problem = id === "" ? null : idProblem("station", id);
  const go = () => {
    if (id !== "" && problem === null) navigate(encodeURIComponent(id));
  };
  return (
    <Stack gap={5}>
      <PageHeader title="Stations" subtitle="Where a station stands and which hint it holds right now" />
      <Card title="Look up a station">
        <Form label="Station lookup" onSubmit={go}>
          <FormRow>
            <TextField
              label="Station"
              value={stationId}
              onChange={setStationId}
              placeholder="STN-07"
              hint={problem ?? "A station is known once it has completed a task"}
            />
            <SubmitButton disabled={id === "" || problem !== null}>View station</SubmitButton>
          </FormRow>
        </Form>
      </Card>
    </Stack>
  );
}

/** The route element of `stations/:stationId`. */
export function StationRoute() {
  const { stationId } = useParams();
  return stationId ? <StationScreen stationId={stationId} /> : null;
}

function pointLabel(p: Point | undefined): string {
  return p ? `${p.xM}, ${p.yM}, ${p.zM} m` : "geometry unknown";
}

/**
 * One station: GET /stations/{stationId} -- its position (location code and
 * point), the task it completed last and its active hint with the ranked
 * candidates. A station with no recorded completion is a 404 station-not-found.
 */
export function StationScreen({ stationId }: { stationId: string }) {
  const station = useRequest(() => getStation(stationId), `station|${stationId}`);
  return (
    <Stack gap={5}>
      <PageHeader title={`Station ${stationId}`} subtitle="Position, last completion and the hint it holds" />
      <RequestView state={station} what="the station">
        {(s) => <StationBody station={s} />}
      </RequestView>
      <Link to="../stations">← Look up another station</Link>
    </Stack>
  );
}

function StationBody({ station }: { station: Station }) {
  const hint = station.activeHint;
  return (
    <>
      <Card title="Station">
        <Facts
          items={[
            { label: "Site", value: station.siteId },
            { label: "Position", value: `${station.position.location} (${pointLabel(station.position.point)})` },
            { label: "Last completed task", value: station.lastCompletedTaskId },
            { label: "Completed at", value: utcLabel(station.lastCompletedAt) },
            { label: "Version", value: String(station.version) },
          ]}
        />
      </Card>
      <Card title="Active hint">
        {hint ? (
          <Stack gap={3}>
            <Facts
              items={[
                { label: "Hint", value: <Link to={`../hints/${encodeURIComponent(hint.hintId)}`}>{hint.hintId}</Link> },
                { label: "State", value: <StatePill state={hint.state} /> },
                { label: "Issued", value: utcLabel(hint.issuedAt) },
                { label: "Expires", value: utcLabel(hint.expiresAt) },
                { label: "Policy version", value: String(hint.policyVersion) },
                { label: "Nearest detour", value: hint.candidates[0] ? metres(hint.candidates[0].detourM) : "—" },
              ]}
            />
            <CandidateTable candidates={hint.candidates} />
          </Stack>
        ) : (
          "This station holds no active hint: its site has no policy, no open task qualified, or the last hint expired."
        )}
      </Card>
    </>
  );
}
