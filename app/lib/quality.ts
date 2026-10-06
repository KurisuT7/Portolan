import type { ApiForward, ApiForwardProbe, ApiForwardProbeHistoryPoint } from "./api";

export const QUALITY_LATENCY_THRESHOLD_MS = 500;
export const QUALITY_JITTER_THRESHOLD_MS = 30;
export const PROBE_FRESHNESS_WINDOW_MS = 2 * 60 * 1000;

type QualityReason = NonNullable<ApiForwardProbeHistoryPoint["reasons"]>[number] | "quality_variation";

export type QualityIncident = {
  start: string;
  end: string;
  status: "degraded" | "down";
  title: string;
  detail: string;
  duration: string;
};

export function probeStatusLabel(status: ApiForwardProbeHistoryPoint["status"]) {
  if (status === "stable") return "稳定";
  if (status === "degraded") return "质量下降";
  if (status === "down") return "连接中断";
  if (status === "unsupported") return "仅 UDP";
  return "无数据";
}

export function isProbeFresh(probe: ApiForwardProbe, now = Date.now()) {
  const checkedAt = Date.parse(probe.checked_at);
  const age = now - checkedAt;
  return Number.isFinite(age) && age >= 0 && age < PROBE_FRESHNESS_WINDOW_MS;
}

export function probePointIssue(point: ApiForwardProbeHistoryPoint) {
  if (point.status === "unknown") return "该时段没有采样";
  if (point.status === "unsupported") return "UDP 转发不计入可用率";
  if (point.status === "down") return "该时段探测全部失败";

  const reasons = reasonSet([point]);
  const issues: string[] = [];
  if (reasons.has("unreachable")) issues.push("出现短暂不可达");
  if (reasons.has("packet_loss")) issues.push(`建连失败 ${formatPercent(point.loss)}`);
  if (reasons.has("high_latency")) issues.push(`延迟 ${formatMilliseconds(point.latency)}（阈值 ${QUALITY_LATENCY_THRESHOLD_MS} ms）`);
  if (reasons.has("high_jitter")) issues.push(`抖动 ${formatMilliseconds(point.jitter)}（阈值 ${QUALITY_JITTER_THRESHOLD_MS} ms）`);
  return issues.join(" · ");
}

export function getProbePointWindow(
  points: ApiForwardProbeHistoryPoint[],
  index: number,
  rangeEnd: string,
) {
  const point = points[index];
  return {
    start: point?.checked_at ?? rangeEnd,
    end: points[index + 1]?.checked_at ?? rangeEnd,
  };
}

