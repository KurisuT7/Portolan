import type { ApiNode, ApiNodeSettings, ApiServer, ClientExport } from "./api";
import { byteLength } from "./format.ts";

export const nodeProtocols: ApiNode["protocol"][] = ["vless-reality", "shadowsocks", "snell"];

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

export type NodeDraft = ReturnType<typeof nodeFormValues> & { key: string };

export function createNodeDraft(
  key: string,
  protocol: ApiNode["protocol"] = "vless-reality",
  settings?: ApiNodeSettings,
): NodeDraft {
  return {
    ...nodeFormValues(settings),
    protocol: settings?.protocol ?? protocol,
    key,
  };
}

// Suggests the first protocol not yet present in the current batch.
export function nextDraftProtocol(
  drafts: ReadonlyArray<Pick<NodeDraft, "protocol">>,
): ApiNode["protocol"] {
  return nodeProtocols.find((protocol) => !drafts.some((draft) => draft.protocol === protocol)) ?? "vless-reality";
}

// Generates the resolved node name for a draft, appending the protocol when
// creating several nodes and numbering repeated protocols so placeholders and
// submitted names match.
export function defaultNodeName(
  draft: Pick<NodeDraft, "protocol" | "name">,
  drafts: ReadonlyArray<Pick<NodeDraft, "protocol">>,
  index: number,
  serverName?: string,
) {
  const custom = draft.name.trim();
  if (custom) return custom;
  const base = serverName?.trim() || "";
  if (drafts.length <= 1) return base || protocolLabels[draft.protocol];
  const prefix = base ? `${base} ${protocolLabels[draft.protocol]}` : protocolLabels[draft.protocol];
  const sameCount = drafts.filter((item) => item.protocol === draft.protocol).length;
  if (sameCount <= 1) return prefix;
  const ordinal = drafts.slice(0, index + 1).filter((item) => item.protocol === draft.protocol).length;
  return `${prefix} ${ordinal}`;
}

export function draftNeedsUnsafeConfirm(
  draft: Pick<NodeDraft, "protocol" | "method" | "snellVersion" | "snellMode">,
) {
  return (
    (draft.protocol === "shadowsocks" && draft.method === "none") ||
    (draft.protocol === "snell" && draft.snellVersion === 6 && draft.snellMode === "unsafe-raw")
  );
}

export function buildNodeRequest(
  draft: NodeDraft,
  finalName: string,
  initial?: ApiNodeSettings,
): Record<string, unknown> {
  const common = {
    protocol: draft.protocol,
    name: finalName,
    listen_port: Number(draft.port) || 0,
  };
  if (draft.protocol === "vless-reality") {
    return {
      ...common,
      reality: {
        flow: draft.transport === "tcp" ? draft.flow : "",
        handshake_server: draft.handshake.trim(),
        handshake_port: Number(draft.handshakePort),
        server_name: draft.handshake.trim(),
        // Settings the form does not show keep their stored values.
        fingerprint: initial?.reality?.fingerprint || "chrome",
        max_time_difference: initial?.reality?.max_time_difference,
        transport: draft.transport,
        transport_settings:
          draft.transport === "grpc" && draft.serviceName.trim()
            ? { service_name: draft.serviceName.trim() }
            : {},
      },
    };
  }
  const wrapper = draft.shadowTLS
    ? {
        shadow_tls: {
          handshake_server: draft.shadowTLSServer.trim(),
          handshake_port: Number(draft.shadowTLSPort),
        },
      }
    : {};
  if (draft.protocol === "shadowsocks") {
    return {
      ...common,
      ...wrapper,
      shadowsocks: {
        method: draft.method,
        allow_insecure: draft.method === "none" && draft.allowUnsafe,
      },
    };
  }
  return {
    ...common,
    ...wrapper,
    snell: {
      version: draft.snellVersion,
      ...(draft.snellVersion === 5 ? { obfs_mode: draft.snellMode } : { mode: draft.snellMode }),
      allow_insecure: draft.snellMode === "unsafe-raw" && draft.allowUnsafe,
    },
  };
}

export function validateNodeDrafts(
  drafts: ReadonlyArray<NodeDraft>,
  serverName?: string,
  occupiedPorts?: ReadonlySet<number>,
): { index: number; message: string } | null {
  const seenPorts = new Set<number>();
  const prefix = (index: number, reason: string) =>
    drafts.length > 1 ? `第 ${index + 1} 个节点：${reason}` : reason;
  for (let index = 0; index < drafts.length; index++) {
    const draft = drafts[index];
    const finalName = defaultNodeName(draft, drafts, index, serverName);
    if (byteLength(finalName) > 96) {
      return { index, message: prefix(index, "名称最多 96 字节（约 32 个汉字）。") };
    }
    const rawPort = draft.port.trim();
    if (!rawPort) continue;
    const port = Number(rawPort);
    if (seenPorts.has(port)) {
      return { index, message: prefix(index, `端口 ${port} 在待创建列表中重复。`) };
    }
    if (occupiedPorts?.has(port)) {
      return { index, message: prefix(index, `监听端口 ${port} 已被这台服务器上的节点或转发占用。`) };
    }
    seenPorts.add(port);
  }
  return null;
}

export type ClientFormat = "link" | "surge";

export const clientFormatLabels: Record<ClientFormat, string> = {
  link: "链接",
  surge: "Surge 配置",
};

// Surge implements every Shadowsocks method except this one.
const surgeLacksMethod = "2022-blake3-chacha20-poly1305";

// Shadowsocks exports both a share link and a Surge line; the other
// protocols, and methods Surge lacks, have one form and offer no choice.
export function clientFormatChoices(node: Pick<ApiNode, "protocol" | "profile">): ClientFormat[] {
  if (node.protocol !== "shadowsocks") return [];
  const method = displayNodeProfile(node.protocol, node.profile).split("·")[0].trim();
  return method === surgeLacksMethod ? [] : ["link", "surge"];
}

export function clientFormats(client: ClientExport) {
  const forms: { format: ClientFormat; value: string }[] = [
    { format: "link", value: client.uri ?? "" },
    { format: "surge", value: client.surge_line ?? "" },
  ];
  return forms.filter((form) => form.value);
}

// Groups multi-node exports by format so incompatible client lines are never combined.
export function batchClientExports(clients: ReadonlyArray<ClientExport>) {
  const groups: { format: ClientFormat; label: string; value: string; count: number }[] = [];
  const uris = clients.map((client) => client.uri ?? "").filter(Boolean);
  if (uris.length > 1) {
    groups.push({ format: "link", label: `复制全部链接（${uris.length}）`, value: uris.join("\n"), count: uris.length });
  }
  const surgeLines = clients.map((client) => client.surge_line ?? "").filter(Boolean);
  if (surgeLines.length > 1) {
    groups.push({ format: "surge", label: `复制全部 Surge 配置（${surgeLines.length}）`, value: surgeLines.join("\n"), count: surgeLines.length });
  }
  return groups;
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
