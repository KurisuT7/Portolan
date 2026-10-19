import type { ApiServer, PortMapping } from "./api";

// One editable range: public ports and, when they differ, the host ports they reach.
export type MappingRow = { public: string; host: string };

const maxMappings = 32;

function parseRange(text: string) {
  const match = /^\s*(\d{1,5})\s*(?:[-–~～]\s*(\d{1,5}))?\s*$/u.exec(text);
  if (!match) return null;
  const start = Number(match[1]);
  const end = match[2] ? Number(match[2]) : start;
  if (start < 1 || end > 65535 || end < start) return null;
  return { start, count: end - start + 1 };
}

const span = (start: number, count: number) => (count === 1 ? String(start) : `${start}-${start + count - 1}`);
const overlaps = (a: number, b: number, count: number, otherCount: number) => a < b + otherCount && b < a + count;

export function mappingRows(mappings: PortMapping[] = []): MappingRow[] {
  return mappings.map((item) => ({ public: span(item.public, item.count), host: item.host === item.public ? "" : span(item.host, item.count) }));
}

// Blank rows are ignored. A host port may be given as the first port of the range.
export function parseMappingRows(rows: MappingRow[]): { mappings: PortMapping[] } | { error: string } {
  const mappings: PortMapping[] = [];
  for (const [index, row] of rows.entries()) {
    if (!row.public.trim() && !row.host.trim()) continue;
    const label = `第 ${index + 1} 段`;
    const published = parseRange(row.public);
    if (!published) return { error: `${label}的公网端口要填端口号或范围，例如 30001-30020。` };
    let host = published.start;
    if (row.host.trim()) {
      const local = parseRange(row.host);
      if (!local || (local.count !== 1 && local.count !== published.count)) {
        return { error: `${label}的本机端口要填起始端口，或与公网端口数量相同的范围。` };
      }
      host = local.start;
    }
    if (host + published.count - 1 > 65535) return { error: `${label}的本机端口超出 65535。` };
    const mapping = { public: published.start, host, count: published.count };
    for (const [position, other] of mappings.entries()) {
      if (overlaps(mapping.public, other.public, mapping.count, other.count)) return { error: `${label}的公网端口与第 ${position + 1} 段重叠。` };
      if (overlaps(mapping.host, other.host, mapping.count, other.count)) return { error: `${label}的本机端口与第 ${position + 1} 段重叠。` };
    }
    mappings.push(mapping);
  }
  if (mappings.length > maxMappings) return { error: `最多 ${maxMappings} 段。` };
  return { mappings };
}

export function describeMapping(mapping: PortMapping) {
  const published = span(mapping.public, mapping.count);
  return mapping.public === mapping.host ? published : `${published} → ${span(mapping.host, mapping.count)}`;
}

export function hostRanges(mappings: PortMapping[]) {
  return [...mappings].sort((a, b) => a.host - b.host).map((item) => span(item.host, item.count)).join("、");
}

export function mappedPortCount(mappings: PortMapping[]) {
  return mappings.reduce((total, item) => total + item.count, 0);
}

// Hint for a host port field: what the mappings make of the entered port.
export function natPortHint(server: Pick<ApiServer, "nat_ports"> | undefined, value: string) {
  const mappings = server?.nat_ports ?? [];
  if (!mappings.length) return undefined;
  const port = Number(value);
  if (!value.trim() || !Number.isInteger(port) || port < 1) return `NAT 映射内的本机端口：${hostRanges(mappings)}`;
  const mapping = mappings.find((item) => port >= item.host && port < item.host + item.count);
  if (!mapping) return `不在 NAT 端口映射内（${hostRanges(mappings)}）`;
  return mapping.public === mapping.host ? undefined : `公网端口 ${mapping.public + port - mapping.host}`;
}
