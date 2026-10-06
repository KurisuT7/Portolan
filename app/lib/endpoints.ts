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

export function serverHost(server: Pick<ApiServer, "address" | "ipv4_address" | "ipv6_address"> | undefined) {
  return preferredIngressHost(server ? { address: server.address, ipv4: server.ipv4_address, ipv6: server.ipv6_address } : undefined);
}

export function serverEndpoint(server: Pick<ApiServer, "address" | "ipv4_address" | "ipv6_address"> | undefined, port: number) {
  const host = serverHost(server);
  return host ? formatEndpoint(host, port) : "";
}
