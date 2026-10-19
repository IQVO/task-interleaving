import { describe, expect, it } from "vitest";
import { ApiError, declareSitePolicy, etag, getHint, getSitePolicy, getStation, listHints, toApiError, withQuery } from "./api";
import { INTERLEAVING_API_BASE } from "./config";
import { json, mockApi, problem } from "./test/fetchMock";
import { hint, policy, station } from "./test/fixtures";

describe("withQuery", () => {
  it("drops empty values and encodes the rest", () => {
    expect(withQuery("/hints", { limit: 25, state: "", stationId: undefined, siteId: "A B" })).toBe("/hints?limit=25&siteId=A+B");
    expect(withQuery("/hints", {})).toBe("/hints");
    expect(withQuery("/hints")).toBe("/hints");
  });
});

describe("etag", () => {
  it("is the quoted version", () => {
    expect(etag(3)).toBe('"3"');
  });
});

describe("the API base", () => {
  it("is this context's own /api/task-interleaving or the developer port, never a sibling", () => {
    expect(INTERLEAVING_API_BASE).toBe("http://localhost:8080");
  });
});

describe("requests", () => {
  it("lists hints with the filters as query parameters and no custom header", async () => {
    const api = mockApi({ "GET /hints": json({ items: [hint()], nextCursor: "abc" }) });
    const page = await listHints({ limit: 25, cursor: "c1", stationId: "STN-07", siteId: "SITE-1", state: "Active" });
    expect(page.nextCursor).toBe("abc");
    expect(page.items).toHaveLength(1);
    const call = api.calls[0];
    expect(call.url.startsWith(INTERLEAVING_API_BASE + "/hints?")).toBe(true);
    expect(Object.fromEntries(call.query)).toEqual({ limit: "25", cursor: "c1", stationId: "STN-07", siteId: "SITE-1", state: "Active" });
    expect(call.headers).toEqual({});
  });

  it("normalizes a missing item list and an empty cursor", async () => {
    mockApi({ "GET /hints": json({ nextCursor: "" }) });
    expect(await listHints({})).toEqual({ items: [], nextCursor: undefined });
  });

  it("reads one hint and one station by escaped id", async () => {
    const api = mockApi({
      "GET /hints/hint%201": json(hint({ hintId: "hint 1", candidates: undefined as never })),
      "GET /stations/STN-07": json(station({ activeHint: { ...station().activeHint!, candidates: undefined as never } })),
    });
    expect((await getHint("hint 1")).candidates).toEqual([]);
    expect((await getStation("STN-07")).activeHint?.candidates).toEqual([]);
    expect(api.calls.map((c) => c.path)).toEqual(["/hints/hint%201", "/stations/STN-07"]);
  });

  it("replaces a policy with If-Match = the quoted version it was edited at", async () => {
    const api = mockApi({ "PUT /sites/SITE-1/policy": json(policy({ version: 5 })) });
    const settings = { maxDetourM: 30, maxCandidates: 2, hintTtlSeconds: 60, verticalWeight: 1, eligibleTaskTypes: ["PICK" as const] };
    const saved = await declareSitePolicy("SITE-1", settings, 4);
    expect(saved.version).toBe(5);
    const call = api.calls[0];
    expect(call.headers).toEqual({ "Content-Type": "application/json", "If-Match": '"4"' });
    expect(call.body).toEqual(settings);
  });

  it("sends no If-Match for a first declaration", async () => {
    const api = mockApi({ "PUT /sites/SITE-9/policy": json(policy({ siteId: "SITE-9", version: 1 })) });
    await declareSitePolicy("SITE-9", { maxDetourM: 1, maxCandidates: 1, hintTtlSeconds: 1, verticalWeight: 1, eligibleTaskTypes: ["PICK"] }, undefined);
    expect(api.calls[0].headers).toEqual({ "Content-Type": "application/json" });
  });

  it("calls nothing but this context's API", async () => {
    const api = mockApi({
      "GET /sites/SITE-1/policy": json(policy()),
      "GET /hints": json({ items: [] }),
    });
    await getSitePolicy("SITE-1");
    await listHints({});
    for (const c of api.calls) expect(c.url.startsWith(INTERLEAVING_API_BASE + "/")).toBe(true);
  });
});

describe("errors", () => {
  it("keeps the problem's title, detail and slug", async () => {
    mockApi({ "GET /sites/SITE-1/policy": problem(404, "policy-not-found", "Interleaving policy not found", "the site has not declared an interleaving policy") });
    const err = await getSitePolicy("SITE-1").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const e = err as ApiError;
    expect([e.status, e.slug, e.title, e.detail]).toEqual([404, "policy-not-found", "Interleaving policy not found", "the site has not declared an interleaving policy"]);
    expect(e.message).toBe("the site has not declared an interleaving policy");
  });

  it("falls back to the status text for a body that is not a problem", async () => {
    mockApi({ "GET /hints/x": new Response("<html>", { status: 502, statusText: "Bad Gateway" }) });
    const e = (await getHint("x").catch((err: unknown) => err)) as ApiError;
    expect([e.status, e.title, e.slug, e.problem]).toEqual([502, "502 Bad Gateway", "", null]);
  });

  it("turns a network failure into an ApiError with status 0", async () => {
    mockApi({});
    const e = (await getHint("x").catch((err: unknown) => err)) as ApiError;
    expect(e.status).toBe(0);
    expect(e.title).toBe("Network error");
    expect(toApiError(new Error("boom")).detail).toBe("boom");
    expect(toApiError("text").detail).toBe("text");
    expect(toApiError(e)).toBe(e);
  });
});
