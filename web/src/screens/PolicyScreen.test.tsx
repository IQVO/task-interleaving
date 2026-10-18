import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { PolicyLookupScreen, PolicyRoute } from "./PolicyScreen";
import { json, mockApi, problem } from "../test/fetchMock";
import { policy } from "../test/fixtures";

function Where() {
  return <div data-testid="where">{useLocation().pathname}</div>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/policy" element={<PolicyLookupScreen />} />
        <Route path="/policy/:siteId" element={<PolicyRoute />} />
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

const NOT_DECLARED = problem(404, "policy-not-found", "Interleaving policy not found", "the site has not declared an interleaving policy");

describe("PolicyLookupScreen", () => {
  it("opens the site's policy", async () => {
    renderAt("/policy");
    const user = userEvent.setup();
    expect(screen.getByRole("button", { name: "Open policy" })).toBeDisabled();
    await user.type(screen.getByLabelText("Site"), "SITE-1");
    await user.click(screen.getByRole("button", { name: "Open policy" }));
    expect(screen.getByTestId("where")).toHaveTextContent("/policy/SITE-1");
  });
});

describe("PolicyScreen: view", () => {
  it("shows the current policy and prefills the edit form", async () => {
    mockApi({ "GET /sites/SITE-1/policy": json(policy()) });
    renderAt("/policy/SITE-1");
    expect(await screen.findByText("PICK, REPLENISH")).toBeInTheDocument();
    expect(screen.getByText("25 m")).toBeInTheDocument();
    expect(screen.getByText("90 s")).toBeInTheDocument();
    expect(screen.getByLabelText(/Max detour/)).toHaveValue("25");
    expect(screen.getByLabelText("PICK")).toBeChecked();
    expect(screen.getByLabelText("PACK")).not.toBeChecked();
    expect(screen.getByText("Change the policy (editing version 4)")).toBeInTheDocument();
  });

  it("explains an inert site and offers the first declaration", async () => {
    mockApi({ "GET /sites/SITE-9/policy": NOT_DECLARED });
    renderAt("/policy/SITE-9");
    expect(await screen.findByText(/This site is inert/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Declare policy" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows any other failure as a problem", async () => {
    mockApi({ "GET /sites/SITE-1/policy": problem(500, "internal-error", "Internal server error", "an unexpected error occurred") });
    renderAt("/policy/SITE-1");
    expect(await screen.findByRole("alert")).toHaveTextContent("Internal server error");
  });
});

describe("PolicyScreen: edit", () => {
  it("saves with If-Match = the version it was loaded at, then shows the new version", async () => {
    let current = policy();
    const api = mockApi({
      "GET /sites/SITE-1/policy": () => json(current),
      "PUT /sites/SITE-1/policy": () => {
        current = policy({ maxDetourM: 30, version: 5 });
        return json(current);
      },
    });
    renderAt("/policy/SITE-1");
    const detour = await screen.findByLabelText(/Max detour/);
    const user = userEvent.setup();
    await user.clear(detour);
    await user.type(detour, "30");
    await user.click(screen.getByLabelText("PACK"));
    await user.click(screen.getByRole("button", { name: "Save new version" }));

    await screen.findByText("Policy saved as version 5.");
    const put = api.to("PUT /sites/SITE-1/policy")[0];
    expect(put.headers).toEqual({ "Content-Type": "application/json", "If-Match": '"4"' });
    expect(put.body).toEqual({
      maxDetourM: 30, maxCandidates: 3, hintTtlSeconds: 90, verticalWeight: 1.5, eligibleTaskTypes: ["PICK", "PACK", "REPLENISH"],
    });
    expect(await screen.findByText("30 m")).toBeInTheDocument();
    expect(screen.getByText("Change the policy (editing version 5)")).toBeInTheDocument();
  });

  it("declares a first policy without If-Match", async () => {
    let declared = false;
    const api = mockApi({
      "GET /sites/SITE-9/policy": () => (declared ? json(policy({ siteId: "SITE-9", version: 1 })) : NOT_DECLARED.clone()),
      "PUT /sites/SITE-9/policy": () => {
        declared = true;
        return json(policy({ siteId: "SITE-9", version: 1 }));
      },
    });
    renderAt("/policy/SITE-9");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/Max detour/), "25");
    await user.click(screen.getByLabelText("PICK"));
    await user.click(screen.getByRole("button", { name: "Declare policy" }));
    await screen.findByText("Policy saved as version 1.");
    expect(api.to("PUT /sites/SITE-9/policy")[0].headers).toEqual({ "Content-Type": "application/json" });
  });

  it("validates before asking and names every problem", async () => {
    const api = mockApi({ "GET /sites/SITE-1/policy": json(policy()) });
    renderAt("/policy/SITE-1");
    const user = userEvent.setup();
    const detour = await screen.findByLabelText(/Max detour/);
    await user.clear(detour);
    await user.click(screen.getByLabelText("PICK"));
    await user.click(screen.getByLabelText("REPLENISH"));
    await user.click(screen.getByRole("button", { name: "Save new version" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Max detour must be a number greater than 0.");
    expect(alert).toHaveTextContent("Choose at least one eligible task type.");
    expect(api.to("PUT /sites/SITE-1/policy")).toHaveLength(0);
  });

  it("tells the operator a stale edit lost (412) and reloads the current policy on request", async () => {
    let current = policy();
    mockApi({
      "GET /sites/SITE-1/policy": () => json(current),
      "PUT /sites/SITE-1/policy": () => {
        current = policy({ maxDetourM: 99, version: 6 });
        return problem(412, "version-mismatch", "The If-Match version is stale", "the policy is at version 6");
      },
    });
    renderAt("/policy/SITE-1");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Save new version" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Someone changed this policy since you opened it.");
    expect(alert).toHaveTextContent("The If-Match version is stale");
    await user.click(screen.getByRole("button", { name: "Reload the current policy" }));
    await waitFor(() => expect(screen.getByText("99 m")).toBeInTheDocument());
    expect(screen.getByText("Change the policy (editing version 6)")).toBeInTheDocument();
    expect(screen.queryByText("Someone changed this policy since you opened it.")).toBeNull();
  });

  it("shows a rejected value from the server as a problem", async () => {
    mockApi({
      "GET /sites/SITE-1/policy": json(policy()),
      "PUT /sites/SITE-1/policy": problem(400, "invalid-policy", "Policy is invalid", "maxCandidates must be between 1 and 5"),
    });
    renderAt("/policy/SITE-1");
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Save new version" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Policy is invalid");
    expect(alert).toHaveTextContent("maxCandidates must be between 1 and 5");
    expect(screen.queryByRole("button", { name: "Reload the current policy" })).toBeNull();
  });
});
