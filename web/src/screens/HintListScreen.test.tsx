import { MemoryRouter } from "react-router-dom";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { HintListScreen } from "./HintListScreen";
import { json, mockApi, pending, problem } from "../test/fetchMock";
import { hint } from "../test/fixtures";

function renderScreen() {
  return render(
    <MemoryRouter>
      <HintListScreen />
    </MemoryRouter>,
  );
}

describe("HintListScreen", () => {
  it("shows the loading state, then the hints with links to the hint and its station", async () => {
    mockApi({ "GET /hints": pending() });
    renderScreen();
    expect(screen.getByRole("status")).toHaveTextContent("Loading hints");
  });

  it("lists hints newest first with state pills", async () => {
    mockApi({
      "GET /hints": json({
        items: [hint(), hint({ hintId: "hint-0", state: "Consumed", consumedTaskId: "task-9c2e", stationId: "STN-08" })],
      }),
    });
    renderScreen();
    const link = await screen.findByRole("link", { name: "hint-1" });
    expect(link).toHaveAttribute("href", "/hints/hint-1");
    expect(screen.getByRole("link", { name: "STN-08" })).toHaveAttribute("href", "/stations/STN-08");
    // (the filter's <option>s carry the same words, so look at the pills)
    expect(document.querySelector('[data-status="Consumed"]')).not.toBeNull();
    expect(document.querySelector('[data-status="Active"]')).not.toBeNull();
  });

  it("says so when nothing matches", async () => {
    mockApi({ "GET /hints": json({ items: [] }) });
    renderScreen();
    expect(await screen.findByText("No hints match these filters.")).toBeInTheDocument();
  });

  it("shows the problem of a failed list", async () => {
    mockApi({ "GET /hints": problem(500, "internal-error", "Internal server error", "an unexpected error occurred") });
    renderScreen();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Internal server error");
    expect(alert).toHaveTextContent("an unexpected error occurred");
  });

  it("applies the filters as query parameters and restarts at page 1", async () => {
    const api = mockApi({ "GET /hints": json({ items: [hint()] }) });
    renderScreen();
    await screen.findByRole("link", { name: "hint-1" });
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("State"), "Expired");
    await user.type(screen.getByLabelText("Station"), "STN-07");
    await user.type(screen.getByLabelText("Site"), "SITE-1");
    await user.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(api.calls).toHaveLength(2));
    expect(Object.fromEntries(api.calls[1].query)).toEqual({ limit: "25", state: "Expired", stationId: "STN-07", siteId: "SITE-1" });
  });

  it("refuses a malformed site filter before asking", async () => {
    const api = mockApi({ "GET /hints": json({ items: [] }) });
    renderScreen();
    await screen.findByText("No hints match these filters.");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Site"), "a/b");
    expect(screen.getByRole("button", { name: "Apply filters" })).toBeDisabled();
    expect(screen.getByText(/cannot contain/)).toBeInTheDocument();
    expect(api.calls).toHaveLength(1);
  });

  it("walks forward with the cursor and back without asking the API to", async () => {
    const api = mockApi({
      "GET /hints": (call) =>
        call.query.get("cursor") === "c2"
          ? json({ items: [hint({ hintId: "hint-page2" })] })
          : json({ items: [hint({ hintId: "hint-page1" })], nextCursor: "c2" }),
    });
    renderScreen();
    await screen.findByRole("link", { name: "hint-page1" });
    const user = userEvent.setup();
    expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Next page" }));
    await screen.findByRole("link", { name: "hint-page2" });
    expect(api.calls[1].query.get("cursor")).toBe("c2");
    expect(screen.getByRole("button", { name: "Next page" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "Previous page" }));
    await screen.findByRole("link", { name: "hint-page1" });
    expect(within(screen.getByText("Page 1").closest("section") ?? document.body).queryByText("hint-page2")).toBeNull();
  });
});
