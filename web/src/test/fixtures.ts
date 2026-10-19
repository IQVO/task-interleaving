import type { ActiveHint, Candidate, Hint, SitePolicy, Station } from "../types";

export const CANDIDATES: Candidate[] = [
  { rank: 1, taskId: "task-9c2e", taskType: "PICK", location: "B-01", detourM: 4 },
  { rank: 2, taskId: "task-9d10", taskType: "REPLENISH", location: "B-02", detourM: 10.5 },
];

export function hint(over: Partial<Hint> = {}): Hint {
  return {
    hintId: "hint-1",
    stationId: "STN-07",
    siteId: "SITE-1",
    state: "Active",
    afterTaskId: "task-8a1f",
    originLocation: "A-01",
    issuedAt: "2026-10-17T12:00:00Z",
    expiresAt: "2026-10-17T12:01:30Z",
    policyVersion: 2,
    candidates: CANDIDATES,
    version: 1,
    ...over,
  };
}

export const ACTIVE_HINT: ActiveHint = {
  hintId: "hint-1",
  state: "Active",
  issuedAt: "2026-10-17T12:00:00Z",
  expiresAt: "2026-10-17T12:01:30Z",
  policyVersion: 2,
  candidates: CANDIDATES,
};

export function station(over: Partial<Station> = {}): Station {
  return {
    stationId: "STN-07",
    siteId: "SITE-1",
    position: { location: "A-01", point: { xM: 0, yM: 1.5, zM: 0 } },
    lastCompletedTaskId: "task-8a1f",
    lastCompletedAt: "2026-10-17T12:00:00Z",
    activeHint: ACTIVE_HINT,
    version: 3,
    ...over,
  };
}

export function policy(over: Partial<SitePolicy> = {}): SitePolicy {
  return {
    siteId: "SITE-1",
    maxDetourM: 25,
    maxCandidates: 3,
    hintTtlSeconds: 90,
    eligibleTaskTypes: ["PICK", "REPLENISH"],
    verticalWeight: 1.5,
    version: 4,
    declaredAt: "2026-10-17T11:00:00Z",
    ...over,
  };
}
