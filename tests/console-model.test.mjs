import assert from "node:assert/strict";
import test from "node:test";
import { attentionItems, buildIndex, fleetSummary, forwardTarget, regionParts, routeState, sortRoutes, targetsServer } from "../app/lib/fleet.ts";
import { parseTime, relativeTime } from "../app/lib/format.ts";
import { chartScale, latencyRuns, probeState } from "../app/lib/quality.ts";
import { parseRoute, routeHref } from "../app/lib/routing.ts";
import { configurationState, forwardUnit, realmForwardId, singBoxUnit, stoppedUnits, unitState } from "../app/lib/status.ts";
import { coreSummary, coresReady, serverCoreState } from "../app/lib/cores.ts";
import { agentUpdateState, agentUpdateSummary, olderRelease } from "../app/lib/agent.ts";

const now = Date.parse("2026-10-05T12:00:00Z");
const fresh = "2026-10-05T11:59:30Z";
const noTraffic = { since: "", items: [] };

test("routes round-trip and accept legacy hashes", () => {
  for (const route of [
    { page: "overview" },
    { page: "servers" },
    { page: "server", id: "srv/1" },
    { page: "nodes", server: "srv 1" },
    { page: "forwards" },
    { page: "forward", id: "fwd-1" },
  ]) assert.deepEqual(parseRoute(routeHref(route)), route.page === "forwards" || route.page === "nodes" ? { server: undefined, ...route } : route);
  assert.deepEqual(parseRoute("#forwards?server=a"), { page: "forwards", server: "a" });
  assert.deepEqual(parseRoute("#overview"), { page: "overview" });
  assert.deepEqual(parseRoute("#servers?server=a"), { page: "servers" });
  assert.deepEqual(parseRoute("#/servers/%E0"), { page: "overview" });
  assert.deepEqual(parseRoute(""), { page: "overview" });
});

test("relative times ignore Go zero values", () => {
  assert.equal(parseTime("0001-01-01T00:00:00Z"), null);
  assert.equal(relativeTime("0001-01-01T00:00:00Z", now), "—");
  assert.equal(relativeTime(fresh, now), "刚刚");
  assert.equal(relativeTime("2026-10-05T11:45:00Z", now), "15 分钟前");
  assert.equal(relativeTime("2026-10-03T12:00:00Z", now), "2 天前");
});

test("probe state never reports stale, disabled or UDP-only rules as measured", () => {
  const forward = { enabled: true, networks: ["tcp"], updated_at: "2026-10-05T11:00:00Z" };
  const probe = { forward_id: "f", server_id: "s", checked_at: fresh, attempts: 3, successes: 3, latency_ms: 42.04, jitter_ms: 1, loss_percent: 0, status: "stable" };
  assert.deepEqual(probeState(forward, probe, false, now), { label: "42.0 ms", tone: "good", detail: "" });
  assert.equal(probeState(forward, probe, true, now).label, "数据未更新");
  assert.equal(probeState({ ...forward, enabled: false }, probe, false, now).label, "已停用");
  assert.equal(probeState({ ...forward, networks: ["udp"] }, probe, false, now).label, "仅 UDP");
  assert.equal(probeState(forward, { ...probe, checked_at: "2026-10-05T11:50:00Z" }, false, now).label, "等待采样");
  assert.equal(probeState({ ...forward, updated_at: "2026-10-05T11:59:50Z" }, probe, false, now).label, "等待采样");
  const down = probeState(forward, { ...probe, status: "down", successes: 0, last_error: "dial tcp 203.0.113.1:443: connect: connection refused" }, false, now);
  assert.deepEqual(down, { label: "不可达", tone: "bad", detail: "目标端口未监听" });
  assert.equal(probeState(forward, { ...probe, status: "degraded" }, false, now).tone, "warn");
});

test("chart scale rounds to readable steps and latency runs split at gaps", () => {
  assert.deepEqual(chartScale(20), { step: 5, maximum: 20 });
  assert.deepEqual(chartScale(57.2), { step: 20, maximum: 60 });
  assert.deepEqual(chartScale(310), { step: 100, maximum: 400 });
  const ok = { checked_at: "x", attempts: 3, successes: 3, latency: 10, jitter: 1, loss: 0, availability: 100, status: "stable" };
  const gap = { ...ok, attempts: 0, successes: 0, status: "unknown", availability: null };
  assert.deepEqual(latencyRuns([ok, ok, gap, ok, { ...ok, status: "down", successes: 0 }, ok]), [[0, 1], [3], [5]]);
});

