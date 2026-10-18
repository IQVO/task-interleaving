import { MemoryRouter, Route, Routes } from "react-router-dom";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { HintDetailRoute } from "./HintDetailScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { hint } from "../test/fixtures";

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/hints/:hintId" element={<HintDetailRoute />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("HintDetailScreen", () => {
  it("shows an active hint with its ranked candidates", async () => {
    const api = mockApi({ "GET /hints/hint-1": json(hint()) });
    renderAt("/hints/hint-1");
    expect(await screen.findByRole("heading", { name: "Hint hint-1" })).toBeInTheDocument();
    expect(api.calls[0].path).toBe("/hints/hint-1");
    expect(screen.getByText("Active")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "STN-07" })).toHaveAttribute("href", "/stations/STN-07");
    expect(screen.getByText("2026-10-17 12:01:30Z")).toBeInTheDocument();
    expect(screen.getByText("task-9c2e")).toBeInTheDocument();
    expect(screen.getByText("4.0 m")).toBeInTheDocument();
    expect(screen.getByText("10.5 m")).toBeInTheDocument();
    expect(screen.queryByText("Task taken")).toBeNull();
  });

  it("marks the candidate a consumed hint was resolved with", async () => {
    mockApi({
      "GET /hints/hint-1": json(hint({ state: "Consumed", consumedTaskId: "task-9d10", consumedAt: "2026-10-17T12:00:20Z" })),
    });
    renderAt("/hints/hint-1");
    expect(await screen.findByText("task-9d10 (taken)")).toBeInTheDocument();
    expect(screen.getByText("Task taken")).toBeInTheDocument();
    expect(screen.getByText("2026-10-17 12:00:20Z")).toBeInTheDocument();
  });

  it("shows when a hint was superseded", async () => {
    mockApi({ "GET /hints/hint-1": json(hint({ state: "Superseded", supersededAt: "2026-10-17T12:00:40Z" })) });
    renderAt("/hints/hint-1");
    expect(await screen.findByText("Superseded at")).toBeInTheDocument();
    expect(screen.getByText("2026-10-17 12:00:40Z")).toBeInTheDocument();
  });

  it("handles a hint without candidates", async () => {
    mockApi({ "GET /hints/hint-1": json(hint({ candidates: [] })) });
    renderAt("/hints/hint-1");
    expect(await screen.findByText("This hint carries no candidates.")).toBeInTheDocument();
  });

  it("explains an unknown hint", async () => {
    mockApi({ "GET /hints/hint-x": problem(404, "hint-not-found", "Hint not found", "interleaving hint not found") });
    renderAt("/hints/hint-x");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Hint not found");
    expect(alert).toHaveTextContent("HTTP 404");
  });
});
