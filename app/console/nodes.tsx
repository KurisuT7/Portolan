"use client";

import { Fragment, useState, type FormEvent, type ReactNode } from "react";
import { Check, Link2, Plus, Trash2 } from "lucide-react";
import { ApiError, type ApiForward, type ApiNode, type ClientExport } from "../lib/api";
import { serverHost } from "../lib/endpoints";
import { regionParts } from "../lib/fleet";
import { byteLength } from "../lib/format";
import { displayNodeProfile, groupNodesByServer, protocolLabels } from "../lib/nodes";
import { routeHref } from "../lib/routing";
import { configurationState, serverState, singBoxUnit, unitState } from "../lib/status";
import { cycleWord, formatBytes, portsCounted, totalBytes, trafficOf } from "../lib/traffic";
import { api, errorText, useFleet } from "./data";
import { navigate } from "./hooks";
import { CodeBlock, ConfirmDelete, copyText, Dialog, Empty, ErrorText, Field, PageHeader, ProtocolTag, SearchInput, Segmented, Spinner, Status, toast, useCopied } from "./ui";

type Protocol = ApiNode["protocol"];
const protocols: Protocol[] = ["vless-reality", "shadowsocks", "snell"];
const shadowsocksMethods = [
  "2022-blake3-aes-128-gcm",
  "2022-blake3-aes-256-gcm",
  "2022-blake3-chacha20-poly1305",
  "aes-128-gcm",
  "aes-192-gcm",
  "aes-256-gcm",
  "chacha20-ietf-poly1305",
  "xchacha20-ietf-poly1305",
  "none",
];
const exportValue = (client: ClientExport) => client.uri || client.surge_line || "";