test("fleet index resolves targets and surfaces only actionable problems", () => {
  const servers = [
    { id: "a", name: "入口", address: "203.0.113.1", agent_status: "online", last_seen_at: fresh, egress_ipv4: true, egress_ipv6: false },
    { id: "b", name: "目标", address: "203.0.113.2", agent_status: "online", last_seen_at: "2026-10-05T11:00:00Z", egress_ipv4: true, egress_ipv6: false },
    { id: "c", name: "新机", address: "", agent_status: "pending", egress_ipv4: false, egress_ipv6: false },
  ];
  const nodes = [{ id: "n", server_id: "b", name: "目标节点", protocol: "snell", listen_port: 443, enabled: true, managed: true, profile: "Snell v6" }];
  const forwards = [
    { id: "f1", ingress_server_id: "a", name: "到节点", listen_port: 1000, networks: ["tcp"], target_host: "203.0.113.2", target_port: 443, target_node_id: "n", engine: "sing-box", enabled: true },
    { id: "f2", ingress_server_id: "a", name: "到地址", listen_port: 1001, networks: ["tcp"], target_host: "2001:db8::1", target_port: 80, engine: "realm", enabled: true },
  ];
  const probes = [{ forward_id: "f2", server_id: "a", checked_at: fresh, attempts: 3, successes: 0, latency_ms: 0, jitter_ms: 0, loss_percent: 100, status: "down", last_error: "i/o timeout" }];
  const config = [
    { id: "j2", server_id: "a", type: "sync", state: "failed", result: JSON.stringify({ message: "核心配置校验失败，未切换到新配置。" }), created_at: fresh },
    { id: "j1", server_id: "a", type: "sync", state: "succeeded", created_at: "2026-10-05T10:00:00Z" },
  ];
  const fleet = { servers, nodes, forwards, probes, config, traffic: noTraffic };
  const index = buildIndex(fleet);
  assert.equal(index.config.get("a").id, "j2");
  assert.deepEqual(index.forwardsByIngress.get("a").map((item) => item.id), ["f1", "f2"]);
  assert.deepEqual(forwardTarget(forwards[0], index), { node: nodes[0], server: servers[1], title: "目标节点", kind: "Snell", endpoint: "203.0.113.2:443" });
  assert.equal(forwardTarget(forwards[1], index).endpoint, "[2001:db8::1]:80");
  assert.equal(targetsServer(forwards[0], "b", index), true);
  assert.equal(targetsServer(forwards[1], "b", index), false);

  const items = attentionItems(fleet, index, {}, now);
  assert.deepEqual(items.map((item) => [item.key, item.tone]), [["config:a", "bad"], ["agent:b", "bad"], ["agent:c", "neutral"], ["probe:f2", "bad"]]);
  assert.equal(items.find((item) => item.key === "probe:f2").detail, "目标不可达 · 连接超时");
  assert.deepEqual(attentionItems(fleet, index, { servers: "x", probes: "y" }, now), []);
});

test("regions accept GeoIP and hand-written formats", () => {
  assert.deepEqual(regionParts("JP · 东京"), { code: "JP", place: "东京" });
  assert.deepEqual(regionParts("us los angeles"), { code: "US", place: "los angeles" });
  assert.deepEqual(regionParts("香港"), { code: "", place: "香港" });
  assert.deepEqual(regionParts(undefined), { code: "", place: "" });
});

test("route ordering puts problems first and summary counts only measured routes", () => {
  const probe = (id, status) => ({ forward_id: id, server_id: "a", checked_at: fresh, attempts: 3, successes: status === "down" ? 0 : 3, latency_ms: 20, jitter_ms: 1, loss_percent: 0, status });
  const forward = (id, extra = {}) => ({ id, ingress_server_id: "a", name: id, listen_port: 1, networks: ["tcp"], target_host: "x", target_port: 1, engine: "sing-box", enabled: true, ...extra });
  const forwards = [forward("ok"), forward("off", { enabled: false }), forward("udp", { networks: ["udp"] }), forward("down"), forward("slow")];
  const fleet = { servers: [{ id: "a", name: "a", address: "", agent_status: "online", last_seen_at: fresh, egress_ipv4: true, egress_ipv6: false }], nodes: [], forwards,
    probes: [probe("ok", "stable"), probe("down", "down"), probe("slow", "degraded")], config: [], traffic: noTraffic };
  const index = buildIndex(fleet);
  assert.deepEqual(sortRoutes(forwards, index, now).map((item) => item.id), ["down", "slow", "ok", "udp", "off"]);
  assert.deepEqual(fleetSummary(fleet, index, now), { servers: 1, online: 1, measured: 3, healthy: 1 });
});

