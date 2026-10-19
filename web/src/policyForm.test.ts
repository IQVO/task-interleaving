import { describe, expect, it } from "vitest";
import { draftOf, EMPTY_DRAFT, validatePolicy } from "./policyForm";
import type { SitePolicy } from "./types";

const valid = { ...EMPTY_DRAFT, maxDetourM: "25", eligibleTaskTypes: ["REPLENISH", "PICK"] as const };

describe("validatePolicy", () => {
  it("accepts a complete form and orders the task types like the contract", () => {
    const r = validatePolicy({ ...valid, eligibleTaskTypes: [...valid.eligibleTaskTypes] });
    expect(r).toEqual({
      ok: true,
      settings: { maxDetourM: 25, maxCandidates: 3, hintTtlSeconds: 90, verticalWeight: 1, eligibleTaskTypes: ["PICK", "REPLENISH"] },
    });
  });

  it("explains every bad field at once", () => {
    const r = validatePolicy({ maxDetourM: "0", maxCandidates: "6", hintTtlSeconds: "1.5", verticalWeight: "-1", eligibleTaskTypes: [] });
    expect(r.ok).toBe(false);
    if (!r.ok) {
      expect(r.problems).toHaveLength(5);
      expect(r.problems.join(" ")).toMatch(/Max detour.*Max candidates.*Hint TTL.*Vertical weight.*task type/);
    }
  });

  it("bounds candidates to 1..5 and the TTL to int32", () => {
    const base = { ...valid, eligibleTaskTypes: ["PICK" as const] };
    expect(validatePolicy({ ...base, maxCandidates: "1" }).ok).toBe(true);
    expect(validatePolicy({ ...base, maxCandidates: "5" }).ok).toBe(true);
    expect(validatePolicy({ ...base, maxCandidates: "0" }).ok).toBe(false);
    expect(validatePolicy({ ...base, hintTtlSeconds: "2147483647" }).ok).toBe(true);
    expect(validatePolicy({ ...base, hintTtlSeconds: "2147483648" }).ok).toBe(false);
    expect(validatePolicy({ ...base, verticalWeight: "0" }).ok).toBe(true);
  });
});

describe("draftOf", () => {
  it("turns a policy into editable text", () => {
    const p: SitePolicy = {
      siteId: "SITE-1", maxDetourM: 25.5, maxCandidates: 2, hintTtlSeconds: 60, eligibleTaskTypes: ["PICK"],
      verticalWeight: 1.5, version: 4, declaredAt: "2026-10-17T12:00:00Z",
    };
    expect(draftOf(p)).toEqual({ maxDetourM: "25.5", maxCandidates: "2", hintTtlSeconds: "60", verticalWeight: "1.5", eligibleTaskTypes: ["PICK"] });
  });
});
