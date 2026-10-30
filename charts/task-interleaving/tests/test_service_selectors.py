#!/usr/bin/env python3
"""Assert this chart's Service selectors isolate each component, and that every
component carries exactly the environment its binary reads.

Why the selector check exists (fleet-wide): `app.kubernetes.io/name` +
`instance` are identical on every pod a release creates. A Service that
selects on those two alone selects ALL components -- verified live in other
fleet charts, where a Kong request for an api /healthz was answered by the
wrong pod. Every Deployment here carries `app.kubernetes.io/component` in
selector.matchLabels and its pod labels, and every Service selects on it, so
each Service selects EXACTLY ONE Deployment. Components: api (always), mcp
(ADR 0007, default off), a2a (ADR 0009, default off), analytics-projector and analytics-reports (ADR 0008,
default off) and frontend (the nginx pod serving the task_interleaving_mfe
remote, default off), all rendered TOGETHER here. The projector has no Service
(its admin port is probes-only); the frontend is never routed by this chart.

It also asserts the ADR 0006 runtime: the mandatory env is rendered, the
*_MODE variables never are, and the api HPA owns replicas when enabled.

Run: python3 charts/task-interleaving/tests/test_service_selectors.py
Needs: helm, PyYAML.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "task-interleaving"

# Dummy values, no password: the chart refuses to render without a database
# source and without Kafka brokers.
BASE = [
    "--set", "database.url=postgres://u@example.invalid:5432/db",
    "--set", "kafka.brokers=kafka.example.invalid:9092",
]
ENABLE_EVERYTHING = BASE + [
    "--set", "autoscaling.api.enabled=true",
    "--set", "config.hintExpiryInterval=2s",
    "--set", "config.outboxRelayInterval=500ms",
    "--set", "config.shutdownDrainDelay=5s",
    "--set", "gatewayApi.enabled=true",
    "--set", "ingress.enabled=true",
    "--set", "mcp.enabled=true",
    "--set", "a2a.enabled=true",
    "--set", "frontend.enabled=true",
    "--set", "analytics.enabled=true",
    "--set", "analytics.url=postgres://u@example.invalid:5432/analytics",
]

# Every env var cmd/api reads that has a dedicated chart value.
API_ENV = (
    "HTTP_ADDR", "LOG_LEVEL", "DATABASE_URL", "MIGRATIONS_DATABASE_URL", "KAFKA_BROKERS",
    "FULFILLMENT_CONSUMER_GROUP", "LAYOUT_CONSUMER_GROUP",
    "OUTBOX_RELAY_INTERVAL", "OUTBOX_MAX_ATTEMPTS", "SHUTDOWN_DRAIN_DELAY",
    "HINT_EXPIRY_INTERVAL",
)
# The api's OLTP/outbox env: never on the projector or reports pods.
OLTP_ENV = {"DATABASE_URL", "MIGRATIONS_DATABASE_URL", "FULFILLMENT_CONSUMER_GROUP", "LAYOUT_CONSUMER_GROUP", "OUTBOX_MAX_ATTEMPTS"}
# The MCP server is read-only: no Kafka, no outbox relay, no consumers, no workers.
MCP_STRAY = {
    "KAFKA_BROKERS", "OUTBOX_RELAY_INTERVAL", "OUTBOX_MAX_ATTEMPTS", "FULFILLMENT_CONSUMER_GROUP",
    "LAYOUT_CONSUMER_GROUP", "HINT_EXPIRY_INTERVAL", "ANALYTICS_DATABASE_URL",
}
# The A2A server is read-only too: same strays, none of them rendered.
A2A_STRAY = MCP_STRAY
# Never rendered: they default to kafka and the binary rejects permissive.
FORBIDDEN_ENV = ("FULFILLMENT_MODE", "LAYOUT_MODE")


def helm_template(extra_args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["helm", "template", RELEASE, str(CHART_DIR), *extra_args],
        capture_output=True, text=True,
    )


def render(extra_args: list[str]) -> list[dict]:
    result = helm_template(extra_args)
    if result.returncode != 0:
        raise SystemExit(f"FAIL: helm template {' '.join(extra_args)}:\n{result.stderr}")
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available; cannot assert selectors", file=sys.stderr)
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(result.stdout) if d]


def selector_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("selector") or {}


def pod_labels_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("template", {}).get("metadata", {}).get("labels") or {}


def matches(selector: dict, labels: dict) -> bool:
    return bool(selector) and all(labels.get(k) == v for k, v in selector.items())


def env_of(dep: dict) -> list[dict]:
    return dep["spec"]["template"]["spec"]["containers"][0].get("env", [])


def env_names(dep: dict) -> list[str]:
    return [e["name"] for e in env_of(dep)]


def env_value(dep: dict, name: str) -> str | None:
    return next((e.get("value") for e in env_of(dep) if e["name"] == name), None)


def command_of(dep: dict) -> list[str] | None:
    return dep["spec"]["template"]["spec"]["containers"][0].get("command")


def pins(svc: dict | None, component: str) -> bool:
    return svc is not None and selector_of(svc).get("app.kubernetes.io/component") == component


def check_mcp(deployments: dict, services: dict, failures: list[str]) -> None:
    name = f"{RELEASE}-mcp"
    if not pins(services.get(name), "mcp"):
        failures.append("the MCP Service must exist and pin component=mcp")
    if name not in deployments:
        failures.append("the MCP Deployment was not rendered with mcp.enabled=true")
        return
    if command_of(deployments[name]) != ["/app/mcp"]:
        failures.append("the MCP Deployment must run /app/mcp")
    env = set(env_names(deployments[name]))
    stray = MCP_STRAY & env
    if stray:
        failures.append(f"the MCP Deployment must not get Kafka/relay/consumer env (it is read-only and never dials Kafka): {sorted(stray)}")
    for want in ("DATABASE_URL", "MIGRATIONS_DATABASE_URL", "MCP_ADDR", "LOG_LEVEL"):
        if want not in env:
            failures.append(f"the MCP Deployment does not render {want}")


def check_a2a(deployments: dict, services: dict, failures: list[str]) -> None:
    name = f"{RELEASE}-a2a"
    if not pins(services.get(name), "a2a"):
        failures.append("the A2A Service must exist and pin component=a2a")
    if name not in deployments:
        failures.append("the A2A Deployment was not rendered with a2a.enabled=true")
        return
    if command_of(deployments[name]) != ["/app/a2a"]:
        failures.append("the A2A Deployment must run /app/a2a")
    env = set(env_names(deployments[name]))
    stray = A2A_STRAY & env
    if stray:
        failures.append(f"the A2A Deployment must not get Kafka/relay/consumer env (it is read-only and never dials Kafka): {sorted(stray)}")
    for want in ("DATABASE_URL", "MIGRATIONS_DATABASE_URL", "A2A_ADDR", "A2A_AGENT_URL", "LOG_LEVEL"):
        if want not in env:
            failures.append(f"the A2A Deployment does not render {want}")
    agent_url = env_value(deployments[name], "A2A_AGENT_URL") or ""
    if not agent_url.startswith("http://") or not agent_url.endswith(":8093/"):
        failures.append(f"A2A_AGENT_URL must be the absolute in-cluster Service URL on :8093, got {agent_url!r}")
    if services[name]["spec"]["ports"][0]["port"] != 8093:
        failures.append("the A2A Service must listen on 8093")


def check_analytics(deployments: dict, services: dict, failures: list[str]) -> None:
    projector, reports = f"{RELEASE}-analytics-projector", f"{RELEASE}-analytics-reports"
    if projector not in deployments:
        failures.append("the projector Deployment was not rendered with analytics.enabled=true")
    else:
        if command_of(deployments[projector]) != ["/app/ti-projector"]:
            failures.append("the projector Deployment must run /app/ti-projector")
        env = set(env_names(deployments[projector]))
        # It never opens the OLTP database and runs none of the api's consumers.
        stray = (OLTP_ENV | {"HINT_EXPIRY_INTERVAL", "OUTBOX_RELAY_INTERVAL"}) & env
        if stray:
            failures.append(f"the projector Deployment must not get the api's OLTP env: {sorted(stray)}")
        for want in ("ANALYTICS_DATABASE_URL", "KAFKA_BROKERS", "ANALYTICS_CONSUMER_GROUP", "ADMIN_ADDR", "LOG_LEVEL"):
            if want not in env:
                failures.append(f"the projector Deployment does not render {want}")
        if env_value(deployments[projector], "ANALYTICS_CONSUMER_GROUP") != "task-interleaving-analytics":
            failures.append("the projector's consumer group must be the stable task-interleaving-analytics")
    if reports not in deployments:
        failures.append("the reports Deployment was not rendered with analytics.enabled=true")
    else:
        if command_of(deployments[reports]) != ["/app/ti-reports"]:
            failures.append("the reports Deployment must run /app/ti-reports")
        env = set(env_names(deployments[reports]))
        stray = (OLTP_ENV | {"KAFKA_BROKERS"}) & env
        if stray:
            failures.append(f"the reports Deployment must not get Kafka/OLTP env (it reads the analytical database only): {sorted(stray)}")
        for want in ("ANALYTICS_DATABASE_URL", "HTTP_ADDR", "LOG_LEVEL"):
            if want not in env:
                failures.append(f"the reports Deployment does not render {want}")
    if not pins(services.get(reports), "analytics-reports"):
        failures.append("the reports Service must exist and pin component=analytics-reports")
    if projector in services:
        failures.append("the projector must not render a Service (its admin port is probes-only)")


def check_frontend(docs: list[dict], deployments: dict, services: dict, failures: list[str]) -> None:
    name = f"{RELEASE}-frontend"
    if not pins(services.get(name), "frontend"):
        failures.append("the frontend Service must exist and pin component=frontend")
    elif services[name]["spec"].get("type") != "ClusterIP":
        failures.append("the frontend Service must be ClusterIP")
    if name not in deployments:
        failures.append("the frontend Deployment was not rendered with frontend.enabled=true")
    elif {"DATABASE_URL", "KAFKA_BROKERS"} & set(env_names(deployments[name])):
        failures.append("the frontend Deployment must not get database/Kafka env (it serves static bytes)")
    for d in docs:
        if d.get("kind") in {"Ingress", "HTTPRoute"} and "frontend" in d["metadata"]["name"]:
            failures.append(f"{d['kind']} {d['metadata']['name']}: frontend routing must not live in this chart")


def check_selectors(docs: list[dict], failures: list[str]) -> None:
    services = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Service"}
    deployments = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Deployment"}

    want = sorted([RELEASE] + [f"{RELEASE}-{c}" for c in ("mcp", "a2a", "analytics-projector", "analytics-reports", "frontend")])
    if sorted(deployments) != want:
        failures.append(f"expected the Deployments {want}, got {sorted(deployments)}")
    want_svc = sorted([RELEASE] + [f"{RELEASE}-{c}" for c in ("mcp", "a2a", "analytics-reports", "frontend")])
    if sorted(services) != want_svc:
        failures.append(f"expected the Services {want_svc}, got {sorted(services)}")
    if not pins(services.get(RELEASE), "api"):
        failures.append("the api Service selector must pin component=api")

    check_mcp(deployments, services, failures)
    check_a2a(deployments, services, failures)
    check_analytics(deployments, services, failures)
    check_frontend(docs, deployments, services, failures)

    # Every Deployment's own selector must pin a component too, and be
    # satisfied by its pod labels.
    for name, dep in deployments.items():
        match_labels = dep["spec"]["selector"].get("matchLabels") or {}
        if "app.kubernetes.io/component" not in match_labels:
            failures.append(f"Deployment {name} selector.matchLabels lacks app.kubernetes.io/component")
        if not matches(match_labels, pod_labels_of(dep)):
            failures.append(f"Deployment {name} pod labels do not satisfy its own selector")

    # The real invariant: each Service selects exactly one Deployment.
    for svc_name, svc in services.items():
        sel = selector_of(svc)
        hit = [d for d, dep in deployments.items() if matches(sel, pod_labels_of(dep))]
        if len(hit) != 1:
            failures.append(f"Service {svc_name} selects {len(hit)} Deployments {sorted(hit)}; expected exactly 1")

    # The Service's named target port exists on every container it targets.
    for name, dep in deployments.items():
        ports = {p["name"] for p in dep["spec"]["template"]["spec"]["containers"][0].get("ports", [])}
        if "http" not in ports:
            failures.append(f"Deployment {name} does not expose the named port 'http'")

    api = deployments.get(RELEASE)
    if api is None:
        failures.append("the api Deployment was not rendered")
        return
    # The api HPA owns replicas when enabled.
    hpa = next((d for d in docs if d.get("kind") == "HorizontalPodAutoscaler"), None)
    if hpa is None or hpa["spec"]["scaleTargetRef"]["name"] != RELEASE:
        failures.append("the api HPA was not rendered (or does not scale the api Deployment)")
    elif "replicas" in api.get("spec", {}):
        failures.append("the api Deployment must omit replicas when its HPA owns them")

    check_env(api, failures)


def check_env(api: dict, failures: list[str]) -> None:
    api_env = env_names(api)
    for want in API_ENV:
        if want not in api_env:
            failures.append(f"the api Deployment does not render {want} from its dedicated value")
    for banned in FORBIDDEN_ENV:
        if banned in api_env:
            failures.append(f"the api Deployment renders {banned}: the *_MODE variables are never set (ADR 0006)")
    duplicates = sorted({n for n in api_env if api_env.count(n) > 1})
    if duplicates:
        failures.append(f"the api Deployment renders env vars twice: {duplicates}")
    for name, want in (
        ("KAFKA_BROKERS", "kafka.example.invalid:9092"),
        ("HINT_EXPIRY_INTERVAL", "2s"),
        ("OUTBOX_RELAY_INTERVAL", "500ms"),
        ("SHUTDOWN_DRAIN_DELAY", "5s"),
    ):
        if env_value(api, name) != want:
            failures.append(f"the api Deployment renders {name}={env_value(api, name)!r}, want {want!r}")


def check_defaults(failures: list[str]) -> None:
    """Default values deploy the api only: no mcp, a2a, analytics, frontend, HPA, route or ingress."""
    defaults = render(BASE)
    names = sorted(d["metadata"]["name"] for d in defaults if d.get("kind") in {"Deployment", "Service", "Secret"})
    optional = [n for n in names if n.endswith(("-mcp", "-a2a", "-frontend", "-analytics-projector", "-analytics-reports", "-analytics"))]
    if optional:
        failures.append(f"optional components rendered with default values: {optional}")
    kinds = sorted(d["kind"] for d in defaults)
    stray = [k for k in kinds if k in {"HorizontalPodAutoscaler", "Ingress", "HTTPRoute", "KongPlugin"}]
    if stray:
        failures.append(f"optional components rendered with default values: {stray}")
    dep = next(d for d in defaults if d.get("kind") == "Deployment")
    names = env_names(dep)
    for banned in FORBIDDEN_ENV:
        if banned in names:
            failures.append(f"{banned} must not be rendered with default values")
    # Optional values stay absent so the binary's own defaults apply.
    off = {"OUTBOX_RELAY_INTERVAL", "SHUTDOWN_DRAIN_DELAY", "HINT_EXPIRY_INTERVAL"}
    if off & set(names):
        failures.append(f"these must be off with default values: {sorted(off & set(names))}")


def check_refusals(failures: list[str]) -> None:
    for label, args, needle in (
        ("without a database source", ["--set", "kafka.brokers=k:9092"], "database.url or database.existingSecret"),
        ("without Kafka brokers", ["--set", "database.url=postgres://u@h/db"], "kafka.brokers"),
        ("an empty fulfillment group", BASE + ["--set", "config.fulfillmentConsumerGroup="], "config.fulfillmentConsumerGroup"),
        ("an empty layout group", BASE + ["--set", "config.layoutConsumerGroup="], "config.layoutConsumerGroup"),
        ("a *_MODE variable through extraEnv",
         BASE + ["--set", "extraEnv[0].name=FULFILLMENT_MODE", "--set", "extraEnv[0].value=permissive"], "FULFILLMENT_MODE"),
        ("analytics without a DSN source", BASE + ["--set", "analytics.enabled=true"], "neither analytics.url nor analytics.existingSecret"),
        ("analytics without a consumer group",
         BASE + ["--set", "analytics.enabled=true", "--set", "analytics.url=postgres://u@h/a", "--set", "analytics.consumerGroup="],
         "analytics.consumerGroup"),
        ("a grace period under the shutdown worst case", BASE + ["--set", "terminationGracePeriodSeconds=54"], "terminationGracePeriodSeconds"),
    ):
        result = helm_template(args)
        if result.returncode == 0 or needle not in result.stderr:
            failures.append(f"chart rendered (or failed for another reason) {label}")


def main() -> int:
    failures: list[str] = []

    check_selectors(render(ENABLE_EVERYTHING), failures)
    check_defaults(failures)
    check_refusals(failures)

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1

    print("PASS: every Service selects exactly one Deployment (api, mcp, a2a, analytics-reports, frontend; the projector has none); "
          "mcp, a2a, analytics, frontend, HPA and routes are off by default; "
          "every cmd/api env var has a dedicated value and no *_MODE is rendered; the chart refuses to "
          "render without a database source, without brokers, with an empty consumer group, with a "
          "*_MODE injected through extraEnv or with analytics on but no DSN source")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
