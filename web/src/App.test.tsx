import { MemoryRouter, Route, Routes } from "react-router-dom";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import App from "./App";
import { json, mockApi } from "./test/fetchMock";
import { hint, policy, station } from "./test/fixtures";

// The console mounts the remote under its own splat route; the remote's
// relative links must resolve against the mount point, not the full URL.
function renderUnderHost(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/task-interleaving/*" element={<App />} />
      </Routes>
    </MemoryRouter>,
  );
}

const ROUTES = {
  "GET /hints": json({ items: [hint()] }),
  "GET /hints/hint-1": json(hint()),
  "GET /stations/STN-07": json(station()),
  "GET /sites/SITE-1/policy": json(policy()),
};

describe("App", () => {
  it("redirects the mount point to the hint list", async () => {
    mockApi(ROUTES);
    renderUnderHost("/task-interleaving");
    expect(await screen.findByRole("heading", { name: "Interleaving hints" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Hints" })).toHaveAttribute("href", "/task-interleaving/hints");
  });

  it("keeps the sub-nav at the mount point from a deep route", async () => {
    mockApi(ROUTES);
    renderUnderHost("/task-interleaving/hints/hint-1");
    await screen.findByRole("heading", { name: "Hint hint-1" });
    expect(screen.getByRole("link", { name: "Stations" })).toHaveAttribute("href", "/task-interleaving/stations");
    expect(screen.getByRole("link", { name: "Site policy" })).toHaveAttribute("href", "/task-interleaving/policy");
  });

  it("navigates between the sections and into a hint and its station", async () => {
    mockApi(ROUTES);
    renderUnderHost("/task-interleaving/hints");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("link", { name: "hint-1" }));
    await screen.findByRole("heading", { name: "Hint hint-1" });
    await user.click(screen.getByRole("link", { name: "STN-07" }));
    await screen.findByRole("heading", { name: "Station STN-07" });
    await user.click(screen.getByRole("link", { name: "hint-1" }));
    await screen.findByRole("heading", { name: "Hint hint-1" });
    await user.click(screen.getByRole("link", { name: "Site policy" }));
    expect(await screen.findByRole("heading", { name: "Site policy" })).toBeInTheDocument();
  });
});
