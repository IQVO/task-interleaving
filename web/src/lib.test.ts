import { describe, expect, it } from "vitest";
import { finiteNumber, idProblem, metres, optionalIdProblem, utcLabel, wholeNumber } from "./lib";

describe("utcLabel", () => {
  it("formats an instant to the second in UTC", () => {
    expect(utcLabel("2026-10-17T12:00:30+00:00")).toBe("2026-10-17 12:00:30Z");
    expect(utcLabel("2026-10-17T09:00:30-03:00")).toBe("2026-10-17 12:00:30Z");
  });
  it("shows a dash for nothing and the raw text for garbage", () => {
    expect(utcLabel(undefined)).toBe("—");
    expect(utcLabel("soon")).toBe("soon");
  });
});

describe("metres", () => {
  it("shows one decimal", () => {
    expect(metres(4)).toBe("4.0 m");
    expect(metres(12.345)).toBe("12.3 m");
  });
});

describe("idProblem", () => {
  it("accepts a plain id", () => {
    expect(idProblem("site", "SITE-1")).toBeNull();
    expect(idProblem("site", "a".repeat(64))).toBeNull();
  });
  it("names what is wrong", () => {
    expect(idProblem("station", "")).toMatch(/Enter a station id/);
    expect(idProblem("station", "a".repeat(65))).toMatch(/at most 64/);
    expect(idProblem("site", "a b")).toMatch(/cannot contain/);
    expect(idProblem("site", "a/b")).toMatch(/cannot contain/);
    expect(idProblem("site", "a\tb")).toMatch(/cannot contain/);
  });
  it("lets an optional filter be empty", () => {
    expect(optionalIdProblem("site", "")).toBeNull();
    expect(optionalIdProblem("site", "a/b")).toMatch(/cannot contain/);
  });
});

describe("number parsing", () => {
  it("finiteNumber", () => {
    expect(finiteNumber(" 25.5 ")).toBe(25.5);
    expect(finiteNumber("")).toBeNull();
    expect(finiteNumber("abc")).toBeNull();
    expect(finiteNumber("Infinity")).toBeNull();
  });
  it("wholeNumber", () => {
    expect(wholeNumber("90")).toBe(90);
    expect(wholeNumber("0")).toBe(0);
    expect(wholeNumber("12.5")).toBeNull();
    expect(wholeNumber("1e3")).toBeNull();
    expect(wholeNumber("-1")).toBeNull();
    expect(wholeNumber("")).toBeNull();
  });
});
