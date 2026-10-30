#!/usr/bin/env python3
"""Assert this chart's Kong configuration contract.

Fleet decision (2026-10-13): ALL Kong configuration — routes AND plugins —
belongs to the application's Helm chart, not warehouse-infra Terraform. The
old Terraform-side null_resource + kubectl objects silently vanished on a
cluster rebuild (local-exec only runs when triggers change) while
chart-rendered objects are Helm/ArgoCD-reconciled forever.

This test is the sensor for that decision. It fails if:
  * any enabled HTTPRoute renders WITHOUT the konghq.com/plugins annotation,
  * the annotation names KongPlugins the chart does not actually render,
  * any auth/authorization plugin (key-auth, jwt, oidc, acl, basic-auth,
    oauth2, ...) is ever configured — forbidden fleet-wide,
  * the route does not strip its /api/task-interleaving prefix before
    the request reaches the service (the service serves /recommendations, ... at
    the root),
  * the CORS plugin allows "*" or does not allow the Idempotency-Key header
    the POST commands send or the If-Match header the policy PUT sends,
  * the MCP route (when enabled) does not match /api/mcp/task-interleaving,
    does not rewrite to /mcp, or lacks the plugin annotation,
  * the A2A route (when enabled) does not match /api/a2a/task-interleaving,
    does not rewrite to / (so the Agent Card at /.well-known/agent-card.json
    stays reachable under the prefix), or lacks the plugin annotation,
  * defaults (no --set flags beyond the mandatory runtime values) render any
    Kong object at all — standalone installs without Kong must be unaffected.

The API route and the optional MCP (/api/mcp/task-interleaving -> /mcp) and A2A
(/api/a2a/task-interleaving -> /) routes are the only Kong routes: reports and the web remote are never routed here.

Run: python3 charts/task-interleaving/tests/test_kong_config.py
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART = Path(__file__).resolve().parents[1]
RELEASE = "task-interleaving"

FORBIDDEN_PLUGINS = {
    "basic-auth", "key-auth", "jwt", "oauth2", "oidc", "acl",
    "hmac-auth", "ldap-auth", "mtls", "session", "request-termination",
}

# The chart refuses to render without a database DSN and Kafka brokers (the
# real runtime, ADR 0006): supply dummies for template assertions.
MANDATORY = [
    "--set", "database.url=postgres://user:***@localhost:5432/db",
    "--set", "kafka.brokers=kafka.example.invalid:9092",
]

ENABLE_KONG = MANDATORY + [
    "--set", "gatewayApi.enabled=true",
    "--set", "gatewayApi.parentRefs[0].name=warehouse-gateway",
    "--set", "gatewayApi.parentRefs[0].namespace=kong",
    "--set", "gatewayApi.hosts[0].path=/api/task-interleaving",
    "--set", "kongPlugins.cors.enabled=true",
    "--set", "kongPlugins.prometheus.enabled=true",
    "--set", "kongPlugins.opentelemetry.enabled=true",
    "--set", "mcp.enabled=true",
    "--set", "mcp.route.enabled=true",
    "--set", "mcp.route.parentRefs[0].name=warehouse-gateway",
    "--set", "mcp.route.parentRefs[0].namespace=kong",
    "--set", "a2a.enabled=true",
    "--set", "a2a.route.enabled=true",
    "--set", "a2a.route.parentRefs[0].name=warehouse-gateway",
    "--set", "a2a.route.parentRefs[0].namespace=kong",
]

API_PREFIX, API_TARGET = "/api/task-interleaving", "/"
MCP_PREFIX, MCP_TARGET = "/api/mcp/task-interleaving", "/mcp"
A2A_PREFIX, A2A_TARGET = "/api/a2a/task-interleaving", "/"

# route name suffix -> (expected match prefix, expected rewrite target)
ROUTE_CONTRACT = {
    "-mcp": (MCP_PREFIX, MCP_TARGET),
    "-a2a": (A2A_PREFIX, A2A_TARGET),
}


def render(extra_args: list[str]) -> list[dict]:
    out = subprocess.run(
        ["helm", "template", RELEASE, str(CHART), *extra_args],
        capture_output=True, text=True, check=True,
    ).stdout
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available; cannot assert Kong config", file=sys.stderr)
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(out) if d]


def check_routes(routes: list[dict], plugins: dict[str, dict], failures: list[str]) -> None:
    if not routes:
        failures.append("enabled render contains no HTTPRoute at all")
    if not plugins:
        failures.append("enabled render contains no KongPlugin at all")
    for r in routes:
        name = r["metadata"]["name"]
        ann = (r.get("metadata", {}).get("annotations") or {}).get("konghq.com/plugins")
        if not ann:
            failures.append(f"HTTPRoute {name} has no konghq.com/plugins annotation")
        else:
            for plugin in ann.split(","):
                if plugin not in plugins:
                    failures.append(f"HTTPRoute {name} references plugin '{plugin}' that the chart does not render")
        prefix, target = next((c for suffix, c in ROUTE_CONTRACT.items() if name.endswith(suffix)), (API_PREFIX, API_TARGET))
        for rule in r.get("spec", {}).get("rules", []):
            check_rule(name, rule, prefix, target, failures)
    if sorted(r["metadata"]["name"] for r in routes) != [RELEASE, f"{RELEASE}-a2a", f"{RELEASE}-mcp"]:
        failures.append("expected exactly the api, the a2a and the mcp HTTPRoute")


def check_rule(name: str, rule: dict, want_prefix: str, want_target: str, failures: list[str]) -> None:
    prefix = rule["matches"][0]["path"]["value"]
    if prefix != want_prefix:
        failures.append(f"HTTPRoute {name} matches {prefix!r}, expected {want_prefix!r}")
    rewrites = [f for f in rule.get("filters", []) if f.get("type") == "URLRewrite"]
    if len(rewrites) != 1:
        failures.append(f"HTTPRoute {name} must strip its prefix with exactly one URLRewrite filter")
        return
    target = rewrites[0]["urlRewrite"]["path"].get("replacePrefixMatch")
    if target != want_target:
        failures.append(f"HTTPRoute {name} rewrites to {target!r}, expected {want_target!r}")


def check_plugins(plugins: dict[str, dict], failures: list[str]) -> None:
    for name, p in plugins.items():
        plugin_type = p.get("plugin", "")
        if plugin_type in FORBIDDEN_PLUGINS:
            failures.append(
                f"KongPlugin {name} is '{plugin_type}' — authentication/authorization "
                "plugins are forbidden at the gateway by fleet-wide decision"
            )
    cors = next((p for p in plugins.values() if p.get("plugin") == "cors"), None)
    if cors is None:
        failures.append("the cors plugin was enabled but not rendered")
        return
    cfg = cors["config"]
    if "*" in cfg.get("origins", []):
        failures.append("the cors plugin allows origin '*': endpoints are unauthenticated, use the exact origin")
    if "Idempotency-Key" not in cfg.get("headers", []):
        failures.append("the cors plugin must allow the Idempotency-Key request header the commands send")
    if "If-Match" not in cfg.get("headers", []):
        failures.append("the cors plugin must allow the If-Match request header the policy edit sends")


def main() -> int:
    failures: list[str] = []

    # 1. Defaults render zero Kong objects.
    default_docs = render(MANDATORY)
    kong_docs = [d for d in default_docs if "Kong" in (d.get("kind") or "")]
    if kong_docs:
        failures.append(
            f"default values render Kong objects {sorted(d['metadata']['name'] for d in kong_docs)} "
            "-- standalone installs must be unaffected; keep plugin/route blocks default-off"
        )
    if any(d.get("kind") == "HTTPRoute" for d in default_docs):
        failures.append("default values render an HTTPRoute")

    # 2. Enabled render.
    docs = render(ENABLE_KONG)
    plugins = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "KongPlugin"}
    routes = [d for d in docs if d.get("kind") == "HTTPRoute"]
    check_routes(routes, plugins, failures)
    check_plugins(plugins, failures)

    if failures:
        print("FAIL: task-interleaving chart Kong-config contract")
        for f in failures:
            print(f"  - {f}")
        return 1
    print(f"PASS: {len(routes)} HTTPRoutes (api, mcp, a2a) annotated and stripping their prefix, {len(plugins)} KongPlugins, "
          "no auth plugin, defaults are a no-op")
    return 0


if __name__ == "__main__":
    sys.exit(main())
