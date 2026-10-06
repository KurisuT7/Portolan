"use client";

import { useEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import { ArrowDown, ArrowUp, RefreshCw } from "lucide-react";
import type { ApiTrafficItem, ApiTrafficPoint, TrafficKind, TrafficRange } from "../lib/api";
import { serverState } from "../lib/status";
import { axisLabel, byteParts, byteScale, cycleDays, cycleWord, formatBytes, formatRate, periodLabel, portErrorText, portsCounted, seriesTotal, totalBytes, trafficOf } from "../lib/traffic";
import { sampleIndexAt } from "../lib/quality";
import { api, errorText, useFleet } from "./data";
import { Segmented, Spinner } from "./ui";

const ranges = (cycles: boolean): ReadonlyArray<{ value: TrafficRange; label: string }> => [
  { value: "24h", label: "24 小时" },
  { value: "30d", label: "30 天" },
  { value: "12m", label: cycles ? "12 个周期" : "12 个月" },
];

export function Rates({ item }: { item: Pick<ApiTrafficItem, "rx_rate" | "tx_rate"> }) {
  return (
    <span className="rates" title="当前接收 / 发送速率">
      <span><ArrowDown size={12} aria-label="接收" />{formatRate(item.rx_rate)}</span>
      <span><ArrowUp size={12} aria-label="发送" />{formatRate(item.tx_rate)}</span>
    </span>
  );
}

function Bytes({ value }: { value: number }) {
  const parts = byteParts(value);
  return <>{parts.value}<small>{parts.unit}</small></>;
}

type Result = { key: string; range: TrafficRange; points?: ApiTrafficPoint[]; error?: string };

// The traffic of a server or forward: the current cycle's totals, current
// rates and a history by hour, day or cycle in the browser's time zone.
export function TrafficPanel({ kind, id, serverId, children }: { kind: Exclude<TrafficKind, "node">; id: string; serverId: string; children?: ReactNode }) {
  const { errors, index, now } = useFleet();
  const [range, setRange] = useState<TrafficRange>("24h");
  const [reload, setReload] = useState(0);
  const [result, setResult] = useState<Result | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const host = index.servers.get(serverId);
  const resetDay = host?.traffic_reset_day ?? 1;
  // The current period keeps growing; read the history again every minute.
  const minute = Math.floor(now / 60_000);
  const key = `${kind}:${id}:${range}:${resetDay}`;
  useEffect(() => {
    const controller = new AbortController();
    api.trafficHistory(kind, id, range, controller.signal)
      .then((points) => setResult({ key, range, points }))
      .catch((error) => {
        if (!controller.signal.aborted) setResult((previous) => ({ key, range, points: previous?.key === key ? previous.points : undefined, error: errorText(error) }));
      });
    return () => controller.abort();
  }, [kind, id, range, key, minute, reload]);

  const server = trafficOf(index.traffic, "server", serverId);
  const item = trafficOf(index.traffic, kind, id);
  const word = cycleWord(server);
  const online = !!host && serverState(host, now).label === "在线";
  const counted = kind === "server" ? !!server?.reported_at : portsCounted(server);
  const portError = server?.reported_at ? portErrorText(server.port_error) : "";
  const unavailable = !!errors.traffic;
  const current = counted && !unavailable ? item ?? { rx_bytes: 0, tx_bytes: 0, rx_rate: 0, tx_rate: 0 } : null;
  const loading = result?.key !== key;
  const shown = result?.range ?? range;
  const points = result?.points ?? [];
  const inspected = selected ?? points.length - 1;
  const point = points[inspected];
  const total = seriesTotal(points);

  return (
    <section className="section">
      <div className="section-header">
        <h2>流量{word === "本期" && server && <span className="section-note">本期 {cycleDays(server.since, resetDay)}</span>}</h2>
        <div className="quality-controls">
          <Segmented label="时间范围" value={range} options={ranges(word === "本期")} onChange={(value) => { setRange(value); setSelected(null); }} />
          <button className="icon-btn" aria-label="刷新" title="刷新" onClick={() => setReload((value) => value + 1)}>
            <RefreshCw size={15} className={loading ? "spin" : ""} />
          </button>
        </div>
      </div>
      {portError && <div className="notice tone-warn"><div className="notice-body"><span>{portError}</span></div></div>}
      {!server?.reported_at && !unavailable && (
        <p className="hint traffic-hint">{host?.agent_status === "pending" ? "安装 Agent 后开始统计流量。" : "还没有收到这台服务器的流量数据。Agent 每 30 秒上报一次。"}</p>
      )}
      <div className="stat-tiles">
        <div><span>{word}合计</span><strong>{current ? <Bytes value={totalBytes(current)} /> : "—"}</strong></div>
        <div><span>{word}接收</span><strong>{current ? <Bytes value={current.rx_bytes} /> : "—"}</strong></div>
        <div><span>{word}发送</span><strong>{current ? <Bytes value={current.tx_bytes} /> : "—"}</strong></div>
        <div className="rate-tile"><span>当前速率</span>{current && online ? <Rates item={current} /> : <strong>—</strong>}</div>
      </div>
      {result?.error && (
        <div className="notice tone-warn" role="alert">
          <div className="notice-body">
            <strong>{result.points ? "未能更新，显示的是上次结果" : "暂时无法读取流量记录"}</strong>
            <span>{result.error}</span>
          </div>
          <button className="btn btn-sm" onClick={() => setReload((value) => value + 1)}>重试</button>
        </div>
      )}
      {!result?.points && !result?.error && <div className="chart-placeholder"><Spinner />读取中…</div>}
      {result?.points && (
        <div className={`chart-card quality-body${loading ? " is-loading" : ""}`}>
          <TrafficChart points={points} range={shown} selected={selected} onSelect={setSelected} />
          {point && (
            <div className="inspector" aria-live="polite">
              <span className="inspector-time">{periodLabel(shown, point)}</span>
              <span><i className="swatch swatch-received" aria-hidden="true" />接收 <b>{formatBytes(point.rx_bytes)}</b></span>
              <span><i className="swatch swatch-sent" aria-hidden="true" />发送 <b>{formatBytes(point.tx_bytes)}</b></span>
              <span>合计 <b>{formatBytes(point.rx_bytes + point.tx_bytes)}</b></span>
              <span className="muted">此范围共 {formatBytes(total.rx + total.tx)}</span>
            </div>
          )}
        </div>
      )}
      <p className="hint">
        {kind === "server"
          ? "统计默认路由所在网卡的收发字节，包含服务器上所有程序的流量。"
          : "统计入口端口上与客户端之间的 TCP 和 UDP 流量，包含 IP 包头。"}
      </p>
      {children}
    </section>
  );
}

const height = 210;
const pad = { top: 14, right: 8, bottom: 28, left: 58 };
const plotHeight = height - pad.top - pad.bottom;

export function TrafficChart({ points, range, selected, onSelect }: {
  points: ApiTrafficPoint[];
  range: TrafficRange;
  selected: number | null;
  onSelect: (index: number | null) => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const observer = new ResizeObserver(([entry]) => setWidth(Math.floor(entry.contentRect.width)));
    observer.observe(box.current!);
    return () => observer.disconnect();
  }, []);
  const count = points.length;
  const plotWidth = Math.max(0, width - pad.left - pad.right);
  const bucket = count ? plotWidth / count : 0;
  const gap = Math.min(4, bucket * 0.25);
  const scale = byteScale(Math.max(0, ...points.map((point) => point.rx_bytes + point.tx_bytes)));
  const y = (bytes: number) => pad.top + plotHeight - (bytes / (scale.maximum * scale.divisor)) * plotHeight;
  const empty = points.every((point) => point.rx_bytes + point.tx_bytes === 0);
  const labels = width < 520 ? 3 : 5;
  const active = selected != null && selected < count ? selected : null;
  const described = points[active ?? count - 1];

  function inspect(event: PointerEvent<SVGSVGElement>) {
    const bounds = event.currentTarget.getBoundingClientRect();
    onSelect(sampleIndexAt((event.clientX - bounds.left - pad.left) / Math.max(1, plotWidth), count));
  }
  function keyboard(event: KeyboardEvent<SVGSVGElement>) {
    const moves: Record<string, number> = { ArrowLeft: -1, ArrowRight: 1 };
    if (!count || !(event.key in moves || event.key === "Home" || event.key === "End")) return;
    event.preventDefault();
    const current = active ?? count - 1;
    if (event.key === "Home") onSelect(0);
    else if (event.key === "End") onSelect(count - 1);
    else onSelect(Math.min(count - 1, Math.max(0, current + moves[event.key])));
  }

  return (
    <div className="chart traffic-chart" ref={box}>
      {width > 0 && (
        <svg
          width={width}
          height={height}
          role="slider"
          tabIndex={0}
          aria-label="流量历史，方向键选择时段"
          aria-valuemin={0}
          aria-valuemax={Math.max(0, count - 1)}
          aria-valuenow={active ?? Math.max(0, count - 1)}
          aria-valuetext={described ? `${periodLabel(range, described)} ${formatBytes(described.rx_bytes + described.tx_bytes)}` : "无数据"}
          onPointerMove={inspect}
          onPointerDown={inspect}
          onPointerLeave={() => onSelect(null)}
          onKeyDown={keyboard}
          onBlur={() => onSelect(null)}
        >
          {Array.from({ length: scale.intervals + 1 }, (_, position) => {
            const value = position * scale.step;
            return (
              <g key={position}>
                <line className="chart-grid" x1={pad.left} x2={pad.left + plotWidth} y1={y(value * scale.divisor)} y2={y(value * scale.divisor)} />
                <text className="chart-axis" x={pad.left - 8} y={y(value * scale.divisor) + 4} textAnchor="end">
                  {position ? `${Number(value.toFixed(2))} ${scale.unit}` : "0"}
                </text>
              </g>
            );
          })}
          {empty && <text className="chart-empty" x={pad.left + plotWidth / 2} y={pad.top + plotHeight / 2} textAnchor="middle">此时段没有流量记录</text>}
          {points.map((point, position) => {
            const x = pad.left + position * bucket + gap / 2;
            const barWidth = Math.max(1, bucket - gap);
            const received = y(point.rx_bytes);
            const top = y(point.rx_bytes + point.tx_bytes);
            return (
              <g key={point.start} className={active === position ? "bar is-active" : "bar"}>
                {point.rx_bytes > 0 && <rect className="bar-received" x={x} y={received} width={barWidth} height={pad.top + plotHeight - received} />}
                {point.tx_bytes > 0 && <rect className="bar-sent" x={x} y={top} width={barWidth} height={received - top} />}
              </g>
            );
          })}
          {Array.from({ length: labels }, (_, position) => {
            const pointIndex = Math.round((position * (count - 1)) / Math.max(1, labels - 1));
            const point = points[pointIndex];
            if (!point) return null;
            return (
              <text key={position} className="chart-axis" x={pad.left + (pointIndex + 0.5) * bucket} y={height - 8}
                textAnchor={position === 0 ? "start" : position === labels - 1 ? "end" : "middle"}>
                {axisLabel(range, new Date(point.start))}
              </text>
            );
          })}
          {active != null && (
            <line className="chart-cursor-line" x1={pad.left + (active + 0.5) * bucket} x2={pad.left + (active + 0.5) * bucket} y1={pad.top} y2={pad.top + plotHeight} />
          )}
        </svg>
      )}
    </div>
  );
}