test("configuration state trusts the Agent's active release over the job log", () => {
  const job = { id: "j", server_id: "a", type: "sync", state: "running", created_at: fresh, started_at: "2026-10-05T11:50:00Z" };
  assert.equal(configurationState(job, false, now).label, "结果未确认");
  assert.equal(configurationState({ ...job, applied: "current" }, false, now).tone, "good");
  assert.deepEqual(
    [configurationState({ ...job, state: "succeeded", applied: "behind" }, false, now).label, configurationState({ ...job, state: "succeeded", applied: "behind" }, false, now).tone],
    ["待重新下发", "warn"],
  );
  const ahead = configurationState({ ...job, state: "succeeded", applied: "ahead" }, false, now);
  assert.equal(ahead.tone, "bad");
  assert.equal(ahead.label, "比面板记录新");
  assert.equal(configurationState({ ...job, state: "failed", applied: "behind" }, false, now).label, "应用失败");
});

test("service state is claimed only from a fresh Agent report", () => {
  const runtime = { sing_box_version: "1.14.2", realm_version: "2.9.4", reported_at: fresh, units: [
    { name: singBoxUnit, active_state: "active", sub_state: "running" },
    { name: "portolan-realm@f2.service", active_state: "activating", sub_state: "auto-restart" },
  ] };
  const server = { id: "a", name: "入口", address: "203.0.113.1", agent_status: "online", last_seen_at: fresh, egress_ipv4: true, egress_ipv6: false, runtime };
  assert.deepEqual(unitState(server, forwardUnit({ id: "f1", engine: "sing-box" }), now), { running: true, detail: "运行中" });
  assert.deepEqual(unitState(server, forwardUnit({ id: "f2", engine: "realm" }), now), { running: false, detail: "反复重启" });
  assert.equal(unitState(server, forwardUnit({ id: "f3", engine: "realm" }), now), null);
  assert.equal(unitState({ ...server, last_seen_at: "2026-10-05T11:00:00Z" }, singBoxUnit, now), null);
  assert.equal(unitState({ ...server, runtime: undefined }, singBoxUnit, now), null);
  assert.equal(realmForwardId("portolan-realm@f2.service"), "f2");

  const forwards = [{ id: "f2", ingress_server_id: "a", name: "到地址", listen_port: 1001, networks: ["tcp"], target_host: "2001:db8::1", target_port: 80, engine: "realm", enabled: true }];
  const fleet = { servers: [server], nodes: [], forwards, probes: [], config: [], traffic: noTraffic };
  const items = attentionItems(fleet, buildIndex(fleet), {}, now);
  assert.deepEqual(items.map((item) => [item.key, item.detail]), [["unit:a:portolan-realm@f2.service", "转发「到地址」未运行 · 反复重启"]]);
  assert.deepEqual(stoppedUnits({ ...server, last_seen_at: "2026-10-05T11:00:00Z" }, now), []);
});

test("a route whose forwarding process is down is not healthy", () => {
  const server = { id: "a", name: "入口", address: "203.0.113.1", agent_status: "online", last_seen_at: fresh, egress_ipv4: true, egress_ipv6: false,
    runtime: { sing_box_version: "1.14.2", realm_version: "2.9.4", reported_at: fresh, units: [{ name: "portolan-realm@down.service", active_state: "failed", sub_state: "failed" }] } };
  const forward = (id) => ({ id, ingress_server_id: "a", name: id, listen_port: 1, networks: ["tcp"], target_host: "x", target_port: 1, engine: "realm", enabled: true });
  const probe = (id) => ({ forward_id: id, server_id: "a", checked_at: fresh, attempts: 3, successes: 3, latency_ms: 20, jitter_ms: 1, loss_percent: 0, status: "stable" });
  const forwards = [forward("up"), forward("down")];
  const fleet = { servers: [server], nodes: [], forwards, probes: [probe("up"), probe("down")], config: [], traffic: noTraffic };
  const index = buildIndex(fleet);
  const down = routeState(forwards[1], index, false, now);
  assert.deepEqual([down.stopped, down.tone, down.engineDetail, down.probe.tone], [true, "bad", "启动失败", "good"]);
  assert.equal(routeState(forwards[0], index, false, now).tone, "good");
  assert.deepEqual(sortRoutes(forwards, index, now).map((item) => item.id), ["down", "up"]);
  assert.deepEqual(fleetSummary(fleet, index, now), { servers: 1, online: 1, measured: 2, healthy: 1 });
});

