// Go encodes an unset time.Time as year 1; treat it as missing rather than as a real moment.
export function parseTime(value?: string) {
  if (!value || value.startsWith("0001-")) return null;
  const at = Date.parse(value);
  return Number.isFinite(at) ? at : null;
}

export function timeLabel(value?: string) {
  const at = parseTime(value);
  if (at == null) return "—";
  return new Date(at).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

export function clockLabel(at: number) {
  return new Date(at).toLocaleTimeString("zh-CN", { hour12: false });
}

export function relativeTime(value: string | undefined, now: number) {
  const at = parseTime(value);
  if (at == null) return "—";
  const elapsed = now - at;
  if (elapsed < 60_000) return "刚刚";
  if (elapsed < 3_600_000) return `${Math.floor(elapsed / 60_000)} 分钟前`;
  if (elapsed < 86_400_000) return `${Math.floor(elapsed / 3_600_000)} 小时前`;
  return `${Math.floor(elapsed / 86_400_000)} 天前`;
}

export function milliseconds(value: number) {
  return `${value.toFixed(1)} ms`;
}

export function percent(value: number) {
  return `${value.toFixed(value < 10 ? 2 : 1)}%`;
}

export function byteLength(value: string) {
  return new TextEncoder().encode(value).length;
}
