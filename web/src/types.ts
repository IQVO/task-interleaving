/** Wire types of apis/openapi.yaml (camelCase, as the REST contract names them). */

export const HINT_STATES = ["Active", "Consumed", "Expired", "Superseded"] as const;
export type HintState = (typeof HINT_STATES)[number];

export const TASK_TYPES = ["PICK", "PACK", "REBIN", "SLAM", "RELOCATE", "REPLENISH"] as const;
export type TaskType = (typeof TASK_TYPES)[number];

export interface Candidate {
  rank: number;
  taskId: string;
  taskType: TaskType;
  location: string;
  detourM: number;
}

export interface Hint {
  hintId: string;
  stationId: string;
  siteId: string;
  state: HintState;
  afterTaskId: string;
  originLocation: string;
  issuedAt: string;
  expiresAt: string;
  policyVersion: number;
  candidates: Candidate[];
  consumedTaskId?: string;
  consumedAt?: string;
  supersededAt?: string;
  version: number;
}

export interface HintPage {
  items: Hint[];
  nextCursor?: string;
}

export interface ListHintsQuery {
  limit?: number;
  cursor?: string;
  stationId?: string;
  siteId?: string;
  state?: HintState;
}

export interface Point {
  xM: number;
  yM: number;
  zM: number;
}

export interface ActiveHint {
  hintId: string;
  state: HintState;
  issuedAt: string;
  expiresAt: string;
  policyVersion: number;
  candidates: Candidate[];
}

export interface Station {
  stationId: string;
  siteId: string;
  position: { location: string; point?: Point };
  lastCompletedTaskId: string;
  lastCompletedAt: string;
  activeHint?: ActiveHint;
  version: number;
}

export interface SitePolicy {
  siteId: string;
  maxDetourM: number;
  maxCandidates: number;
  hintTtlSeconds: number;
  eligibleTaskTypes: TaskType[];
  verticalWeight: number;
  version: number;
  declaredAt: string;
}

/** The PUT /sites/{siteId}/policy body. */
export interface PolicySettings {
  maxDetourM: number;
  maxCandidates: number;
  hintTtlSeconds: number;
  eligibleTaskTypes: TaskType[];
  verticalWeight: number;
}
