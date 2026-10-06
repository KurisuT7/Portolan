"use client";

import { useEffect, useState } from "react";
import { Activity, Check, Link2, Pencil, RefreshCw, Trash2 } from "lucide-react";
import type { ApiForward, ApiForwardProbeHistory, ProbeHistoryRange } from "../lib/api";
import { serverEndpoint } from "../lib/endpoints";
import { forwardTarget, regionParts, routeState } from "../lib/fleet";
import { milliseconds, parseTime, percent, relativeTime, timeLabel } from "../lib/format";
import { buildQualityIncidents, formatProbeWindow, getProbePointWindow, hasLatency, probeFailureHint, probePointIssue, probeStatusLabel } from "../lib/quality";
import { routeHref } from "../lib/routing";
import { configurationState } from "../lib/status";
import { LatencyChart } from "./chart";
import { api, errorText, useFleet } from "./data";
import { engineLabels, ForwardForm, networksLabel, probeBadge } from "./forwards";
import { navigate } from "./hooks";
import { useNodeLink } from "./nodes";
import { TrafficPanel } from "./traffic";
import { Address, Badge, ConfirmDelete, CopyButton, Empty, PageHeader, Segmented, Spinner, Status, toast, type Tone } from "./ui";

const ranges: ReadonlyArray<{ value: ProbeHistoryRange; label: string }> = [
  { value: "1h", label: "1 小时" },
  { value: "6h", label: "6 小时" },
  { value: "24h", label: "24 小时" },
  { value: "7d", label: "7 天" },
];

