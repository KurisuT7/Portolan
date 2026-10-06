"use client";

import { Fragment, useState, type FormEvent } from "react";
import { ArrowRight, Check, Link2, Plus } from "lucide-react";
import { ApiError, type ApiForward, type ForwardInput } from "../lib/api";
import { serverEndpoint } from "../lib/endpoints";
import { forwardTarget, regionParts, routeState, sortRoutes } from "../lib/fleet";
import { byteLength, milliseconds, percent } from "../lib/format";
import { formatNodeOption, groupNodesByServer } from "../lib/nodes";
import { routeHref } from "../lib/routing";
import { configurationState } from "../lib/status";
import { formatBytes, portsCounted, totalBytes, trafficOf } from "../lib/traffic";
import { api, errorText, useFleet } from "./data";
import { useHistory } from "./history";
import { navigate } from "./hooks";
import { useNodeLink } from "./nodes";
import { Badge, CopyButton, Dialog, Empty, ErrorText, Field, PageHeader, SearchInput, Segmented, Sparkline, Spinner, Status, type Tone } from "./ui";

export const engineLabels: Record<ApiForward["engine"], string> = { "sing-box": "sing-box", realm: "Realm" };

export function networksLabel(forward: Pick<ApiForward, "networks">) {
  return forward.networks.map((network) => network.toUpperCase()).join(" + ");
}

export function probeBadge(tone: Tone, label: string) {
  if (tone === "good") return "正常";
  if (tone === "warn") return "波动";
  if (tone === "bad") return "不可达";
  return label;
}

