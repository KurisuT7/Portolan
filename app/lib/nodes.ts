import type { ApiNode, ApiNodeSettings, ApiServer } from "./api";

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

// The node form starts from the settings of the node being edited, or from
// the defaults for a new node.
export function nodeFormValues(settings?: ApiNodeSettings) {
  const { reality, shadowsocks, snell, shadow_tls: shadowTLS } = settings ?? {};
  return {
    protocol: settings?.protocol ?? "vless-reality",
    name: settings?.name ?? "",
    port: settings ? String(settings.listen_port) : "",
    handshake: reality?.handshake_server ?? "aws.amazon.com",
    handshakePort: String(reality?.handshake_port ?? 443),
    transport: reality?.transport || "tcp",
    flow: reality ? reality.flow : "xtls-rprx-vision",
    serviceName: reality?.transport_settings?.service_name ?? "",
    method: shadowsocks?.method ?? "2022-blake3-aes-256-gcm",
    snellVersion: snell?.version ?? 6,
    snellMode: !snell ? "default" : snell.version === 5 ? snell.obfs_mode ?? "" : snell.mode || "default",
    shadowTLS: !!shadowTLS,
    shadowTLSServer: shadowTLS?.handshake_server ?? "aws.amazon.com",
    shadowTLSPort: String(shadowTLS?.handshake_port ?? 443),
    allowUnsafe: !!(shadowsocks?.allow_insecure || snell?.allow_insecure),
  };
}

// port is the one clients dial, which differs from listen_port behind NAT.
export function formatNodeOption(node: ApiNode, port = node.listen_port) {
  const management = node.managed ? "托管" : "外部";
  return [
    management,
    node.name,
    protocolLabels[node.protocol],
    `:${port}`,
  ].join(" · ");
}

export const protocolShort: Record<ApiNode["protocol"], string> = {
  "vless-reality": "Reality",
  shadowsocks: "SS",
  snell: "Snell",
};