export function ForwardPage({ id }: { id: string }) {
  const { errors, index, now, refresh } = useFleet();
  const link = useNodeLink();
  const [dialog, setDialog] = useState<"edit" | "delete" | null>(null);
  const [probing, setProbing] = useState(false);
  const [version, setVersion] = useState(0);
  const forward = index.forwards.get(id);
  if (!forward) {
    return (
      <>
        <PageHeader back={{ href: "#/forwards", label: "转发" }} title="转发不存在" />
        <Empty>{errors.forwards ? "暂时无法读取转发。" : "这条转发可能已被删除。"}</Empty>
      </>
    );
  }
  const ingress = index.servers.get(forward.ingress_server_id);
  const endpoint = serverEndpoint(ingress, forward.listen_port);
  const target = forwardTarget(forward, index);
  const latest = index.probes.get(forward.id);
  const route = routeState(forward, index, !!(errors.probes || errors.forwards), now);
  const probe = route.probe;
  const config = configurationState(index.config.get(forward.ingress_server_id), !!errors.config, now, forward.updated_at);
  const measurable = forward.enabled && forward.networks.includes("tcp");
  const failure = probe.tone === "bad" ? latest?.last_error : "";

  async function probeNow() {
    setProbing(true);
    try {
      const result = await api.probeForward(id);
      if (result.status === "down") toast(`目标不可达${probeFailureHint(result.last_error) ? ` · ${probeFailureHint(result.last_error)}` : ""}`, "bad");
      else toast(`${milliseconds(result.latency_ms)} · ${result.successes}/${result.attempts} 次成功`);
      setVersion((value) => value + 1);
      await refresh();
    } catch (error) {
      toast(errorText(error), "bad");
    } finally {
      setProbing(false);
    }
  }

  return (
    <>
      <PageHeader
        back={{ href: "#/forwards", label: "转发" }}
        title={forward.name}
        status={<Badge tone={route.tone}>{route.stopped ? "进程未运行" : probeBadge(probe.tone, probe.label)}</Badge>}
        meta={`${networksLabel(forward)} · ${engineLabels[forward.engine]}${forward.enabled ? "" : " · 已停用"}`}
        actions={
          <>
            <button className="btn btn-primary" disabled={probing || !measurable} title={measurable ? undefined : forward.enabled ? "仅 UDP 的转发无法进行 TCP 检测" : "规则已停用"} onClick={probeNow}>
              {probing ? <Spinner size={15} /> : <Activity size={15} />}立即检测
            </button>
            <button className="btn" disabled={!!(errors.forwards || errors.servers || errors.nodes)} onClick={() => setDialog("edit")}><Pencil size={15} />编辑</button>
            <button className="btn btn-danger-quiet" disabled={!!errors.forwards} onClick={() => setDialog("delete")}><Trash2 size={15} />删除</button>
          </>
        }
      />
      <div className="route-board">
        <div className="route-end">
          <span className="route-label">入口</span>
          <a href={routeHref({ page: "server", id: forward.ingress_server_id })}>
            <span className="region">{regionParts(ingress?.region).code || "··"}</span>{ingress?.name ?? "未知服务器"}
          </a>
          <div className="endpoint-hero">
            {endpoint ? <Address value={endpoint} /> : <span className="mono">{`地址待识别 · :${forward.listen_port}`}</span>}
            {endpoint && <CopyButton value={endpoint} label="复制入口地址" />}
          </div>
          <div className="route-end-foot">
            <Status tone={config.tone}>配置{config.label}</Status>
            {route.stopped && <Status tone="bad">转发进程未运行 · {route.engineDetail}</Status>}
            {target.node && forward.enabled && endpoint && (
              <button className="btn btn-sm" onClick={() => void link.copyLink(target.node!, forward)} title="地址和端口已替换为此入口">
                {link.copied === forward.id ? <Check size={14} /> : <Link2 size={14} />}
                {link.copied === forward.id ? "已复制" : "复制客户端链接"}
              </button>
            )}
          </div>
        </div>
        <div className={`route-wire tone-${probe.tone}`} aria-hidden="true">
          <span>{probe.tone === "good" || probe.tone === "warn" ? probe.label : engineLabels[forward.engine]}</span>
        </div>
        <div className="route-end">
          <span className="route-label">目标 · {target.kind}</span>
          {target.server ? (
            <a href={routeHref({ page: "server", id: target.server.id })}>
              <span className="region">{regionParts(target.server.region).code || "··"}</span>{target.title}
            </a>
          ) : <span className="route-end-title">{target.title}</span>}
          <div className="endpoint-hero"><Address value={target.endpoint} /></div>
          {latest && <span className="sub">最近检测 {relativeTime(latest.checked_at, now)}</span>}
        </div>
      </div>
      {failure && (
        <div className="notice tone-bad">
          <div className="notice-body">
            <strong>入口无法连接目标{probe.detail ? `：${probe.detail}` : ""}</strong>
            <code>{failure}</code>
          </div>
        </div>
      )}
      <Quality forward={forward} version={version} />
      <TrafficPanel kind="forward" id={forward.id} serverId={forward.ingress_server_id} />
      {link.dialog}
      {dialog === "edit" && <ForwardForm initial={forward} onClose={() => setDialog(null)} />}
      {dialog === "delete" && (
        <ConfirmDelete
          title="删除转发"
          name={forward.name}
          description="入口 Agent 应用后停止监听该端口，目标节点不受影响。"
          onClose={() => setDialog(null)}
          onConfirm={async () => {
            await api.deleteForward(forward.id);
            toast(`已删除 ${forward.name}`);
            navigate({ page: "forwards" });
          }}
        />
      )}
    </>
  );
}

