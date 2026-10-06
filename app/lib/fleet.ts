import type { ApiCores, ApiForward, ApiForwardProbe, ApiJob, ApiNode, ApiServer } from "./api";
import { formatEndpoint } from "./endpoints.ts";
import { relativeTime } from "./format.ts";
import { protocolLabels } from "./nodes.ts";
import { probeState } from "./quality.ts";
import { configurationState, forwardUnit, realmForwardId, serverState, stoppedUnits, unitDetail, unitState, type StatusTone } from "./status.ts";
import type { Route } from "./routing.ts";

export type Fleet = {
  servers: ApiServer[];
  nodes: ApiNode[];
  forwards: ApiForward[];
  probes: ApiForwardProbe[];
  config: ApiJob[];
  cores: ApiCores;
};

export type FleetIndex = ReturnType<typeof buildIndex>;

function groupBy<T>(items: T[], key: (item: T) => string) {
  const groups = new Map<string, T[]>();
  for (const item of items) {
    const list = groups.get(key(item));
    if (list) list.push(item);
    else groups.set(key(item), [item]);
  }
  return groups;
}

export function buildIndex(fleet: Fleet) {
  const config = new Map<string, ApiJob>();
  for (const job of fleet.config) if (!config.has(job.server_id)) config.set(job.server_id, job);
  return {
    servers: new Map(fleet.servers.map((server) => [server.id, server])),
    nodes: new Map(fleet.nodes.map((node) => [node.id, node])),
    forwards: new Map(fleet.forwards.map((forward) => [forward.id, forward])),
    probes: new Map(fleet.probes.map((probe) => [probe.forward_id, probe])),
    config,
    nodesByServer: groupBy(fleet.nodes, (node) => node.server_id),
    forwardsByIngress: groupBy(fleet.forwards, (forward) => forward.ingress_server_id),
  };
}

export function forwardTarget(forward: ApiForward, index: FleetIndex) {
  const node = forward.target_node_id ? index.nodes.get(forward.target_node_id) : undefined;
  const server = index.servers.get(node?.server_id || forward.target_server_id || "");
  return {
    node,
    server,
    title: node?.name || server?.name || forward.target_host,
    kind: node ? protocolLabels[node.protocol] : server ? "服务器端口" : "自定义地址",
    endpoint: formatEndpoint(forward.target_host, forward.target_port),
  };
}

// The probe measures ingress-to-target reachability only; a route whose
// forwarding process is not running is down whatever the probe reports.
export function routeState(forward: ApiForward, index: FleetIndex, unavailable: boolean, now: number) {
  const probe = probeState(forward, index.probes.get(forward.id), unavailable, now);
  const engine = forward.enabled ? unitState(index.servers.get(forward.ingress_server_id), forwardUnit(forward), now) : null;
  const stopped = !!engine && !engine.running;
  return { probe, stopped, engineDetail: engine?.detail ?? "", tone: stopped ? ("bad" as StatusTone) : probe.tone };
}

export function targetsServer(forward: ApiForward, serverId: string, index: FleetIndex) {
  const node = forward.target_node_id ? index.nodes.get(forward.target_node_id) : undefined;
  return (node?.server_id || forward.target_server_id) === serverId;
}

export type Attention = { key: string; tone: StatusTone; title: string; detail: string; route: Route };

export function attentionItems(fleet: Fleet, index: FleetIndex, unavailable: Partial<Record<keyof Fleet, unknown>>, now: number) {
  const items: Attention[] = [];
  if (!unavailable.servers) {
    for (const server of fleet.servers) {
      const state = serverState(server, now);
      const route: Route = { page: "server", id: server.id };
      if (state.label === "离线") items.push({ key: `agent:${server.id}`, tone: "bad", title: server.name, detail: `Agent 离线 · 最近心跳 ${relativeTime(server.last_seen_at, now)}`, route });
      if (state.label === "待安装") items.push({ key: `agent:${server.id}`, tone: "neutral", title: server.name, detail: "尚未安装 Agent", route });
      for (const unit of stoppedUnits(server, now)) {
        const forwardId = realmForwardId(unit.name);
        const name = forwardId ? `转发「${index.forwards.get(forwardId)?.name ?? forwardId}」` : "sing-box ";
        items.push({ key: `unit:${server.id}:${unit.name}`, tone: "bad", title: server.name, detail: `${name}未运行 · ${unitDetail(unit)}`, route });
      }
      if (unavailable.config) continue;
      const config = configurationState(index.config.get(server.id), false, now);
      if (config.tone === "bad" || config.label === "结果未确认") items.push({ key: `config:${server.id}`, tone: config.tone, title: server.name, detail: `配置${config.label}`, route });
    }
  }
  if (!unavailable.forwards && !unavailable.probes) {
    for (const forward of fleet.forwards) {
      const state = probeState(forward, index.probes.get(forward.id), false, now);
      if (state.tone === "bad") items.push({ key: `probe:${forward.id}`, tone: "bad", title: forward.name, detail: ["目标不可达", state.detail].filter(Boolean).join(" · "), route: { page: "forward", id: forward.id } });
    }
  }
  return items;
}

// GeoIP stores regions as "JP · 东京"; operators may also type "JP Tokyo".
export function regionParts(region: string | undefined) {
  const [first = "", ...rest] = (region || "").trim().split(/[\s·]+/).filter(Boolean);
  if (/^[A-Za-z]{2,3}$/.test(first)) return { code: first.toUpperCase(), place: rest.join(" ") };
  return { code: "", place: [first, ...rest].join(" ") };
}

export function fleetSummary(fleet: Fleet, index: FleetIndex, now: number) {
  const states = fleet.servers.map((server) => serverState(server, now).label);
  const routes = fleet.forwards.map((forward) => routeState(forward, index, false, now));
  return {
    servers: fleet.servers.length,
    online: states.filter((state) => state === "在线").length,
    measured: routes.filter((route) => route.tone !== "neutral").length,
    healthy: routes.filter((route) => route.tone === "good").length,
    nodes: fleet.nodes.length,
  };
}

const severity = { bad: 0, warn: 1, good: 2, neutral: 3 } as const;

// Problems first, then healthy routes, then unmeasured and disabled ones.
export function sortRoutes(forwards: ApiForward[], index: FleetIndex, now: number) {
  const rank = (forward: ApiForward) => {
    const state = routeState(forward, index, false, now);
    return severity[state.tone] * 3 + (forward.enabled ? 0 : 2) + (forward.networks.includes("tcp") ? 0 : 1);
  };
  return [...forwards].sort((left, right) => rank(left) - rank(right));
}