export function formatProbeWindow(startValue: string, endValue: string) {
  const start = new Date(startValue);
  const end = new Date(endValue);
  const startText = start.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
  const sameDay = start.getFullYear() === end.getFullYear()
    && start.getMonth() === end.getMonth()
    && start.getDate() === end.getDate();
  const endText = end.toLocaleString("zh-CN", sameDay ? {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  } : {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
  return `${startText} – ${endText}`;
}

export function buildQualityIncidents(
  points: ApiForwardProbeHistoryPoint[],
  rangeEnd: string,
) {
  const incidents: QualityIncident[] = [];
  let active: ApiForwardProbeHistoryPoint[] = [];

  const finish = (end: string) => {
    if (active.length === 0) return;
    incidents.push(summarizeIncident(active, end));
    active = [];
  };

  points.forEach((point) => {
    if (point.status === "degraded" || point.status === "down") {
      active.push(point);
      return;
    }
    finish(point.checked_at);
  });
  finish(rangeEnd);
  return incidents;
}

function summarizeIncident(points: ApiForwardProbeHistoryPoint[], end: string): QualityIncident {
  const reasons = reasonSet(points);
  const status = reasons.has("unreachable") ? "down" : "degraded";
  const availabilities = points.flatMap((point) => point.availability == null ? [] : [point.availability]);
  const minAvailability = availabilities.length > 0 ? Math.min(...availabilities) : null;
  const maxLoss = Math.max(0, ...points.map((point) => point.loss));
  const maxLatency = Math.max(0, ...points.map((point) => point.latency));
  const maxJitter = Math.max(0, ...points.map((point) => point.jitter));
  const start = points[0].checked_at;

  let title = "质量波动";
  let detail = `峰值延迟 ${formatMilliseconds(maxLatency)} · 峰值抖动 ${formatMilliseconds(maxJitter)}`;
  if (status === "down") {
    title = "连接中断";
    detail = minAvailability == null
      ? "该时段探测不可达"
      : `最低可用率 ${formatPercent(minAvailability)} · 最高建连失败 ${formatPercent(maxLoss)}`;
  } else if (reasons.size > 1) {
    title = "多项指标异常";
    const details: string[] = [];
    if (reasons.has("packet_loss")) details.push(`最低可用率 ${formatPercent(minAvailability ?? 0)}`);
    if (reasons.has("high_latency")) details.push(`峰值延迟 ${formatMilliseconds(maxLatency)}`);
    if (reasons.has("high_jitter")) details.push(`峰值抖动 ${formatMilliseconds(maxJitter)}`);
    detail = details.join(" · ");
  } else if (reasons.has("packet_loss")) {
    title = "可用率下降";
    detail = `最低可用率 ${formatPercent(minAvailability ?? 0)} · 最高建连失败 ${formatPercent(maxLoss)}`;
  } else if (reasons.has("high_latency")) {
    title = "延迟偏高";
    detail = `峰值延迟 ${formatMilliseconds(maxLatency)} · 阈值 ${QUALITY_LATENCY_THRESHOLD_MS} ms`;
  } else if (reasons.has("high_jitter")) {
    title = "抖动偏高";
    detail = `峰值抖动 ${formatMilliseconds(maxJitter)} · 阈值 ${QUALITY_JITTER_THRESHOLD_MS} ms`;
  }

  return {
    start,
    end,
    status,
    title,
    detail,
    duration: formatDuration(new Date(end).getTime() - new Date(start).getTime()),
  };
}

function reasonSet(points: ApiForwardProbeHistoryPoint[]) {
  const reasons = new Set(points.flatMap((point) => point.reasons ?? []));
  if (points.some((point) => point.status === "down")) reasons.add("unreachable");
  if (points.some((point) => point.availability != null && point.availability < 100)) reasons.add("packet_loss");
  if (points.some((point) => point.latency > QUALITY_LATENCY_THRESHOLD_MS)) reasons.add("high_latency");
  if (points.some((point) => point.jitter > QUALITY_JITTER_THRESHOLD_MS)) reasons.add("high_jitter");
  return reasons as Set<QualityReason>;
}

function formatDuration(milliseconds: number) {
  const minutes = Math.max(1, Math.round(milliseconds / 60_000));
  if (minutes < 60) return `${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  const remainder = minutes % 60;
  return remainder === 0 ? `${hours} 小时` : `${hours} 小时 ${remainder} 分`;
}

function formatPercent(value: number) {
  return `${value.toFixed(value < 10 ? 2 : 1)}%`;
}

function formatMilliseconds(value: number) {
  return `${value.toFixed(1)} ms`;
}

export function hasLatency(point: ApiForwardProbeHistoryPoint) {
  return point.successes > 0 && Number.isFinite(point.latency) && point.status !== "unknown" && point.status !== "unsupported";
}

// Missing and failed buckets deliberately break the latency line. They are not
// zero-latency samples and must never be interpolated into reassuring values.
export function latencyRuns(points: ApiForwardProbeHistoryPoint[]) {
  const runs: number[][] = [];
  let active: number[] = [];
  points.forEach((point, index) => {
    if (hasLatency(point)) {
      active.push(index);
      return;
    }
    if (active.length) runs.push(active);
    active = [];
  });
  if (active.length) runs.push(active);
  return runs;
}

export function latencySegments(points: ApiForwardProbeHistoryPoint[], width: number, height: number, maximum: number) {
  const x = (index: number) => ((index + 0.5) / Math.max(1, points.length)) * width;
  const y = (latency: number) => height - Math.min(1, Math.max(0, latency / Math.max(1, maximum))) * height;
  return latencyRuns(points).map((run) => run
    .map((index, position) => `${position ? "L" : "M"}${x(index).toFixed(2)},${y(points[index].latency).toFixed(2)}`)
    .join(" "));
}

export function sampleIndexAt(fraction: number, count: number) {
  if (count <= 0) return -1;
  return Math.min(count - 1, Math.max(0, Math.floor(fraction * count)));
}

// Rounds a chart ceiling up to 1/2/2.5/5 x 10^n so that four grid steps stay readable.
export function chartScale(maximum: number, steps = 4) {
  const rough = Math.max(1, maximum) / steps;
  const magnitude = 10 ** Math.floor(Math.log10(rough));
  const step = [1, 2, 2.5, 5, 10].map((factor) => factor * magnitude).find((value) => value >= rough) ?? 10 * magnitude;
  return { step, maximum: step * Math.ceil(Math.max(1, maximum) / step) };
}

export type ProbeTone = "good" | "warn" | "bad" | "neutral";

export function probeFailureHint(error = "") {
  const text = error.toLowerCase();
  if (text.includes("network is unreachable") && text.includes("[")) return "入口没有 IPv6 路由";
  if (text.includes("network is unreachable") || text.includes("no route to host")) return "路由不可达";
  if (text.includes("connection refused")) return "目标端口未监听";
  if (text.includes("timeout") || text.includes("deadline exceeded")) return "连接超时";
  return "";
}

export function probeState(
  forward: Pick<ApiForward, "enabled" | "networks" | "updated_at">,
  probe: ApiForwardProbe | undefined,
  unavailable: boolean,
  now: number,
): { label: string; tone: ProbeTone; detail: string } {
  if (unavailable) return { label: "数据未更新", tone: "neutral", detail: "" };
  if (!forward.enabled) return { label: "已停用", tone: "neutral", detail: "" };
  if (!forward.networks.includes("tcp") || probe?.status === "unsupported") return { label: "仅 UDP", tone: "neutral", detail: "不测量" };
  const changedAt = forward.updated_at ? Date.parse(forward.updated_at) : Number.NaN;
  if (!probe || !isProbeFresh(probe, now) || Date.parse(probe.checked_at) < changedAt) return { label: "等待采样", tone: "neutral", detail: "" };
  if (probe.status === "down") return { label: "不可达", tone: "bad", detail: probeFailureHint(probe.last_error) };
  return {
    label: formatMilliseconds(probe.latency_ms),
    tone: probe.status === "stable" ? "good" : "warn",
    detail: probe.status === "degraded" ? "波动" : "",
  };
}