test("core state follows the target, update jobs and Agent reports", () => {
  const runtime = { sing_box_version: "1.14.2", realm_version: "2.9.6", reported_at: fresh, units: [] };
  const server = { id: "a", name: "入口", address: "203.0.113.1", agent_status: "online", last_seen_at: fresh, egress_ipv4: true, egress_ipv6: false, runtime };
  const unreported = { ...server, id: "b", runtime: undefined };
  const target = { core: "sing-box", version: "1.14.3", updated_at: "2026-10-05T11:00:00Z" };
  const job = (state, created_at = fresh, result = "") => ({ id: "j", server_id: "a", type: "update-sing-box", state, result, created_at });
  const cores = (jobs = [], targets = [target]) => ({ targets, jobs });

  assert.deepEqual(serverCoreState(unreported, "sing-box", cores()), { kind: "unreported" });
  assert.deepEqual(serverCoreState(server, "sing-box", cores([], [])), { kind: "untargeted", installed: "1.14.2" });
  assert.deepEqual(serverCoreState(server, "sing-box", cores()), { kind: "outdated", installed: "1.14.2", target: "1.14.3" });
  assert.deepEqual(serverCoreState(server, "sing-box", cores([job("pending")])), { kind: "updating", installed: "1.14.2", pending: true });
  const failed = serverCoreState(server, "sing-box", cores([job("failed", fresh, JSON.stringify({ message: "新核心没有正常运行，已换回原版本。" }))]));
  assert.deepEqual(failed, { kind: "failed", installed: "1.14.2", target: "1.14.3", detail: "新核心没有正常运行，已换回原版本。" });
  // A failure from before the target changed does not describe the new target.
  assert.equal(serverCoreState(server, "sing-box", cores([job("failed", "2026-10-05T10:00:00Z")])).kind, "outdated");
  assert.equal(serverCoreState(server, "realm", cores([], [{ core: "realm", version: "2.9.6", updated_at: fresh }])).kind, "current");
  assert.deepEqual(coreSummary([server, unreported, { ...server, id: "c", agent_status: "pending" }], "sing-box", cores()),
    { total: 2, current: 0, outdated: 1, updating: 0, unreported: 1 });
  assert.equal(coresReady(cores()), false);
  assert.equal(coresReady(cores([], [target, { core: "realm", version: "2.9.6", updated_at: fresh }])), true);
});

test("releases compare numerically and development builds never count as older", () => {
  assert.equal(olderRelease("v0.1.0", "v0.2.0"), true);
  assert.equal(olderRelease("v0.9.3", "v0.10.0"), true);
  assert.equal(olderRelease("v0.2.0", "v0.2.0"), false);
  // A panel-only release keeps the earlier Agent version, and Agents installed by a newer panel stay current.
  assert.equal(olderRelease("v0.2.1", "v0.2.0"), false);
  assert.equal(olderRelease("dev", "v0.2.0"), false);
  assert.equal(olderRelease("v0.2.0", "dev"), false);
  assert.equal(olderRelease(undefined, "v0.2.0"), false);
});

test("Agent update state follows the reported version and the latest update job", () => {
  const server = (id, version, extra = {}) => ({ id, agent_status: "online", runtime: version ? { agent_version: version } : undefined, ...extra });
  const job = (server_id, state, result = "") => ({ id: `job_${server_id}`, server_id, type: "update-agent", state, result, created_at: fresh });
  const servers = [
    server("current", "v0.3.0"),
    server("capable", "v0.2.0"),
    server("legacy", "v0.1.0"),
    server("updating", "v0.2.0"),
    server("failed", "v0.2.1"),
    server("dev", "dev"),
    server("unreported", ""),
    server("pending", "", { agent_status: "pending" }),
  ];
  const jobs = [job("updating", "running"), job("failed", "failed", JSON.stringify({ message: "新版本 Agent 无法连接面板，未替换。" })), { ...job("capable", "pending"), type: "update-sing-box" }];
  const states = Object.fromEntries(servers.map((item) => [item.id, agentUpdateState(item, "v0.3.0", jobs)]));
  assert.deepEqual(states.current, { kind: "current" });
  assert.deepEqual(states.capable, { kind: "outdated", target: "v0.3.0" });
  assert.deepEqual(states.legacy, { kind: "reinstall", target: "v0.3.0" });
  assert.deepEqual(states.updating, { kind: "updating", pending: false });
  assert.deepEqual(states.failed, { kind: "failed", target: "v0.3.0", detail: "新版本 Agent 无法连接面板，未替换。" });
  assert.deepEqual(states.dev, { kind: "current" });
  assert.deepEqual(states.unreported, { kind: "current" });
  assert.deepEqual(agentUpdateSummary(servers, "v0.3.0", jobs), { updatable: 2, reinstall: 1, updating: 1 });
  // A development panel has no release to offer.
  assert.deepEqual(agentUpdateState(servers[1], "dev", []), { kind: "current" });
});