// The current cycle's traffic of the nodes and forwards a server carries, largest first.
export function TrafficBreakdown({ serverId }: { serverId: string }) {
  const { index } = useFleet();
  const nodes = (index.nodesByServer.get(serverId) ?? []).map((node) => ({ kind: "node" as const, id: node.id, name: node.name, tag: "节点" }));
  const forwards = (index.forwardsByIngress.get(serverId) ?? []).map((forward) => ({ kind: "forward" as const, id: forward.id, name: forward.name, tag: "转发" }));
  const server = trafficOf(index.traffic, "server", serverId);
  if (!portsCounted(server)) return null;
  const rows = [...nodes, ...forwards]
    .map((row) => ({ ...row, item: trafficOf(index.traffic, row.kind, row.id) }))
    .map((row) => ({ ...row, total: row.item ? totalBytes(row.item) : 0 }))
    .sort((left, right) => right.total - left.total);
  if (!rows.length) return null;
  const largest = Math.max(1, ...rows.map((row) => row.total));
  return (
    <div className="breakdown">
      <h3>{cycleWord(server)}按节点和转发</h3>
      <ul>
        {rows.map((row) => (
          <li key={`${row.kind}:${row.id}`}>
            <span className="breakdown-tag">{row.tag}</span>
            {row.kind === "forward" ? <a href={`#/forwards/${encodeURIComponent(row.id)}`}>{row.name}</a> : <span className="breakdown-name">{row.name}</span>}
            <span className="breakdown-bar" aria-hidden="true"><i style={{ width: `${(row.total / largest) * 100}%` }} /></span>
            <span className="mono" title={row.item ? `接收 ${formatBytes(row.item.rx_bytes)} · 发送 ${formatBytes(row.item.tx_bytes)}` : undefined}>{formatBytes(row.total)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}
