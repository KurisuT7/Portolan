import type { ApiTraffic, ApiTrafficItem, ApiTrafficPoint, TrafficKind } from "./api";

const units = ["B", "KB", "MB", "GB", "TB", "PB"];

// Binary multiples with the familiar short names, as Linux tools report them.
export function byteParts(value: number) {
  let amount = Math.max(0, value);
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit++;
  }
  const digits = unit === 0 || amount >= 100 ? 0 : amount >= 10 ? 1 : 2;
  return { value: amount.toFixed(digits), unit: units[unit] };
}

export function formatBytes(value: number) {
  const parts = byteParts(value);
  return `${parts.value} ${parts.unit}`;
}

export function formatRate(bytesPerSecond: number) {
  return `${formatBytes(bytesPerSecond)}/s`;
}

export const totalBytes = (item: Pick<ApiTrafficItem, "rx_bytes" | "tx_bytes">) => item.rx_bytes + item.tx_bytes;

export type TrafficRange = "24h" | "30d" | "12m";

// Period edges in the browser's time zone, ending with the period now in
// progress: hours for a day, days for a month, months for a year.
export function trafficEdges(range: TrafficRange, now: Date) {
  const edges: Date[] = [];
  if (range === "24h") {
    const hour = new Date(now.getFullYear(), now.getMonth(), now.getDate(), now.getHours());
    for (let offset = -23; offset <= 1; offset++) edges.push(new Date(hour.getTime() + offset * 3_600_000));
  } else if (range === "30d") {
    for (let offset = -29; offset <= 1; offset++) edges.push(new Date(now.getFullYear(), now.getMonth(), now.getDate() + offset));
  } else {
    for (let offset = -11; offset <= 1; offset++) edges.push(new Date(now.getFullYear(), now.getMonth() + offset, 1));
  }
  return edges;
}

export function periodLabel(range: TrafficRange, start: Date) {
  if (range === "24h") return `${pad(start.getMonth() + 1)}/${pad(start.getDate())} ${pad(start.getHours())}:00`;
  if (range === "30d") return `${pad(start.getMonth() + 1)}/${pad(start.getDate())}`;
  return `${start.getFullYear()} 年 ${start.getMonth() + 1} 月`;
}

export function axisLabel(range: TrafficRange, start: Date) {
  if (range === "24h") return `${pad(start.getHours())}:00`;
  if (range === "30d") return `${start.getMonth() + 1}/${start.getDate()}`;
  return `${start.getMonth() + 1} 月`;
}

const pad = (value: number) => String(value).padStart(2, "0");

export type TrafficIndex = Map<string, ApiTrafficItem>;

export function indexTraffic(traffic: ApiTraffic): TrafficIndex {
  return new Map(traffic.items.map((item) => [`${item.kind}:${item.id}`, item]));
}

export function trafficOf(index: TrafficIndex, kind: TrafficKind, id: string) {
  return index.get(`${kind}:${id}`);
}

// The month's traffic of every server that has reported, or null when none has.
export function fleetTraffic(traffic: ApiTraffic) {
  const servers = traffic.items.filter((item) => item.kind === "server" && item.reported_at);
  if (!servers.length) return null;
  return servers.reduce((sum, item) => ({ rx: sum.rx + item.rx_bytes, tx: sum.tx + item.tx_bytes }), { rx: 0, tx: 0 });
}

// Node and forward traffic exists only where the server counts its ports.
export function portsCounted(server: ApiTrafficItem | undefined) {
  return !!server?.reported_at && !server.port_error;
}

export function portErrorText(code: ApiTrafficItem["port_error"]) {
  if (code === "nft_missing") return "服务器上没有 nft 命令，节点和转发的流量没有统计。安装 nftables 后会自动开始统计。";
  if (code === "nft_failed") return "nftables 没有接受计数规则，节点和转发的流量没有统计。请查看这台服务器的 Agent 日志。";
  return "";
}

// A readable maximum and step for a byte axis with about four intervals.
export function byteScale(maximum: number) {
  const top = Math.max(maximum, 1024);
  let unit = 0;
  while (top / 1024 ** unit >= 1024 && unit < units.length - 1) unit++;
  const scaled = top / 1024 ** unit;
  const rough = scaled / 4;
  const magnitude = 10 ** Math.floor(Math.log10(rough));
  const step = [1, 2, 2.5, 5, 10].map((factor) => factor * magnitude).find((candidate) => candidate >= rough) ?? 10 * magnitude;
  const intervals = Math.ceil(scaled / step);
  return { unit: units[unit], divisor: 1024 ** unit, step, maximum: step * intervals, intervals };
}

export function seriesTotal(points: ApiTrafficPoint[]) {
  return points.reduce((sum, point) => ({ rx: sum.rx + point.rx_bytes, tx: sum.tx + point.tx_bytes }), { rx: 0, tx: 0 });
}
