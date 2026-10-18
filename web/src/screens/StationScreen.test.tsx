import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { StationLookupScreen, StationRoute } from "./StationScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { station } from "../test/fixtures";

function Where() {
  return <div data-testid="where">{useLocation().pathname}</div>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/stations" element={<StationLookupScreen />} />
        <Route path="/stations/:stationId" element={<StationRoute />} />
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

describe("StationLookupScreen", () => {
  it("opens the station, escaping the id", async () => {
    renderAt("/stations");
    const user = userEvent.setup();
    expect(screen.getByRole("button", { name: "View station" })).toBeDisabled();
    await user.type(screen.getByLabelText("Station"), "STN-07");
    await user.click(screen.getByRole("button", { name: "View station" }));
    expect(screen.getByTestId("where")).toHaveTextContent("/stations/STN-07");
  });

  it("refuses a malformed id before asking", async () => {
    renderAt("/stations");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Station"), "a/b");
    expect(screen.getByRole("button", { name: "View station" })).toBeDisabled();
    expect(screen.getByText(/cannot contain/)).toBeInTheDocument();
  });
});

describe("StationScreen", () => {
  it("shows the position and the active hint with its candidates", async () => {
    mockApi({ "GET /stations/STN-07": json(station()) });
    renderAt("/stations/STN-07");
    expect(await screen.findByRole("heading", { name: "Station STN-07" })).toBeInTheDocument();
    expect(screen.getByText("A-01 (0, 1.5, 0 m)")).toBeInTheDocument();
    expect(screen.getByText("task-8a1f")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "hint-1" })).toHaveAttribute("href", "/hints/hint-1");
    expect(screen.getByText("task-9c2e")).toBeInTheDocument();
    // the nearest-detour fact and the candidate table row
    expect(screen.getAllByText("4.0 m")).toHaveLength(2);
  });

  it("says when the geometry of the position is unknown", async () => {
    mockApi({ "GET /stations/STN-07": json(station({ position: { location: "A-01" } })) });
    renderAt("/stations/STN-07");
    expect(await screen.findByText("A-01 (geometry unknown)")).toBeInTheDocument();
  });

  it("explains a station without an active hint", async () => {
    mockApi({ "GET /stations/STN-07": json(station({ activeHint: undefined })) });
    renderAt("/stations/STN-07");
    expect(await screen.findByText(/holds no active hint/)).toBeInTheDocument();
  });

  it("explains a station that never completed a task", async () => {
    mockApi({ "GET /stations/STN-99": problem(404, "station-not-found", "Station not found", "no completion has been recorded for the station") });
    renderAt("/stations/STN-99");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Station not found");
    expect(alert).toHaveTextContent("no completion has been recorded for the station");
  });
});
