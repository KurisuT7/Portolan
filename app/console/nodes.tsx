"use client";

import { Fragment, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Check, ChevronDown, Link2, Pencil, Plus, Trash2, X } from "lucide-react";
import { ApiError, type ApiForward, type ApiNode, type ApiNodeSettings, type ApiServer, type ClientExport, type SavedNode } from "../lib/api";
import { displayPort, publicPort, serverHost } from "../lib/endpoints";
import { regionParts } from "../lib/fleet";
import { hostRanges, natPortHint } from "../lib/nat";
import {
  batchClientExports,
  buildNodeRequest,
  clientFormatChoices,
  clientFormatLabels,
  clientFormats,
  createNodeDraft,
  defaultNodeName,
  displayNodeProfile,
  draftNeedsUnsafeConfirm,
  groupNodesByServer,
  nextDraftProtocol,
  nodeProtocols,
  protocolLabels,
  validateNodeDrafts,
  type ClientFormat,
  type NodeDraft,
} from "../lib/nodes";
import { routeHref } from "../lib/routing";
import { configurationState, serverState, singBoxUnit, unitState } from "../lib/status";
import { cycleWord, formatBytes, portsCounted, totalBytes, trafficOf } from "../lib/traffic";
import { api, errorText, useFleet } from "./data";
import { navigate } from "./hooks";
import { CodeBlock, ConfirmDelete, CopyButton, copyText, Dialog, Empty, ErrorText, Field, MenuButton, PageHeader, ProtocolTag, SearchInput, Segmented, Spinner, Status, toast, useCopied } from "./ui";

type Protocol = ApiNode["protocol"];
const protocols = nodeProtocols;
const maxBatchNodes = 32;
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
  const { data, errors, index } = useFleet();
  const [query, setQuery] = useState("");
  const [protocol, setProtocol] = useState<Protocol | "all">("all");
  const [creating, setCreating] = useState(false);
  const needle = query.trim().toLowerCase();
  const rows = data.nodes.filter((node) =>
    (!server || node.server_id === server)
    && (protocol === "all" || node.protocol === protocol)
    && `${node.name} ${node.listen_port} ${displayPort(index.servers.get(node.server_id), node.listen_port) ?? ""} ${node.profile}`.toLowerCase().includes(needle));
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
  const [fallback, setFallback] = useState<{ title: string; surge: boolean; value: string } | null>(null);
  async function copyLink(node: ApiNode, forward?: ApiForward, format?: ClientFormat) {
    const ingress = forward ? index.servers.get(forward.ingress_server_id) : undefined;
    const address = serverHost(ingress);
    const port = forward ? publicPort(ingress, address, forward.listen_port) : null;
    if (forward && port == null) return toast(`入口端口 ${forward.listen_port} 不在 NAT 端口映射内`, "bad");
    const endpoint = forward && port != null ? { address, port, name: node.name } : undefined;
    const surge = format === "surge";
    const request = api.exportNode(node.id, endpoint).then((client) => {
      if (surge && !client.surge_line) throw new Error("Surge 不支持这个节点的加密方式");
      return surge ? client.surge_line! : exportValue(client);
    });
    try {
      await copyText(request);
      setCopied(forward?.id ?? node.id);
      const what = surge ? " Surge 配置" : forward ? "客户端链接" : "节点链接";
      toast(forward ? `已复制经入口连接的${what}` : `已复制${what}`);
    } catch {
      try {
        setFallback({ title: node.name, surge, value: await request });
      } catch (failure) {
        toast(errorText(failure), "bad");
      }
    }
  }
  const dialog: ReactNode = fallback && (
    <Dialog title={fallback.surge ? "Surge 配置" : "节点链接"} onClose={() => setFallback(null)}>
      <div className="dialog-body">
        <p><strong>{fallback.title}</strong></p>
        <CodeBlock value={fallback.value} label={fallback.surge ? "复制 Surge 配置" : "复制链接"} />
        <p className="hint">包含连接密钥，请勿公开分享。</p>
      </div>
    </Dialog>
  );
  return { copyLink, copied, dialog };
}

