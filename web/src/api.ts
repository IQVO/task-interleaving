import { INTERLEAVING_API_BASE } from "./config";
import type { Hint, HintPage, ListHintsQuery, PolicySettings, SitePolicy, Station } from "./types";

/** RFC 7807 problem+json body every error response from task-interleaving
 *  returns (`type` = https://errors.task-interleaving.warehouse-systems.dev/<slug>). */
export interface ProblemDetails {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  instance?: string;
}

/**
 * Every failed call -- an HTTP error with a problem+json body, an HTTP error
 * without one, or a network failure -- surfaces as one ApiError, so a screen
 * has a single shape to render: `title` (the problem's category) and `detail`
 * (what exactly was wrong), plus `slug` to branch on a specific problem
 * (e.g. `policy-not-found`, `version-mismatch`).
 */
export class ApiError extends Error {
  readonly status: number;
  readonly problem: ProblemDetails | null;
  readonly title: string;
  readonly detail: string;
  /** Last path segment of problem.type (e.g. "policy-not-found"); "" when unknown. */
  readonly slug: string;

  constructor(status: number, problem: ProblemDetails | null, fallbackTitle: string) {
    const title = problem?.title || fallbackTitle;
    const detail = problem?.detail ?? "";
    super(detail || title);
    this.name = "ApiError";
    this.status = status;
    this.problem = problem;
    this.title = title;
    this.detail = detail;
    this.slug = problem?.type ? (problem.type.split("/").pop() ?? "") : "";
  }
}

/** Normalizes anything thrown by a request into an ApiError. */
export function toApiError(err: unknown): ApiError {
  if (err instanceof ApiError) return err;
  const message = err instanceof Error ? err.message : String(err);
  return new ApiError(0, { detail: message }, "Network error");
}

type Query = Record<string, string | number | boolean | undefined>;

export function withQuery(path: string, query?: Query): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== "") params.set(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

/** The strong entity tag of a resource version: the quoted number, `"3"`. */
export function etag(version: number): string {
  return `"${version}"`;
}

/**
 * The one door to the service. Only this context's own API is ever called
 * (`INTERLEAVING_API_BASE` = <apiOrigin>/api/task-interleaving): no sibling
 * context, no reports service. GETs send no custom header, so they stay
 * simple CORS requests; the policy PUT sends `Content-Type` and, when it
 * replaces a policy, `If-Match` (Kong's CORS policy allows both).
 */
async function request<T>(method: "GET" | "PUT", path: string, opts: { body?: unknown; ifMatch?: string } = {}): Promise<T> {
  const headers: Record<string, string> = {};
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";
  if (opts.ifMatch !== undefined) headers["If-Match"] = opts.ifMatch;
  let res: Response;
  try {
    res = await fetch(`${INTERLEAVING_API_BASE}${path}`, {
      method,
      ...(Object.keys(headers).length > 0 ? { headers } : {}),
      ...(opts.body === undefined ? {} : { body: JSON.stringify(opts.body) }),
    });
  } catch (err) {
    throw toApiError(err);
  }
  if (!res.ok) {
    let problem: ProblemDetails | null = null;
    try {
      problem = (await res.json()) as ProblemDetails;
    } catch {
      // non-JSON error body -- fall through with problem = null
    }
    throw new ApiError(res.status, problem, `${res.status} ${res.statusText}`.trim());
  }
  return (await res.json()) as T;
}

const seg = encodeURIComponent;

/** A hint as the UI uses it: the candidate list is never undefined. */
function normalizeHint(h: Hint): Hint {
  return { ...h, candidates: h.candidates ?? [] };
}

/** GET /hints?limit=&cursor=&stationId=&siteId=&state= -- newest first. */
export async function listHints(q: ListHintsQuery): Promise<HintPage> {
  const res = await request<HintPage>(
    "GET",
    withQuery("/hints", { limit: q.limit, cursor: q.cursor, stationId: q.stationId, siteId: q.siteId, state: q.state }),
  );
  return { items: (res.items ?? []).map(normalizeHint), nextCursor: res.nextCursor || undefined };
}

/** GET /hints/{hintId} (404 hint-not-found). */
export async function getHint(hintId: string): Promise<Hint> {
  return normalizeHint(await request<Hint>("GET", `/hints/${seg(hintId)}`));
}

/** GET /stations/{stationId} (404 station-not-found until a completion is recorded). */
export async function getStation(stationId: string): Promise<Station> {
  const s = await request<Station>("GET", `/stations/${seg(stationId)}`);
  return s.activeHint ? { ...s, activeHint: { ...s.activeHint, candidates: s.activeHint.candidates ?? [] } } : s;
}

/** GET /sites/{siteId}/policy (404 policy-not-found: the site is inert). */
export async function getSitePolicy(siteId: string): Promise<SitePolicy> {
  const p = await request<SitePolicy>("GET", `/sites/${seg(siteId)}/policy`);
  return { ...p, eligibleTaskTypes: p.eligibleTaskTypes ?? [] };
}

/**
 * PUT /sites/{siteId}/policy. `currentVersion` is the version the operator
 * edited: sent as `If-Match` so a stale edit is a 412 `version-mismatch` and a
 * lost race a 409 `concurrent-modification`. The first declaration of a site
 * (no policy yet) passes `undefined` and sends no `If-Match`.
 */
export async function declareSitePolicy(
  siteId: string,
  settings: PolicySettings,
  currentVersion: number | undefined,
): Promise<SitePolicy> {
  const p = await request<SitePolicy>("PUT", `/sites/${seg(siteId)}/policy`, {
    body: settings,
    ...(currentVersion === undefined ? {} : { ifMatch: etag(currentVersion) }),
  });
  return { ...p, eligibleTaskTypes: p.eligibleTaskTypes ?? [] };
}
