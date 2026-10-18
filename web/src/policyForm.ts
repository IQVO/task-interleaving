import { finiteNumber, MAX_HINT_TTL_SECONDS, wholeNumber } from "./lib";
import { TASK_TYPES } from "./types";
import type { PolicySettings, SitePolicy, TaskType } from "./types";

/** The policy form as the operator typed it (text for every number, so a
 *  half-typed value is never silently coerced). */
export interface PolicyDraft {
  maxDetourM: string;
  maxCandidates: string;
  hintTtlSeconds: string;
  verticalWeight: string;
  eligibleTaskTypes: TaskType[];
}

/** The defaults offered when a site has no policy yet. */
export const EMPTY_DRAFT: PolicyDraft = {
  maxDetourM: "",
  maxCandidates: "3",
  hintTtlSeconds: "90",
  verticalWeight: "1",
  eligibleTaskTypes: [],
};

export function draftOf(p: SitePolicy): PolicyDraft {
  return {
    maxDetourM: String(p.maxDetourM),
    maxCandidates: String(p.maxCandidates),
    hintTtlSeconds: String(p.hintTtlSeconds),
    verticalWeight: String(p.verticalWeight),
    eligibleTaskTypes: [...p.eligibleTaskTypes],
  };
}

export type PolicyValidation = { ok: true; settings: PolicySettings } | { ok: false; problems: string[] };

/** The API's validation rules (docs: PUT /sites/{siteId}/policy), worded for
 *  the operator before the request is made. The server stays the authority. */
export function validatePolicy(d: PolicyDraft): PolicyValidation {
  const problems: string[] = [];
  const maxDetourM = finiteNumber(d.maxDetourM);
  if (maxDetourM === null || maxDetourM <= 0) problems.push("Max detour must be a number greater than 0.");
  const maxCandidates = wholeNumber(d.maxCandidates);
  if (maxCandidates === null || maxCandidates < 1 || maxCandidates > 5) {
    problems.push("Max candidates must be a whole number from 1 to 5.");
  }
  const hintTtlSeconds = wholeNumber(d.hintTtlSeconds);
  if (hintTtlSeconds === null || hintTtlSeconds < 1 || hintTtlSeconds > MAX_HINT_TTL_SECONDS) {
    problems.push(`Hint TTL must be a whole number of seconds from 1 to ${MAX_HINT_TTL_SECONDS}.`);
  }
  const verticalWeight = finiteNumber(d.verticalWeight);
  if (verticalWeight === null || verticalWeight < 0) problems.push("Vertical weight must be a number of at least 0.");
  if (d.eligibleTaskTypes.length === 0) problems.push("Choose at least one eligible task type.");
  if (problems.length > 0) return { ok: false, problems };
  return {
    ok: true,
    settings: {
      maxDetourM: maxDetourM as number,
      maxCandidates: maxCandidates as number,
      hintTtlSeconds: hintTtlSeconds as number,
      verticalWeight: verticalWeight as number,
      // The contract's own order, whatever order the boxes were ticked in.
      eligibleTaskTypes: TASK_TYPES.filter((t) => d.eligibleTaskTypes.includes(t)),
    },
  };
}