// Copies the client configuration of node, through forward when given. A
// protocol with several forms lets the user pick one.
export function NodeLinkButton({ link, node, forward, label, title, disabled }: {
  link: ReturnType<typeof useNodeLink>;
  node: ApiNode;
  forward?: ApiForward;
  label: string;
  title?: string;
  disabled?: boolean;
}) {
  const copied = link.copied === (forward?.id ?? node.id);
  const content = <>{copied ? <Check size={14} /> : <Link2 size={14} />}{copied ? "已复制" : label}</>;
  const formats = clientFormatChoices(node);
  if (!formats.length) {
    return <button className="btn btn-sm" title={title} disabled={disabled} onClick={() => void link.copyLink(node, forward)}>{content}</button>;
  }
  return (
    <MenuButton
      label={label}
      title={title}
      disabled={disabled}
      items={formats.map((format) => ({ label: clientFormatLabels[format], onSelect: () => void link.copyLink(node, forward, format) }))}
    >
      {content}
      <ChevronDown size={13} aria-hidden="true" />
    </MenuButton>
  );
}

// Each form of a node's client configuration, with its own copy button.
function ClientExports({ name, client }: { name: string; client: ClientExport }) {
  const forms = clientFormats(client);
  if (!forms.length) return <p className="hint">服务器地址识别后可在节点列表复制链接。</p>;
  return (
    <>
      {forms.map(({ format, value }) => (
        <div className="client-export" key={format}>
          <span>{clientFormatLabels[format]}</span>
          <CodeBlock value={value} label={`复制 ${name} ${clientFormatLabels[format]}`} />
        </div>
      ))}
    </>
  );
}

