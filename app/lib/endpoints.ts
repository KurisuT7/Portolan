import type { ApiServer } from "./api";

export type IngressAddressSource = {
  address?: string;
  ipv4?: string;
  ipv6?: string;
};

export function normalizeHost(value: string | undefined) {
  return (value || "").trim().replace(/^\[|\]$/g, "");
}

export function isPublicIPv4(value: string) {
  const octets = value.split(".").map(Number);
  if (octets.length !== 4 || octets.some((octet) => !Number.isInteger(octet) || octet < 0 || octet > 255)) return false;
  if (octets[0] === 10 || octets[0] === 127 || (octets[0] === 169 && octets[1] === 254) || (octets[0] === 192 && octets[1] === 168)) return false;
  if (octets[0] === 172 && octets[1] >= 16 && octets[1] <= 31) return false;
  return !(octets[0] === 100 && octets[1] >= 64 && octets[1] <= 127);
}

export function preferredIngressHost(source: IngressAddressSource | undefined) {
  if (!source) return "";
  const configured = normalizeHost(source.address);
  const configuredIsPlaceholder = !configured || configured === "自动识别公网地址";
  const configuredFamily = configured.includes(":")
    ? "ipv6"
    : /^\d{1,3}(?:\.\d{1,3}){3}$/.test(configured)
      ? "ipv4"
      : "domain";

  if (!configuredIsPlaceholder && configuredFamily === "domain") return configured;
  if (!configuredIsPlaceholder && configuredFamily === "ipv4" && isPublicIPv4(configured)) return configured;
  if (!configuredIsPlaceholder && configuredFamily === "ipv6") return configured;

  const ipv4 = normalizeHost(source.ipv4);
  if (isPublicIPv4(ipv4)) return ipv4;
  const ipv6 = normalizeHost(source.ipv6);
  if (ipv6) return ipv6;
  return configuredIsPlaceholder ? "" : configured;
}

export function formatEndpoint(host: string, port: string | number) {
  const normalized = normalizeHost(host);
  if (!normalized) return `:${port}`;
  return `${normalized.includes(":") ? `[${normalized}]` : normalized}:${port}`;
}

type ServerAddress = Pick<ApiServer, "address" | "ipv4_address" | "ipv6_address" | "nat_ports">;

export function serverHost(server: Pick<ApiServer, "address" | "ipv4_address" | "ipv6_address"> | undefined) {
  return preferredIngressHost(server ? { address: server.address, ipv4: server.ipv4_address, ipv6: server.ipv6_address } : undefined);
}

// The port clients dial at host to reach a host port of the server, or null
// when its NAT port mappings leave the port unreachable. NAT port forwarding
// covers IPv4 only: an IPv6 address reaches the host port directly.
export function publicPort(server: Pick<ApiServer, "nat_ports"> | undefined, host: string, port: number) {
  const mappings = server?.nat_ports ?? [];
  if (!mappings.length || normalizeHost(host).includes(":")) return port;
  const mapping = mappings.find((item) => port >= item.host && port < item.host + item.count);
  return mapping ? mapping.public + port - mapping.host : null;
}

// The port to show for a host port: the one clients dial at the server's address.
export function displayPort(server: ServerAddress | undefined, port: number) {
  return publicPort(server, serverHost(server), port);
}

export function serverEndpoint(server: ServerAddress | undefined, port: number) {
  const host = serverHost(server);
  const published = publicPort(server, host, port);
  return host && published != null ? formatEndpoint(host, published) : "";
}