export function NodesPage({ server }: { server?: string }) {
  const { data, errors } = useFleet();
  const [query, setQuery] = useState("");
  const [protocol, setProtocol] = useState<Protocol | "all">("all");
  const [creating, setCreating] = useState(false);
  const needle = query.trim().toLowerCase();
  const rows = data.nodes.filter((node) =>
    (!server || node.server_id === server)
    && (protocol === "all" || node.protocol === protocol)
    && `${node.name} ${node.listen_port} ${node.profile}`.toLowerCase().includes(needle));
  const filtered = !!(needle || server || protocol !== "all");
  return (
    <>
      <PageHeader
        title="节点"
        meta={errors.nodes ? "节点数据未更新" : `${data.nodes.length} 个 · ${data.nodes.filter((node) => !node.managed).length} 个由 Agent 发现`}
        actions={
          <button className="btn btn-primary" disabled={!data.servers.length || !!errors.nodes || !!errors.servers} onClick={() => setCreating(true)}>
            <Plus size={16} />添加节点
          </button>
        }
      />
      <div className="toolbar">
        <SearchInput value={query} onChange={setQuery} placeholder="搜索名称、端口或参数" />
        <select className="select-compact" aria-label="按服务器筛选" value={server ?? ""} onChange={(event) => navigate({ page: "nodes", server: event.target.value || undefined })}>
          <option value="">全部服务器</option>
          {data.servers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
        </select>
        <Segmented
          label="按协议筛选"
          value={protocol}
          onChange={setProtocol}
          options={[{ value: "all", label: "全部" }, ...protocols.map((value) => ({ value, label: protocolLabels[value] }))]}
        />
      </div>
      {rows.length ? <NodeList nodes={rows} grouped /> : (
        <Empty>{errors.nodes ? "暂时无法读取节点。" : filtered ? "没有匹配的节点" : "还没有节点。添加节点，或等待 Agent 发现服务器上已有的服务。"}</Empty>
      )}
      {creating && <NodeForm serverId={server} onClose={() => setCreating(false)} />}
    </>
  );
}

// One click copies the share link; the dialog only appears when the browser refuses clipboard access.
export function useNodeLink() {
  const { index } = useFleet();
  const [copied, setCopied] = useCopied();
  const [fallback, setFallback] = useState<{ title: string; value: string } | null>(null);
  async function copyLink(node: ApiNode, forward?: ApiForward) {
    const endpoint = forward ? { address: serverHost(index.servers.get(forward.ingress_server_id)), port: forward.listen_port, name: node.name } : undefined;
    const request = api.exportNode(node.id, endpoint).then(exportValue);
    try {
      await copyText(request);
      setCopied(forward?.id ?? node.id);
      toast(forward ? "已复制经入口连接的客户端链接" : "已复制节点链接");
    } catch {
      try {
        setFallback({ title: node.name, value: await request });
      } catch (failure) {
        toast(errorText(failure), "bad");
      }
    }
  }
  const dialog: ReactNode = fallback && (
    <Dialog title="节点链接" onClose={() => setFallback(null)}>
      <div className="dialog-body">
        <p><strong>{fallback.title}</strong></p>
        <CodeBlock value={fallback.value} label="复制链接" />
        <p className="hint">包含连接密钥，请勿公开分享。</p>
      </div>
    </Dialog>
  );
  return { copyLink, copied, dialog };
}

export function NodeList({ nodes, grouped = false }: { nodes: ApiNode[]; grouped?: boolean }) {
  const { data, errors, index, now, refresh } = useFleet();
  const link = useNodeLink();
  const [removing, setRemoving] = useState<ApiNode | null>(null);
  const groups = grouped ? groupNodesByServer(nodes, data.servers) : [{ serverId: "", serverName: "", nodes }];
  const dependents = (node: ApiNode) => data.forwards.filter((forward) => forward.target_node_id === node.id).length;
  return (
    <>
      <div className="stack-list">
        {groups.map((group) => {
          const server = index.servers.get(group.serverId);
          const config = configurationState(index.config.get(group.serverId), !!errors.config, now);
          const state = server ? serverState(server, now) : null;
          return (
            <Fragment key={group.serverId || "all"}>
              {grouped && (
                <a className="group-head" href={routeHref({ page: "server", id: group.serverId })}>
                  <span className="region">{regionParts(server?.region).code || "··"}</span>
                  <b>{group.serverName}</b>
                  <span className="count">{group.nodes.length}</span>
                  {state && state.tone !== "good" && <Status tone={state.tone}>{state.label}</Status>}
                  {config.tone === "bad" && <Status tone="bad">配置{config.label}</Status>}
                </a>
              )}
              <div className="list">
                {group.nodes.map((node) => {
                  const profile = displayNodeProfile(node.protocol, node.profile);
                  const core = node.managed ? unitState(index.servers.get(node.server_id), singBoxUnit, now) : null;
                  const server = trafficOf(index.traffic, "server", node.server_id);
                  const counted = !errors.traffic && portsCounted(server);
                  const traffic = trafficOf(index.traffic, "node", node.id);
                  return (
                    <div className="node-row" key={node.id}>
                      <ProtocolTag protocol={node.protocol} port={node.listen_port} />
                      <div className="node-name">
                        <b>{node.name}</b>
                        <span className="sub">
                          {profile || protocolLabels[node.protocol]}
                          {!node.managed && <> · <span title={node.source}>Agent 发现 · 只读</span></>}
                        </span>
                        {core && !core.running && <Status tone="bad">sing-box 未运行 · {core.detail}</Status>}
                      </div>
                      <span className="node-traffic" title={counted && traffic ? `${cycleWord(server)}接收 ${formatBytes(traffic.rx_bytes)} · 发送 ${formatBytes(traffic.tx_bytes)}` : undefined}>
                        {counted && `${cycleWord(server)} ${formatBytes(traffic ? totalBytes(traffic) : 0)}`}
                      </span>
                      <div className="node-actions">
                        <button className="btn btn-sm" disabled={!!errors.nodes} onClick={() => void link.copyLink(node)}>
                          {link.copied === node.id ? <Check size={14} /> : <Link2 size={14} />}
                          {link.copied === node.id ? "已复制" : "复制链接"}
                        </button>
                        {node.managed && (
                          <button className="icon-btn icon-btn-danger" disabled={!!errors.nodes} aria-label={`删除 ${node.name}`} title="删除" onClick={() => setRemoving(node)}>
                            <Trash2 size={15} />
                          </button>
                        )}
                      </div>
                    </div>
                  );
                })}
              </div>
            </Fragment>
          );
        })}
      </div>
      {link.dialog}
      {removing && (
        <ConfirmDelete
          title="删除节点"
          name={removing.name}
          description={dependents(removing) ? `仍有 ${dependents(removing)} 条转发指向它。先修改或删除这些转发，才能删除节点。` : "Agent 会从服务器上移除这个节点。"}
          disabled={dependents(removing) > 0}
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            await api.deleteNode(removing.id);
            toast(`已删除 ${removing.name}`);
            await refresh();
          }}
        />
      )}
    </>
  );
}

type Created = { id: string; protocol: Protocol; name: string; client: ClientExport };