export function NodeList({ nodes, grouped = false }: { nodes: ApiNode[]; grouped?: boolean }) {
  const { data, errors, index, now, refresh } = useFleet();
  const link = useNodeLink();
  const [removing, setRemoving] = useState<ApiNode | null>(null);
  const [editing, setEditing] = useState<ApiNodeSettings | null>(null);
  const [opening, setOpening] = useState("");
  async function edit(node: ApiNode) {
    setOpening(node.id);
    try {
      setEditing(await api.getNode(node.id));
    } catch (failure) {
      toast(errorText(failure), "bad");
    } finally {
      setOpening("");
    }
  }
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
                  const port = displayPort(index.servers.get(node.server_id), node.listen_port);
                  return (
                    <div className="node-row" key={node.id}>
                      <ProtocolTag protocol={node.protocol} port={port ?? node.listen_port}
                        title={port != null && port !== node.listen_port ? `本机端口 ${node.listen_port}` : undefined} />
                      <div className="node-name">
                        <b>{node.name}</b>
                        <span className="sub">
                          {profile || protocolLabels[node.protocol]}
                          {!node.managed && <> · <span title={node.source}>Agent 发现 · 只读</span></>}
                        </span>
                        {core && !core.running && <Status tone="bad">sing-box 未运行 · {core.detail}</Status>}
                        {port == null && <Status tone="warn">端口不在 NAT 映射内，无法经公网 IPv4 连接</Status>}
                      </div>
                      <span className="node-traffic" title={counted && traffic ? `${cycleWord(server)}接收 ${formatBytes(traffic.rx_bytes)} · 发送 ${formatBytes(traffic.tx_bytes)}` : undefined}>
                        {counted && `${cycleWord(server)} ${formatBytes(traffic ? totalBytes(traffic) : 0)}`}
                      </span>
                      <div className="node-actions">
                        <NodeLinkButton link={link} node={node} label="复制链接" disabled={!!errors.nodes} />
                        {node.managed && (
                          <button className="icon-btn" disabled={!!errors.nodes || !!opening} aria-label={`编辑 ${node.name}`} title="编辑" onClick={() => void edit(node)}>
                            {opening === node.id ? <Spinner size={15} /> : <Pencil size={15} />}
                          </button>
                        )}
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
      {editing && <NodeForm initial={editing} onClose={() => setEditing(null)} />}
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

type CreatedItem = { id: string; protocol: Protocol; name: string; port: number; client: ClientExport };
type CreatedResult = { items: CreatedItem[]; updatedAt: string };

const protocolOptions = protocols.map((value) => ({ value, label: protocolLabels[value] }));

// Renders the shared per-node configuration fields for both single-node editing and multi-node creation cards.
function NodeDraftFields({ draft, drafts, index, server, editing, onChange }: {
  draft: NodeDraft;
  drafts: NodeDraft[];
  index: number;
  server?: ApiServer;
  editing: boolean;
  onChange: (patch: Partial<NodeDraft>) => void;
}) {
  const placeholderName = editing ? (server?.name || "节点名称") : defaultNodeName({ ...draft, name: "" }, drafts, index, server?.name);
  const portHint = editing
    ? ["修改后原端口停止监听", natPortHint(server, draft.port)].filter(Boolean).join(" · ") || undefined
    : draft.port.trim() ? natPortHint(server, draft.port) : undefined;
  const unsafe = draftNeedsUnsafeConfirm(draft);

  return (
    <>
      <div className="node-draft-grid">
        <Field label="名称">
          <input value={draft.name} onChange={(event) => onChange({ name: event.target.value })} placeholder={placeholderName} />
        </Field>
        <Field label="端口" hint={portHint}>
          <input
            type="number"
            min={1}
            max={65535}
            value={draft.port}
            onChange={(event) => onChange({ port: event.target.value })}
            required={editing}
            placeholder="自动分配"
          />
        </Field>
        {draft.protocol === "vless-reality" && (
          <Field label="Reality 握手域名">
            <input value={draft.handshake} onChange={(event) => onChange({ handshake: event.target.value })} required spellCheck={false} />
          </Field>
        )}
        {draft.protocol === "shadowsocks" && (
          <Field label="Shadowsocks 加密">
            <select
              value={draft.method}
              onChange={(event) => onChange({ method: event.target.value, allowUnsafe: false })}
            >
              {shadowsocksMethods.map((item) => <option key={item} value={item}>{item === "none" ? "none（不加密）" : item}</option>)}
            </select>
          </Field>
        )}
        {draft.protocol === "snell" && (
          <Field label="Snell 版本">
            <select
              value={draft.snellVersion}
              onChange={(event) => {
                const version = Number(event.target.value) as 5 | 6;
                onChange({ snellVersion: version, snellMode: version === 5 ? "" : "default", allowUnsafe: false });
              }}
            >
              <option value={6}>v6</option>
              <option value={5}>v5</option>
            </select>
          </Field>
        )}
      </div>

      {draft.protocol !== "vless-reality" && (
        <label className="check">
          <input type="checkbox" checked={draft.shadowTLS} onChange={(event) => onChange({ shadowTLS: event.target.checked })} />
          用 ShadowTLS 包装
        </label>
      )}
      {draft.protocol !== "vless-reality" && draft.shadowTLS && (
        <div className="form-grid">
          <Field label="ShadowTLS 握手域名" hint="需支持 TLS 1.3">
            <input value={draft.shadowTLSServer} onChange={(event) => onChange({ shadowTLSServer: event.target.value })} required spellCheck={false} />
          </Field>
          <Field label="ShadowTLS 握手端口">
            <input type="number" min={1} max={65535} value={draft.shadowTLSPort} onChange={(event) => onChange({ shadowTLSPort: event.target.value })} required />
          </Field>
        </div>
      )}

      {(draft.protocol === "vless-reality" || draft.protocol === "snell") && (
        <details className="advanced">
          <summary>高级参数</summary>
          {draft.protocol === "vless-reality" ? (
            <div className="node-draft-grid">
              <Field label="握手端口">
                <input type="number" min={1} max={65535} value={draft.handshakePort} onChange={(event) => onChange({ handshakePort: event.target.value })} required />
              </Field>
              <Field label="传输">
                <select value={draft.transport} onChange={(event) => onChange({ transport: event.target.value })}>
                  <option value="tcp">TCP</option>
                  <option value="grpc">gRPC</option>
                </select>
              </Field>
              {draft.transport === "tcp" ? (
                <Field label="Flow">
                  <select value={draft.flow} onChange={(event) => onChange({ flow: event.target.value })}>
                    <option value="xtls-rprx-vision">xtls-rprx-vision</option>
                    <option value="">无</option>
                  </select>
                </Field>
              ) : (
                <Field label="Service Name">
                  <input value={draft.serviceName} onChange={(event) => onChange({ serviceName: event.target.value })} placeholder="可留空" spellCheck={false} />
                </Field>
              )}
            </div>
          ) : (
            <div className="form-grid">
              <Field label={draft.snellVersion === 5 ? "Snell 混淆" : "Snell 模式"}>
                <select
                  value={draft.snellMode}
                  onChange={(event) => onChange({ snellMode: event.target.value, allowUnsafe: false })}
                >
                  {draft.snellVersion === 5 ? (
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
            </div>
          )}
        </details>
      )}

      {unsafe && (
        <label className="check check-warn">
          <input type="checkbox" checked={draft.allowUnsafe} onChange={(event) => onChange({ allowUnsafe: event.target.checked })} required />
          <span>这条链路已有其他加密保护，允许使用不加密模式。</span>
        </label>
      )}
    </>
  );
}

// With initial the form edits that node; otherwise it creates a batch of independent node drafts on one server.
export function NodeForm({ serverId: initialServer, initial, onClose }: { serverId?: string; initial?: ApiNodeSettings; onClose: () => void }) {
  const { data, errors, index, now, refresh } = useFleet();
  const [serverId, setServerId] = useState(initial?.server_id || initialServer || data.servers[0]?.id || "");
  const [drafts, setDrafts] = useState<NodeDraft[]>(() => [
    createNodeDraft("draft-1", initial?.protocol ?? "vless-reality", initial),
  ]);
  const [rotate, setRotate] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [errorIndex, setErrorIndex] = useState<number | null>(null);
  const [createdResult, setCreatedResult] = useState<CreatedResult | null>(null);
  const [saved, setSaved] = useState<(SavedNode & { name: string; port: number }) | null>(null);
  const [uncertain, setUncertain] = useState(false);
  const nextDraftKey = useRef(2);
  const scrollToKey = useRef<string | null>(null);
  const cardRefs = useRef(new Map<string, HTMLDivElement>());

  useEffect(() => {
    if (!scrollToKey.current) return;
    const key = scrollToKey.current;
    scrollToKey.current = null;
    cardRefs.current.get(key)?.scrollIntoView({ behavior: "smooth", block: "nearest" });
  }, [drafts]);

  const server = data.servers.find((item) => item.id === serverId);
  const occupiedPorts = new Set<number>([
    ...(index.nodesByServer.get(serverId) ?? [])
      .filter((node) => node.id !== initial?.id)
      .map((node) => node.listen_port),
    ...(index.forwardsByIngress.get(serverId) ?? []).map((forward) => forward.listen_port),
  ]);

  function updateDraft(draftIndex: number, patch: Partial<NodeDraft>) {
    setDrafts((current) => current.map((item, position) => (position === draftIndex ? { ...item, ...patch } : item)));
    setError("");
    setErrorIndex(null);
  }

  function addDraft() {
    if (drafts.length >= maxBatchNodes) return;
    const key = `draft-${nextDraftKey.current++}`;
    scrollToKey.current = key;
    setDrafts((current) => [...current, createNodeDraft(key, nextDraftProtocol(current))]);
    setError("");
    setErrorIndex(null);
  }

  function removeDraft(draftIndex: number) {
    if (drafts.length <= 1) return;
    setDrafts((current) => current.filter((_, position) => position !== draftIndex));
    setError("");
    setErrorIndex(null);
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const validation = validateNodeDrafts(drafts, server?.name, occupiedPorts);
    if (validation) {
      setErrorIndex(validation.index);
      setError(validation.message);
      return;
    }
    setBusy(true);
    setError("");
    setErrorIndex(null);
    try {
      if (initial) {
        const draft = drafts[0];
        const finalName = draft.name.trim() || server?.name || protocolLabels[initial.protocol];
        const settings = buildNodeRequest(draft, finalName, initial);
        const result = await api.updateNode(initial.id, { ...settings, rotate_credentials: rotate });
        setSaved({ ...result, name: finalName, port: Number(draft.port) || initial.listen_port });
      } else {
        const nodesPayload = drafts.map((draft, position) =>
          buildNodeRequest(draft, defaultNodeName(draft, drafts, position, server?.name)),
        );
        const result = await api.createNodes({ server_id: serverId, nodes: nodesPayload });
        const items: CreatedItem[] = result.items.map((item, position) => ({
          id: item.id,
          protocol: item.protocol ?? drafts[position].protocol,
          name: item.name ?? defaultNodeName(drafts[position], drafts, position, server?.name),
          port: item.listen_port ?? (Number(drafts[position].port) || 0),
          client: item.client,
        }));
        setCreatedResult({ items, updatedAt: result.updated_at });
        toast(items.length > 1 ? `已创建 ${items.length} 个节点` : "节点已创建");
      }
    } catch (failure) {
      if (failure instanceof ApiError && failure.status === 0) {
        setUncertain(true);
        setError(initial ? "连接中断，无法确认是否已保存。请关闭窗口核对节点列表。" : "连接中断，无法确认是否已创建。请关闭窗口核对节点列表，不要直接重试。");
      } else {
        const message = errorText(failure);
        const match = /^第 (\d+) 个节点：/.exec(message);
        setErrorIndex(match ? Number(match[1]) - 1 : null);
        setError(message);
      }
    } finally {
      await refresh();
      setBusy(false);
    }
  }

  if (saved && initial) {
    // Forwards to the node dial its new port, so their ingress servers apply the change too.
    const ingress = saved.port === initial.listen_port ? [] : data.forwards.filter((forward) => forward.target_node_id === initial.id).map((forward) => forward.ingress_server_id);
    return (
      <Dialog title="已保存" onClose={onClose}>
        <div className="dialog-body">
          <div className="saved"><Check size={18} aria-hidden="true" /><strong>{saved.name}</strong></div>
          <ClientExports name={saved.name} client={saved.client} />
          <dl className="facts facts-compact">
            {[...new Set([serverId, ...ingress])].map((id) => {
              const config = configurationState(index.config.get(id), !!errors.config, now, saved.updated_at);
              return (
                <div key={id}>
                  <dt>{index.servers.get(id)?.name ?? "服务器"}{id !== serverId && "（转发入口）"}</dt>
                  <dd><Status tone={config.tone}>{config.label}</Status></dd>
                </div>
              );
            })}
          </dl>
        </div>
        <div className="dialog-footer">
          <button className="btn btn-primary" onClick={onClose}>完成</button>
        </div>
      </Dialog>
    );
  }

  if (createdResult) {
    const batchExports = batchClientExports(createdResult.items.map((item) => item.client));
    const config = configurationState(index.config.get(serverId), !!errors.config, now, createdResult.updatedAt);
    return (
      <Dialog title={createdResult.items.length > 1 ? `已创建 ${createdResult.items.length} 个节点` : "节点已创建"} onClose={onClose} wide={createdResult.items.length > 1}>
        <div className="dialog-body">
          {batchExports.length > 0 && (
            <div className="created-batch">
              {batchExports.map((group) => (
                <CopyButton key={group.format} className="btn btn-sm" value={group.value} label={group.label}>
                  {group.label}
                </CopyButton>
              ))}
            </div>
          )}
          <div className="created-list">
            {createdResult.items.map((item) => {
              const publishedPort = item.port ? displayPort(server, item.port) : null;
              return (
                <div className="created-card" key={item.id}>
                  <div className="created-head">
                    <ProtocolTag
                      protocol={item.protocol}
                      port={publishedPort ?? (item.port || undefined)}
                      title={item.port && publishedPort != null && publishedPort !== item.port ? `本机端口 ${item.port}` : undefined}
                    />
                    <strong>{item.name}</strong>
                    {item.port > 0 && publishedPort == null && <Status tone="warn">不在 NAT 映射内</Status>}
                  </div>
                  <ClientExports name={item.name} client={item.client} />
                </div>
              );
            })}
          </div>
          <dl className="facts facts-compact">
            <div>
              <dt>{server?.name ?? "服务器"}</dt>
              <dd><Status tone={config.tone}>{config.label}</Status></dd>
            </div>
          </dl>
        </div>
        <div className="dialog-footer">
          <button className="btn btn-primary" onClick={onClose}>完成</button>
        </div>
      </Dialog>
    );
  }

  const serverNatHint = !initial && server?.nat_ports?.length
    ? `NAT 本机端口范围：${hostRanges(server.nat_ports)}`
    : undefined;

  return (
    <Dialog title={initial ? "编辑节点" : "添加节点"} onClose={onClose} busy={busy} wide>
      <form onSubmit={submit}>
        <fieldset className="dialog-body form" disabled={busy || uncertain}>
          <Field label="服务器" hint={serverNatHint}>
            <select
              value={serverId}
              disabled={!!initial}
              onChange={(event) => {
                setServerId(event.target.value);
                setError("");
                setErrorIndex(null);
              }}
              required
            >
              {data.servers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
            </select>
          </Field>

          {initial ? (
            <>
              <NodeDraftFields
                draft={drafts[0]}
                drafts={drafts}
                index={0}
                server={server}
                editing
                onChange={(patch) => updateDraft(0, patch)}
              />
              <label className="check">
                <input type="checkbox" checked={rotate} onChange={(event) => setRotate(event.target.checked)} />
                重新生成密钥（旧链接失效）
              </label>
              <p className="hint">保存后立即下发到服务器，现有连接可能短暂中断。</p>
            </>
          ) : (
            <div className="node-drafts">
              {drafts.map((draft, position) => (
                <div
                  key={draft.key}
                  ref={(element) => {
                    if (element) cardRefs.current.set(draft.key, element);
                    else cardRefs.current.delete(draft.key);
                  }}
                  className={`node-draft${errorIndex === position ? " is-error" : ""}`}
                >
                  <div className="node-draft-head">
                    <Segmented
                      label={`第 ${position + 1} 个节点协议`}
                      value={draft.protocol}
                      options={protocolOptions}
                      onChange={(protocol) => updateDraft(position, { protocol, allowUnsafe: false })}
                    />
                    {drafts.length > 1 && (
                      <button
                        type="button"
                        className="icon-btn icon-btn-sm icon-btn-danger"
                        aria-label={`移除第 ${position + 1} 个节点`}
                        title="移除"
                        onClick={() => removeDraft(position)}
                      >
                        <X size={15} />
                      </button>
                    )}
                  </div>
                  <NodeDraftFields
                    draft={draft}
                    drafts={drafts}
                    index={position}
                    server={server}
                    editing={false}
                    onChange={(patch) => updateDraft(position, patch)}
                  />
                </div>
              ))}
              {drafts.length < maxBatchNodes && (
                <button type="button" className="node-draft-add" onClick={addDraft}>
                  <Plus size={15} aria-hidden="true" />
                  再加一个节点
                </button>
              )}
            </div>
          )}

          <ErrorText>{error}</ErrorText>
        </fieldset>
        <div className="dialog-footer">
          <button type="button" className="btn" disabled={busy} onClick={onClose}>{uncertain ? "关闭" : "取消"}</button>
          {!uncertain && (
            <button type="submit" className="btn btn-primary" disabled={busy || !serverId}>
              {busy && <Spinner size={15} />}
              {initial ? "保存" : drafts.length > 1 ? `创建 ${drafts.length} 个节点` : "创建节点"}
            </button>
          )}
        </div>
      </form>
    </Dialog>
  );
}