function Quality({ forward, version }: { forward: ApiForward; version: number }) {
  const [range, setRange] = useState<ProbeHistoryRange>("24h");
  const [reload, setReload] = useState(0);
  const [result, setResult] = useState<{ range: ProbeHistoryRange; history?: ApiForwardProbeHistory; error?: string } | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    api.probeHistory(forward.id, range, controller.signal)
      .then((history) => setResult({ range, history }))
      .catch((error) => {
        if (!controller.signal.aborted) setResult((previous) => ({ range, history: previous?.range === range ? previous.history : undefined, error: errorText(error) }));
      });
    return () => controller.abort();
  }, [forward.id, range, version, reload]);

  const history = result?.history;
  const loading = result?.range !== range;
  const points = history?.points ?? [];
  const latencyKnown = points.some(hasLatency);
  const sampled = points.filter((point) => point.attempts > 0 && point.status !== "unsupported").length;
  const incidents = history ? buildQualityIncidents(points, history.summary.to).reverse() : [];
  const inspected = selected ?? points.length - 1;
  const point = points[inspected];
  const window = point ? getProbePointWindow(points, inspected, history!.summary.to) : null;
  const changed = parseTime(forward.updated_at);
  const edited = changed != null && changed > (parseTime(forward.created_at) ?? changed);
  const changedInRange = history && edited && changed > Date.parse(history.summary.from);

  return (
    <section className="section">
      <div className="section-header">
        <h2>质量</h2>
        <div className="quality-controls">
          <Segmented label="时间范围" value={range} options={ranges} onChange={(value) => { setRange(value); setSelected(null); }} />
          <button className="icon-btn" aria-label="刷新" title="刷新" onClick={() => setReload((value) => value + 1)}>
            <RefreshCw size={15} className={loading ? "spin" : ""} />
          </button>
        </div>
      </div>
      {!forward.networks.includes("tcp") && <p className="hint">仅 UDP 的转发没有通用的 TCP 检测，未测量不代表不可用。</p>}
      {result?.error && (
        <div className="notice tone-warn" role="alert">
          <div className="notice-body">
            <strong>{history ? "未能更新，显示的是上次结果" : "暂时无法读取质量记录"}</strong>
            <span>{result.error}</span>
          </div>
          <button className="btn btn-sm" onClick={() => setReload((value) => value + 1)}>重试</button>
        </div>
      )}
      {!history && !result?.error && <div className="chart-placeholder"><Spinner />读取中…</div>}
      {history && (
        <div className={`quality-body${loading ? " is-loading" : ""}`}>
          <div className="stat-tiles">
            <div><span>平均延迟</span><strong>{latencyKnown ? history.summary.avg_latency_ms.toFixed(1) : "—"}{latencyKnown && <small>ms</small>}</strong></div>
            <div><span title="每次检测取成功建连的中位数，再取这些样本的 P95">P95</span><strong>{latencyKnown ? history.summary.p95_latency_ms.toFixed(1) : "—"}{latencyKnown && <small>ms</small>}</strong></div>
            <div><span>建连失败率</span><strong>{history.summary.availability_percent == null ? "—" : percent(history.summary.loss_percent)}</strong></div>
            <div><span>有采样时段</span><strong>{sampled}<small>/{points.length}</small></strong></div>
          </div>
          <div className="chart-card">
            <LatencyChart history={history} selected={selected} onSelect={setSelected} />
            {point && window && (
              <div className="inspector" aria-live="polite">
                <span className="inspector-time">{formatProbeWindow(window.start, window.end)}</span>
                <Status tone={pointTone(point.status)}>{probeStatusLabel(point.status)}</Status>
                {hasLatency(point) && <span>延迟 <b>{milliseconds(point.latency)}</b></span>}
                {hasLatency(point) && <span>抖动 <b>{milliseconds(point.jitter)}</b></span>}
                {point.attempts > 0 && <span>成功 <b>{point.successes}/{point.attempts}</b></span>}
                {!hasLatency(point) && <span className="muted">{probePointIssue(point)}</span>}
              </div>
            )}
          </div>
          <p className="hint">
            入口 → 目标 TCP 建连，每次检测 3 次。
            {changedInRange ? ` 规则于 ${timeLabel(forward.updated_at)} 修改，此前数据可能属于旧目标。` : ""}
          </p>
          {incidents.length > 0 && (
            <div className="incidents">
              <h3>异常时段 <span className="count">{incidents.length}</span></h3>
              <ul>
                {incidents.map((incident) => (
                  <li key={incident.start}>
                    <Status tone={incident.status === "down" ? "bad" : "warn"}>{incident.title}</Status>
                    <span className="incident-time">{formatProbeWindow(incident.start, incident.end)} · {incident.duration}</span>
                    <span className="muted">{incident.detail}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
          <details className="samples">
            <summary>采样明细 · {history.summary.sample_count} 次检测</summary>
            <div className="table-scroll">
              <table>
                <thead><tr><th>时段</th><th>状态</th><th>成功 / 尝试</th><th>延迟</th><th>抖动</th></tr></thead>
                <tbody>
                  {[...points].reverse().map((sample) => (
                    <tr key={sample.checked_at}>
                      <td>{timeLabel(sample.checked_at)}</td>
                      <td><Status tone={pointTone(sample.status)}>{probeStatusLabel(sample.status)}</Status></td>
                      <td>{sample.attempts ? `${sample.successes} / ${sample.attempts}` : "—"}</td>
                      <td>{hasLatency(sample) ? milliseconds(sample.latency) : "—"}</td>
                      <td>{hasLatency(sample) ? milliseconds(sample.jitter) : "—"}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </details>
        </div>
      )}
    </section>
  );
}

function pointTone(status: string): Tone {
  if (status === "stable") return "good";
  if (status === "degraded") return "warn";
  if (status === "down") return "bad";
  return "neutral";
}