export function NodeForm({ serverId: initialServer, onClose }: { serverId?: string; onClose: () => void }) {
  const { data, refresh } = useFleet();
  const [serverId, setServerId] = useState(initialServer || data.servers[0]?.id || "");
  const [selected, setSelected] = useState<Protocol[]>(["vless-reality"]);
  const [name, setName] = useState("");
  const [port, setPort] = useState("");
  const [handshake, setHandshake] = useState("aws.amazon.com");
  const [handshakePort, setHandshakePort] = useState("443");
  const [transport, setTransport] = useState("tcp");
  const [flow, setFlow] = useState("xtls-rprx-vision");
  const [serviceName, setServiceName] = useState("");
  const [method, setMethod] = useState("2022-blake3-aes-256-gcm");
  const [snellVersion, setSnellVersion] = useState<5 | 6>(6);
  const [snellMode, setSnellMode] = useState("default");
  const [allowUnsafe, setAllowUnsafe] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [created, setCreated] = useState<Created[]>([]);
  const [uncertain, setUncertain] = useState(false);
  const server = data.servers.find((item) => item.id === serverId);
  const done = new Set(created.map((item) => item.protocol));
  const remaining = selected.filter((protocol) => !done.has(protocol));
  const locked = created.length > 0;
  const unsafe = (remaining.includes("shadowsocks") && method === "none") || (remaining.includes("snell") && snellVersion === 6 && snellMode === "unsafe-raw");

  function toggle(protocol: Protocol) {
    setSelected((current) => {
      if (!current.includes(protocol)) return protocols.filter((item) => item === protocol || current.includes(item));
      return current.length > 1 ? current.filter((item) => item !== protocol) : current;
    });
  }

  function nodeName(protocol: Protocol) {
    const base = name.trim() || server?.name || protocolLabels[protocol];
    return selected.length > 1 ? `${base} ${protocolLabels[protocol]}` : base;
  }

  function request(protocol: Protocol) {
    const common = { server_id: serverId, name: nodeName(protocol), protocol, listen_port: selected.length === 1 ? Number(port) || 0 : 0 };
    if (protocol === "vless-reality") {
      return {
        ...common,
        reality: {
          flow: transport === "tcp" ? flow : "",
          handshake_server: handshake.trim(),
          handshake_port: Number(handshakePort),
          server_name: handshake.trim(),
          fingerprint: "chrome",
          transport,
          transport_settings: transport === "grpc" && serviceName.trim() ? { service_name: serviceName.trim() } : {},
        },
      };
    }
    if (protocol === "shadowsocks") return { ...common, shadowsocks: { method, allow_insecure: method === "none" && allowUnsafe } };
    return {
      ...common,
      snell: {
        version: snellVersion,
        ...(snellVersion === 5 ? { obfs_mode: snellMode } : { mode: snellMode }),
        allow_insecure: snellMode === "unsafe-raw" && allowUnsafe,
      },
    };
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (remaining.some((protocol) => byteLength(nodeName(protocol)) > 96)) {
      setError("名称过长：最多 96 字节（约 32 个汉字），多协议时会追加协议名。");
      return;
    }
    setBusy(true);
    setError("");
    try {
      for (const protocol of remaining) {
        const result = await api.createNode(request(protocol));
        const item = { id: result.id, protocol, name: nodeName(protocol), client: result.client };
        setCreated((current) => [...current, item]);
      }
      toast(remaining.length > 1 ? `已创建 ${remaining.length} 个节点` : "节点已创建");
    } catch (failure) {
      if (failure instanceof ApiError && failure.status === 0) {
        setUncertain(true);
        setError("连接中断，无法确认是否已创建。请关闭窗口核对节点列表，不要直接重试。");
      } else {
        setError(errorText(failure));
      }
    } finally {
      await refresh();
      setBusy(false);
    }
  }

  const finished = locked && !remaining.length;
  const exportable = created.filter((item) => exportValue(item.client));
  return (
    <Dialog title="添加节点" onClose={onClose} busy={busy} wide>
      <form onSubmit={submit}>
        <div className="dialog-body">
          {!finished && (
            <fieldset className="form" disabled={busy || uncertain}>
              <div className="chips" role="group" aria-label="协议，可多选">
                {protocols.map((protocol) => (
                  <button type="button" key={protocol} aria-pressed={selected.includes(protocol)} disabled={locked} onClick={() => toggle(protocol)}>
                    {protocolLabels[protocol]}{done.has(protocol) ? " · 已创建" : ""}
                  </button>
                ))}
              </div>
              <div className="form-grid">
                <Field label="服务器">
                  <select value={serverId} disabled={locked} onChange={(event) => setServerId(event.target.value)} required>
                    {data.servers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
                  </select>
                </Field>
                <Field label="名称" hint={selected.length > 1 ? "会追加协议名" : undefined}>
                  <input value={name} readOnly={locked} onChange={(event) => setName(event.target.value)} placeholder={server?.name || "节点名称"} />
                </Field>
                {selected.length === 1 && (
                  <Field label="端口">
                    <input type="number" min={1} max={65535} value={port} disabled={locked} onChange={(event) => setPort(event.target.value)} placeholder="自动分配" />
                  </Field>
                )}
                {remaining.includes("vless-reality") && (
                  <Field label="Reality 握手域名">
                    <input value={handshake} onChange={(event) => setHandshake(event.target.value)} required spellCheck={false} />
                  </Field>
                )}
                {remaining.includes("shadowsocks") && (
                  <Field label="Shadowsocks 加密">
                    <select value={method} onChange={(event) => setMethod(event.target.value)}>
                      {shadowsocksMethods.map((item) => <option key={item} value={item}>{item === "none" ? "none（不加密）" : item}</option>)}
                    </select>
                  </Field>
                )}
                {remaining.includes("snell") && (
                  <Field label="Snell 版本">
                    <select value={snellVersion} onChange={(event) => { const version = Number(event.target.value) as 5 | 6; setSnellVersion(version); setSnellMode(version === 5 ? "" : "default"); }}>
                      <option value={6}>v6</option>
                      <option value={5}>v5</option>
                    </select>
                  </Field>
                )}
              </div>
              {(remaining.includes("vless-reality") || remaining.includes("snell")) && (
                <details className="advanced">
                  <summary>高级参数</summary>
                  <div className="form-grid">
                    {remaining.includes("vless-reality") && (
                      <>
                        <Field label="握手端口">
                          <input type="number" min={1} max={65535} value={handshakePort} onChange={(event) => setHandshakePort(event.target.value)} required />
                        </Field>
                        <Field label="传输">
                          <select value={transport} onChange={(event) => setTransport(event.target.value)}>
                            <option value="tcp">TCP</option>
                            <option value="grpc">gRPC</option>
                          </select>
                        </Field>
                        {transport === "tcp" ? (
                          <Field label="Flow">
                            <select value={flow} onChange={(event) => setFlow(event.target.value)}>
                              <option value="xtls-rprx-vision">xtls-rprx-vision</option>
                              <option value="">无</option>
                            </select>
                          </Field>
                        ) : (
                          <Field label="Service Name">
                            <input value={serviceName} onChange={(event) => setServiceName(event.target.value)} placeholder="可留空" spellCheck={false} />
                          </Field>
                        )}
                      </>
                    )}
                    {remaining.includes("snell") && (
                      <Field label={snellVersion === 5 ? "Snell 混淆" : "Snell 模式"}>
                        <select value={snellMode} onChange={(event) => setSnellMode(event.target.value)}>
                          {snellVersion === 5 ? (
                            <>
                              <option value="">无</option>
                              <option value="http">HTTP</option>
                            </>
                          ) : (
                            <>
                              <option value="default">default</option>
                              <option value="unshaped">unshaped</option>
                              <option value="unsafe-raw">unsafe-raw（不加密）</option>
                            </>
                          )}
                        </select>
                      </Field>
                    )}
                  </div>
                </details>
              )}
              {unsafe && (
                <label className="check check-warn">
                  <input type="checkbox" checked={allowUnsafe} onChange={(event) => setAllowUnsafe(event.target.checked)} required />
                  <span>这条链路已有其他加密保护，允许使用不加密模式。</span>
                </label>
              )}
            </fieldset>
          )}
          <ErrorText>{error}</ErrorText>
          {locked && (
            <div className="created">
              <h3>已创建 {created.length} 个节点</h3>
              {created.map((item) => (
                <div className="created-item" key={item.id}>
                  <span className="name">{item.name}</span>
                  {exportValue(item.client) ? <CodeBlock value={exportValue(item.client)} label={`复制 ${item.name}`} /> : <p className="hint">服务器地址识别后可在节点列表复制链接。</p>}
                </div>
              ))}
              {exportable.length > 1 && <CodeBlock value={exportable.map((item) => exportValue(item.client)).join("\n")} label="复制全部链接" />}
              {remaining.length > 0 && <p className="hint">已创建的不会重复创建，修正后可继续创建剩余协议。</p>}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <button type="button" className={finished ? "btn btn-primary" : "btn"} disabled={busy} onClick={onClose}>{locked || uncertain ? "完成" : "取消"}</button>
          {!finished && !uncertain && (
            <button type="submit" className="btn btn-primary" disabled={busy || !serverId}>
              {busy && <Spinner size={15} />}
              {locked ? `创建剩余 ${remaining.length} 个` : selected.length > 1 ? `创建 ${selected.length} 个节点` : "创建节点"}
            </button>
          )}
        </div>
      </form>
    </Dialog>
  );
}
