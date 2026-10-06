import type { ApiNode, ApiServer } from "./api";

export const protocolLabels: Record<ApiNode["protocol"], string> = {
  "vless-reality": "Reality",
  shadowsocks: "Shadowsocks",
  snell: "Snell",
};

export type NodeGroup = {
  serverId: string;
  serverName: string;
  nodes: ApiNode[];
};

export function groupNodesByServer(
  nodes: ApiNode[],
  servers: ApiServer[],
): NodeGroup[] {
  const groups = new Map<string, NodeGroup>(
    servers.map((server) => [
      server.id,
      { serverId: server.id, serverName: server.name, nodes: [] },
    ]),
  );
  const unknownGroups = new Map<string, NodeGroup>();
  for (const node of nodes) {
    const group =
      groups.get(node.server_id) ||
      unknownGroups.get(node.server_id) || {
        serverId: node.server_id,
        serverName: node.server_id,
        nodes: [],
      };
    group.nodes.push(node);
    if (!groups.has(node.server_id)) unknownGroups.set(node.server_id, group);
  }
  return [...groups.values(), ...unknownGroups.values()].filter(
    (group) => group.nodes.length > 0,
  );
}

export function displayNodeProfile(
  protocol: ApiNode["protocol"],
  profile: string,
) {
  let detail = profile.trim().replace(/^外部\s*·\s*/u, "");
  if (protocol === "vless-reality") {
    detail = detail.replace(/^REALITY\s*·\s*/iu, "");
    return /^reality$/iu.test(detail) ? "" : detail;
  }
  if (protocol === "snell") {
    return detail.replace(/^Snell\s*/iu, "");
  }
  return detail;
}

export function formatNodeOption(node: ApiNode) {
  const management = node.managed ? "托管" : "外部";
  return [
    management,
    node.name,
    protocolLabels[node.protocol],
    `:${node.listen_port}`,
  ].join(" · ");
}

export const protocolShort: Record<ApiNode["protocol"], string> = {
  "vless-reality": "Reality",
  shadowsocks: "SS",
  snell: "Snell",
};