export function ForwardsPage({ server }: { server?: string }) {
  const { data, errors, index } = useFleet();
  const [query, setQuery] = useState("");
  const [creating, setCreating] = useState(false);
  const needle = query.trim().toLowerCase();
  const rows = data.forwards.filter((forward) => {
    if (server && forward.ingress_server_id !== server) return false;
    const target = forwardTarget(forward, index);
    const text = [forward.name, forward.listen_port, index.servers.get(forward.ingress_server_id)?.name, target.title, target.endpoint].join(" ");
    return text.toLowerCase().includes(needle);
  });
  const disabled = data.forwards.filter((forward) => !forward.enabled).length;
  return (
    <>
      <PageHeader
        title="转发"
        meta={errors.forwards ? "转发数据未更新" : `${data.forwards.length} 条${disabled ? ` · ${disabled} 条已停用` : ""}`}
        actions={
          <button className="btn btn-primary" disabled={!data.servers.length || !!(errors.forwards || errors.servers || errors.nodes)} onClick={() => setCreating(true)}>
            <Plus size={16} />添加转发
          </button>
        }
      />
      <div className="toolbar">
        <SearchInput value={query} onChange={setQuery} placeholder="搜索名称、服务器、地址或端口" />
        <select className="select-compact" aria-label="按入口服务器筛选" value={server ?? ""} onChange={(event) => navigate({ page: "forwards", server: event.target.value || undefined })}>
          <option value="">全部入口</option>
          {data.servers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
        </select>
      </div>
      {rows.length ? <RouteGrid forwards={rows} /> : (
        <Empty>{errors.forwards ? "暂时无法读取转发。" : needle || server ? "没有匹配的转发" : "转发把入口服务器的端口连到目标节点或任意地址。"}</Empty>
      )}
      {creating && <ForwardForm ingressId={server} onClose={() => setCreating(false)} />}
    </>
  );
}

export function RouteGrid({ forwards }: { forwards: ApiForward[] }) {
  const { index, now } = useFleet();
  const link = useNodeLink();
  return (
    <>
      <div className="route-grid">
        {sortRoutes(forwards, index, now).map((forward) => <RouteCard key={forward.id} forward={forward} link={link} />)}
      </div>
      {link.dialog}
    </>
  );
}

function RouteCard({ forward, link }: { forward: ApiForward; link: ReturnType<typeof useNodeLink> }) {
  const { errors, index, now } = useFleet();
  const history = useHistory(forward.id);
  const ingress = index.servers.get(forward.ingress_server_id);
  const endpoint = serverEndpoint(ingress, forward.listen_port);
  const target = forwardTarget(forward, index);
  const route = routeState(forward, index, !!(errors.probes || errors.forwards), now);
  const probe = route.probe;
  const config = configurationState(index.config.get(forward.ingress_server_id), !!errors.config, now, forward.updated_at);
  const measured = probe.tone !== "neutral";
  const latency = !route.stopped && measured && probe.tone !== "bad" ? index.probes.get(forward.id)!.latency_ms : null;
  const summary = history?.summary;
  const traffic = trafficOf(index.traffic, "forward", forward.id);
  const facts = [
    ...(summary && summary.availability_percent != null ? [`24h 均值 ${milliseconds(summary.avg_latency_ms)}`, `失败率 ${percent(summary.loss_percent)}`] : []),
    ...(!errors.traffic && portsCounted(trafficOf(index.traffic, "server", forward.ingress_server_id)) ? [`本月 ${formatBytes(traffic ? totalBytes(traffic) : 0)}`] : []),
  ];
  return (
    <article className={`route-card tone-${route.tone}${forward.enabled ? "" : " is-off"}`}>
      <div className="route-card-head">
        <a className="card-link" href={routeHref({ page: "forward", id: forward.id })}>{forward.name}</a>
        <Badge tone={route.tone}>{route.stopped ? "进程未运行" : probeBadge(probe.tone, probe.label)}</Badge>
      </div>
      <div className="route-path">
        <span><em>{regionParts(ingress?.region).code || "··"}</em>{ingress?.name ?? "未知服务器"}</span>
        <ArrowRight size={13} aria-hidden="true" />
        <span><em>{regionParts(target.server?.region).code || "··"}</em>{target.title}</span>
      </div>
      <div className="route-metric">
        {latency != null && <strong className="big-number">{latency.toFixed(1)}<small>ms</small></strong>}
        {route.stopped && <strong className="route-reason">{route.engineDetail}</strong>}
        {!route.stopped && probe.tone === "bad" && <strong className="route-reason">{probe.detail || "连接失败"}</strong>}
        {!route.stopped && !measured && <span className="route-reason muted">{probe.label === "仅 UDP" ? "仅 UDP · 不做 TCP 检测" : probe.label}</span>}
        {facts.length > 0 && (
          <span className="route-summary">
            {facts.map((fact, position) => <Fragment key={fact}>{position > 0 && <br />}{fact}</Fragment>)}
          </span>
        )}
      </div>
      {forward.enabled && forward.networks.includes("tcp") ? <Sparkline points={history?.points ?? []} tone={probe.tone === "neutral" ? "neutral" : probe.tone} /> : <div className="spark spark-empty" />}
      <div className="route-card-foot">
        <span className="copyable">
          <span className="mono">{endpoint || `地址待识别 · :${forward.listen_port}`}</span>
          {endpoint && <CopyButton value={endpoint} label="复制入口地址" />}
        </span>
        {target.node && forward.enabled && endpoint && (
          <button className="btn btn-sm" onClick={() => void link.copyLink(target.node!, forward)} title="复制客户端链接，地址和端口已替换为此入口">
            {link.copied === forward.id ? <Check size={14} /> : <Link2 size={14} />}
            {link.copied === forward.id ? "已复制" : "客户端链接"}
          </button>
        )}
      </div>
      {(config.tone === "bad" || (config.tone === "warn" && config.label !== "等待状态确认")) && (
        <Status tone={config.tone}>入口配置{config.label}</Status>
      )}
    </article>
  );
}

type TargetKind = "node" | "server" | "host";

export function ForwardForm({ initial, ingressId, onClose }: { initial?: ApiForward; ingressId?: string; onClose: () => void }) {
  const { data, errors, index, now, refresh } = useFleet();
  const firstIngress = initial?.ingress_server_id || ingressId || data.servers[0]?.id || "";
  const remoteNodes = data.nodes.filter((node) => node.server_id !== firstIngress);
  const [ingress, setIngress] = useState(firstIngress);
  const [name, setName] = useState(initial?.name ?? "");
  const [port, setPort] = useState(initial ? String(initial.listen_port) : "");
  const [kind, setKind] = useState<TargetKind>(initial ? (initial.target_node_id ? "node" : initial.target_server_id ? "server" : "host") : data.nodes.length ? "node" : "host");
  const [targetNode, setTargetNode] = useState(initial?.target_node_id || (remoteNodes.find((node) => node.managed) ?? remoteNodes[0] ?? data.nodes[0])?.id || "");
  const [targetServer, setTargetServer] = useState(initial?.target_server_id || (data.servers.find((server) => server.id !== firstIngress) ?? data.servers[0])?.id || "");
  const [host, setHost] = useState(initial && !initial.target_node_id && !initial.target_server_id ? initial.target_host : "");
  const [targetPort, setTargetPort] = useState(initial && !initial.target_node_id ? String(initial.target_port) : "");
  const [networks, setNetworks] = useState<ApiForward["networks"]>(initial?.networks ?? ["tcp", "udp"]);
  const [engine, setEngine] = useState<ApiForward["engine"]>(initial?.engine ?? "realm");
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState<ApiForward | null>(null);
  const [uncertain, setUncertain] = useState(false);
  const groups = groupNodesByServer(data.nodes, data.servers);
  const ingressName = index.servers.get(ingress)?.name ?? "入口";
  const targetName = kind === "node" ? index.nodes.get(targetNode)?.name : kind === "server" ? index.servers.get(targetServer)?.name : host.trim();
  const suggestedName = `${ingressName} → ${targetName || "目标"}`;
  const moved = !!initial && ingress !== initial.ingress_server_id;

  function toggleNetwork(network: "tcp" | "udp") {
    setNetworks((current) => current.includes(network)
      ? current.filter((item) => item !== network)
      : (["tcp", "udp"] as const).filter((item) => item === network || current.includes(item)));
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const finalName = name.trim() || suggestedName;
    if (!networks.length) return setError("至少选择 TCP 或 UDP。");
    if (byteLength(finalName) > 96) return setError("名称最多 96 字节（约 32 个汉字）。");
    const payload: ForwardInput = {
      name: finalName,
      ingress_server_id: ingress,
      listen_port: Number(port) || 0,
      networks,
      engine,
      enabled,
      ...(kind === "node" ? { target_node_id: targetNode }
        : kind === "server" ? { target_server_id: targetServer, target_port: Number(targetPort) }
        : { target_host: host.trim(), target_port: Number(targetPort) }),
    };
    setBusy(true);
    setError("");
    try {
      const result = initial ? await api.updateForward(initial.id, payload) : await api.createForward(payload);
      setSaved(result);
      await refresh();
    } catch (failure) {
      if (failure instanceof ApiError && failure.status === 0) {
        setUncertain(true);
        setError("连接中断，无法确认是否已保存。请关闭窗口核对转发列表，避免重复创建或覆盖。");
        await refresh();
      } else {
        setError(errorText(failure));
      }
    } finally {
      setBusy(false);
    }
  }

  if (saved) {
    const endpoint = serverEndpoint(index.servers.get(saved.ingress_server_id), saved.listen_port);
    const ingressConfig = configurationState(index.config.get(saved.ingress_server_id), !!errors.config, now, saved.updated_at);
    const previousConfig = moved && initial ? configurationState(index.config.get(initial.ingress_server_id), !!errors.config, now, saved.updated_at) : null;
    return (
      <Dialog title={initial ? "已保存" : "转发已创建"} onClose={onClose}>
        <div className="dialog-body">
          <div className="saved"><Check size={18} aria-hidden="true" /><strong>{saved.name}</strong></div>
          {endpoint && saved.enabled && (
            <div className="endpoint-hero">
              <span className="mono">{endpoint}</span>
              <CopyButton value={endpoint} label="复制入口地址">复制</CopyButton>
            </div>
          )}
          <dl className="facts facts-compact">
            <div>
              <dt>{index.servers.get(saved.ingress_server_id)?.name ?? "入口"}</dt>
              <dd><Status tone={ingressConfig.tone}>{ingressConfig.label}</Status></dd>
            </div>
            {previousConfig && initial && (
              <div>
                <dt>{index.servers.get(initial.ingress_server_id)?.name ?? "原入口"}（移除旧规则）</dt>
                <dd><Status tone={previousConfig.tone}>{previousConfig.label}</Status></dd>
              </div>
            )}
          </dl>
        </div>
        <div className="dialog-footer">
          {!initial && <button className="btn" onClick={() => { onClose(); navigate({ page: "forward", id: saved.id }); }}>查看详情</button>}
          <button className="btn btn-primary" onClick={onClose}>完成</button>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog title={initial ? "编辑转发" : "添加转发"} onClose={onClose} busy={busy} wide>
      <form onSubmit={submit}>
        <fieldset className="dialog-body form" disabled={busy || uncertain}>
          <div className="form-grid">
            <Field label="入口服务器">
              <select value={ingress} onChange={(event) => setIngress(event.target.value)} required>
                {data.servers.map((server) => <option key={server.id} value={server.id}>{server.name}</option>)}
              </select>
            </Field>
            <Field label="入口端口" hint={initial ? "修改后原端口停止监听" : undefined}>
              <input type="number" min={1} max={65535} value={port} onChange={(event) => setPort(event.target.value)} required={!!initial} placeholder="自动分配" />
            </Field>
          </div>
          {moved && <p className="inline-warn">更换入口需要两台服务器分别应用，切换期间可能短暂中断。</p>}
          <div className="field">
            <span className="field-label">目标</span>
            <Segmented
              label="目标类型"
              value={kind}
              onChange={setKind}
              options={[
                ...(data.nodes.length ? [{ value: "node" as const, label: "协议节点" }] : []),
                { value: "server" as const, label: "服务器端口" },
                { value: "host" as const, label: "自定义地址" },
              ]}
            />
          </div>
          {kind === "node" ? (
            <Field label="节点">
              <select value={targetNode} onChange={(event) => setTargetNode(event.target.value)} required>
                {!index.nodes.has(targetNode) && <option value="">选择节点</option>}
                {groups.map((group) => (
                  <optgroup key={group.serverId} label={group.serverName}>
                    {group.nodes.map((node) => <option key={node.id} value={node.id}>{formatNodeOption(node)}</option>)}
                  </optgroup>
                ))}
              </select>
            </Field>
          ) : (
            <div className="form-grid">
              {kind === "server" ? (
                <Field label="服务器" hint="按入口的出站能力自动选择 IPv4 或 IPv6">
                  <select value={targetServer} onChange={(event) => setTargetServer(event.target.value)} required>
                    {!index.servers.has(targetServer) && <option value="">选择服务器</option>}
                    {data.servers.map((server) => <option key={server.id} value={server.id}>{server.name}</option>)}
                  </select>
                </Field>
              ) : (
                <Field label="地址">
                  <input value={host} onChange={(event) => setHost(event.target.value)} required placeholder="IPv4、IPv6 或域名" spellCheck={false} />
                </Field>
              )}
              <Field label="端口">
                <input type="number" min={1} max={65535} value={targetPort} onChange={(event) => setTargetPort(event.target.value)} required />
              </Field>
            </div>
          )}
          <div className="form-grid">
            <div className="field">
              <span className="field-label">协议</span>
              <div className="chips" role="group" aria-label="转发协议">
                {(["tcp", "udp"] as const).map((network) => (
                  <button type="button" key={network} aria-pressed={networks.includes(network)} onClick={() => toggleNetwork(network)}>{network.toUpperCase()}</button>
                ))}
              </div>
            </div>
            <div className="field">
              <span className="field-label">引擎</span>
              <Segmented label="转发引擎" value={engine} onChange={setEngine} options={[{ value: "realm", label: "Realm" }, { value: "sing-box", label: "sing-box" }]} />
              <span className="field-hint">{engine === "realm" ? "独立进程；默认构建不支持 TCP 半关闭。" : "与本机节点共用进程；应用时现有连接可能短暂中断。"}</span>
            </div>
          </div>
          <Field label="名称">
            <input value={name} onChange={(event) => setName(event.target.value)} placeholder={suggestedName} maxLength={96} />
          </Field>
          {initial && (
            <label className="check">
              <input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />
              启用
            </label>
          )}
          <ErrorText>{error}</ErrorText>
        </fieldset>
        <div className="dialog-footer">
          <button type="button" className="btn" disabled={busy} onClick={onClose}>{uncertain ? "关闭" : "取消"}</button>
          {!uncertain && (
            <button type="submit" className="btn btn-primary" disabled={busy || !ingress}>
              {busy && <Spinner size={15} />}
              {initial ? "保存" : "创建"}
            </button>
          )}
        </div>
      </form>
    </Dialog>
  );
}
