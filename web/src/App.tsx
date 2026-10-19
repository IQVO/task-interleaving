import { Navigate, NavLink, Outlet, Route, Routes } from "react-router-dom";
import { HintDetailRoute } from "./screens/HintDetailScreen";
import { HintListScreen } from "./screens/HintListScreen";
import { PolicyLookupScreen, PolicyRoute } from "./screens/PolicyScreen";
import { StationLookupScreen, StationRoute } from "./screens/StationScreen";

const SUB_NAV = [
  { to: "hints", label: "Hints" },
  { to: "stations", label: "Stations" },
  { to: "policy", label: "Site policy" },
];

const linkStyle = ({ isActive }: { isActive: boolean }) => ({
  display: "inline-flex",
  padding: "6px 12px",
  borderRadius: "var(--wh-radius-pill)",
  fontSize: "var(--wh-font-size-sm)",
  fontWeight: isActive ? 600 : 500,
  color: isActive ? "var(--wh-color-text)" : "var(--wh-color-text-muted)",
  background: isActive ? "var(--wh-color-accent-muted)" : "transparent",
  textDecoration: "none",
});

/** The sub-nav, rendered by a layout route with the path "/".
 *
 *  The console mounts this component inside its own `<Route
 *  path="/task-interleaving/*">`. A relative `<NavLink>` rendered straight in
 *  that splat route resolves against the FULL current URL (react-router 7 uses
 *  the last path-contributing match's pathname, splat included), so from
 *  /task-interleaving/stations the link `hints` would point at
 *  /task-interleaving/stations/hints. A pathless layout route does not help
 *  (react-router drops pathless routes from relative resolution); a layout
 *  route WITH path "/" does: its own match is the mount point, under any
 *  prefix and standalone at `/`. (Same fix as the slotting and
 *  inbound-receiving remotes; App.test.tsx mounts under the host's splat route
 *  to prove it.) */
function InterleavingLayout() {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--wh-space-5)" }}>
      <nav aria-label="Task interleaving sections" style={{ display: "flex", gap: "var(--wh-space-2)" }}>
        {SUB_NAV.map((item) => (
          <NavLink key={item.to} to={item.to} style={linkStyle}>
            {item.label}
          </NavLink>
        ))}
      </nav>
      <Outlet />
    </div>
  );
}

/** Exposed as task_interleaving_mfe/App via Module Federation. Takes NO props:
 *  the console mounts it under `/task-interleaving/*` and provides the
 *  BrowserRouter, the design tokens and `window.__WAREHOUSE_CONFIG__`. Routes
 *  here are RELATIVE, so the component works identically under that prefix (in
 *  the shell) or at / (standalone dev, see main.tsx). It calls ONLY this
 *  context's own API (/api/task-interleaving).
 *
 *   - (index): redirects to hints.
 *   - hints: the hint history with station/site/state filters and paging.
 *   - hints/:hintId: one hint -- its ranked candidates and how it ended.
 *   - stations: a lookup; stations/:stationId: position and active hint.
 *   - policy: a lookup; policy/:siteId: view the site policy and edit it
 *     (the edit carries If-Match; a first declaration carries none). */
export default function App() {
  return (
    <Routes>
      <Route path="/" element={<InterleavingLayout />}>
        <Route index element={<Navigate to="hints" replace />} />
        <Route path="hints" element={<HintListScreen />} />
        <Route path="hints/:hintId" element={<HintDetailRoute />} />
        <Route path="stations" element={<StationLookupScreen />} />
        <Route path="stations/:stationId" element={<StationRoute />} />
        <Route path="policy" element={<PolicyLookupScreen />} />
        <Route path="policy/:siteId" element={<PolicyRoute />} />
      </Route>
    </Routes>
  );
}
