import { describe, expect, it } from "vitest";
import { resolveInterleavingApiBase } from "./config";

describe("resolveInterleavingApiBase", () => {
  it("is this context's own /api/task-interleaving under the runtime origin", () => {
    expect(resolveInterleavingApiBase({ apiOrigin: "http://localhost:8000" }, true)).toBe(
      "http://localhost:8000/api/task-interleaving",
    );
  });

  it("drops trailing slashes of the origin", () => {
    expect(resolveInterleavingApiBase({ apiOrigin: "http://kong.local///" }, false)).toBe(
      "http://kong.local/api/task-interleaving",
    );
  });

  it("falls back to the developer port outside production", () => {
    expect(resolveInterleavingApiBase({}, false)).toBe("http://localhost:8080");
  });

  it("refuses to guess in production", () => {
    expect(() => resolveInterleavingApiBase({}, true)).toThrow(/apiOrigin is required/);
    expect(() => resolveInterleavingApiBase({ apiOrigin: "" }, true)).toThrow(/apiOrigin is required/);
  });
});
