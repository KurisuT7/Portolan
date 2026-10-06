"use client";

import { useState, type FormEvent } from "react";
import { Cpu, KeyRound, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import type { ApiServer, CreateServerResponse } from "../lib/api";
import { serverHost } from "../lib/endpoints";
import { regionParts, targetsServer } from "../lib/fleet";
import { relativeTime, timeLabel } from "../lib/format";
import { routeHref } from "../lib/routing";
import { coresReady } from "../lib/cores";
import { configurationState, realmForwardId, serverState, singBoxUnit, unitState } from "../lib/status";
import { formatBytes, totalBytes, trafficOf } from "../lib/traffic";
import { CoreDialog, CoreLine } from "./cores";
import { api, errorText, useFleet } from "./data";
import { navigate } from "./hooks";
import { ForwardForm, RouteGrid } from "./forwards";
import { NodeForm, NodeList } from "./nodes";
import { Rates, TrafficBreakdown, TrafficPanel } from "./traffic";
import { Badge, CodeBlock, ConfirmDelete, CopyButton, Dialog, Empty, ErrorText, Field, PageHeader, ProtocolTag, SearchInput, Section, Spinner, Status, toast } from "./ui";

function matches(server: ApiServer, query: string) {
  const text = [server.name, server.region, server.address, server.ipv4_address, server.ipv6_address].join(" ").toLowerCase();
  return text.includes(query.trim().toLowerCase());
}

export function ServersPage() {
  const { data, errors } = useFleet();
  const [query, setQuery] = useState("");
  const [creating, setCreating] = useState(false);
  const [cores, setCores] = useState(false);
  const rows = data.servers.filter((server) => matches(server, query));
  return (
    <>
      <PageHeader
        title="服务器"
        meta={errors.servers ? "服务器数据未更新" : `${data.servers.length} 台`}
        actions={
          <>
            <button className="btn" disabled={!!errors.cores} onClick={() => setCores(true)}>
              <Cpu size={16} />核心版本
            </button>
            <button className="btn btn-primary" disabled={!!errors.servers} onClick={() => setCreating(true)}>
              <Plus size={16} />添加服务器
            </button>
          </>
        }
      />
      {data.servers.length > 4 && (
        <div className="toolbar">
          <SearchInput value={query} onChange={setQuery} placeholder="搜索名称、地区或地址" />
        </div>
      )}
      {rows.length ? <ServerGrid servers={rows} /> : (
        <Empty>{errors.servers ? "暂时无法读取服务器。" : query ? "没有匹配的服务器" : "添加服务器后，在服务器上运行安装命令即可接入。"}</Empty>
      )}
      {creating && <ServerForm onClose={() => setCreating(false)} />}
      {cores && <CoreDialog onClose={() => setCores(false)} />}
    </>
  );
}

export function ServerGrid({ servers }: { servers: ApiServer[] }) {
  return <div className="server-grid">{servers.map((server) => <ServerCard key={server.id} server={server} />)}</div>;
}

function ServerCard({ server }: { server: ApiServer }) {
  const { errors, index, now } = useFleet();
  const state = serverState(server, now);
  const config = configurationState(index.config.get(server.id), !!errors.config, now);
  const host = serverHost(server);
  const nodes = index.nodesByServer.get(server.id) ?? [];
  const forwards = index.forwardsByIngress.get(server.id) ?? [];
  const region = regionParts(server.region);
  const traffic = errors.traffic ? undefined : trafficOf(index.traffic, "server", server.id);
  return (
    <article className={`server-card tone-${state.tone}`}>
      <span className="watermark" aria-hidden="true">{region.code}</span>
      <div className="server-card-head">
        <span className="region">{region.code || "··"}</span>
        <Status tone={errors.servers ? "neutral" : state.tone}>
          {errors.servers ? "未更新" : state.label === "离线" ? `离线 · ${relativeTime(server.last_seen_at, now)}` : state.label}
        </Status>
      </div>
      <div>
        <a className="card-link" href={routeHref({ page: "server", id: server.id })}>{server.name}</a>
        <p className="sub">{region.place || (state.label === "待安装" ? "等待安装 Agent" : "地区待识别")}</p>
      </div>
      <span className="copyable">
        {host ? <><span className="mono">{host}</span><CopyButton value={host} label="复制服务器地址" /></> : <span className="sub">等待上报地址</span>}
      </span>
      <div className="ptags">
        {nodes.slice(0, 4).map((node) => <ProtocolTag key={node.id} protocol={node.protocol} port={node.listen_port} />)}
        {nodes.length > 4 && <span className="sub">+{nodes.length - 4}</span>}
        {!nodes.length && <span className="sub">无节点</span>}
      </div>
      {traffic?.reported_at && (
        <div className="card-traffic" title={`本月接收 ${formatBytes(traffic.rx_bytes)} · 发送 ${formatBytes(traffic.tx_bytes)}`}>
          <span>本月 <b>{formatBytes(totalBytes(traffic))}</b></span>
          {state.label === "在线" && <Rates item={traffic} />}
        </div>
      )}
      <div className="server-card-foot">
        <span>{forwards.length ? `${forwards.length} 条入口转发` : "无入口转发"}</span>
        {(config.tone === "bad" || config.label === "结果未确认") && <Status tone={config.tone}>配置{config.label}</Status>}
      </div>
    </article>
  );
}

export function ServerPage({ id }: { id: string }) {
  const { data, errors, index, now } = useFleet();
  const [dialog, setDialog] = useState<"edit" | "install" | "delete" | "node" | "forward" | null>(null);
  const server = index.servers.get(id);
  if (!server) {
    return (
      <>
        <PageHeader back={{ href: "#/servers", label: "服务器" }} title="服务器不存在" />
        <Empty>{errors.servers ? "暂时无法读取服务器。" : "这台服务器可能已被删除。"}</Empty>
      </>
    );
  }
  const state = serverState(server, now);
  const region = regionParts(server.region);
  const nodes = index.nodesByServer.get(id) ?? [];
  const outgoing = index.forwardsByIngress.get(id) ?? [];
  const incoming = data.forwards.filter((forward) => forward.ingress_server_id !== id && targetsServer(forward, id, index));
  const unavailable = !!(errors.servers || errors.nodes || errors.forwards);
  return (
    <>
      <PageHeader
        back={{ href: "#/servers", label: "服务器" }}
        title={<><span className="region region-lg">{region.code || "··"}</span>{server.name}</>}
        status={<Badge tone={errors.servers ? "neutral" : state.tone}>{errors.servers ? "状态未更新" : state.label}</Badge>}
        meta={region.place || undefined}
        actions={
          <>
            <button className="btn" disabled={!!errors.servers} onClick={() => setDialog("edit")}><Pencil size={15} />编辑</button>
            {state.label !== "待安装" && <button className="btn" onClick={() => setDialog("install")}><KeyRound size={15} />重装 Agent</button>}
            <button className="btn btn-danger-quiet" disabled={!!errors.servers} onClick={() => setDialog("delete")}><Trash2 size={15} />删除</button>
          </>
        }
      />
      {state.label === "待安装" ? <InstallPanel server={server} /> : <ServerFacts server={server} />}
      <Section
        title="节点"
        count={nodes.length}
        actions={<button className="btn btn-sm" disabled={unavailable} onClick={() => setDialog("node")}><Plus size={15} />添加节点</button>}
      >
        {nodes.length ? <NodeList nodes={nodes} /> : <Empty>{errors.nodes ? "节点数据未更新。" : "还没有节点。Agent 发现的现有服务也会显示在这里。"}</Empty>}
      </Section>
      <Section
        title="入口转发"
        count={outgoing.length}
        actions={<button className="btn btn-sm" disabled={unavailable} onClick={() => setDialog("forward")}><Plus size={15} />添加转发</button>}
      >
        {outgoing.length ? <RouteGrid forwards={outgoing} /> : <Empty>{errors.forwards ? "转发数据未更新。" : "没有以这台服务器为入口的转发。"}</Empty>}
      </Section>
      {incoming.length > 0 && (
        <Section title="指向这台服务器" count={incoming.length}>
          <RouteGrid forwards={incoming} />
        </Section>
      )}
      {state.label !== "待安装" && (
        <TrafficPanel kind="server" id={server.id} serverId={server.id}>
          <TrafficBreakdown serverId={server.id} />
        </TrafficPanel>
      )}
      {dialog === "edit" && <ServerForm server={server} onClose={() => setDialog(null)} />}
      {dialog === "install" && <InstallDialog server={server} onClose={() => setDialog(null)} />}
      {dialog === "node" && <NodeForm serverId={server.id} onClose={() => setDialog(null)} />}
      {dialog === "forward" && <ForwardForm ingressId={server.id} onClose={() => setDialog(null)} />}
      {dialog === "delete" && (
        <ConfirmDelete
          title="删除服务器"
          name={server.name}
          description={incoming.length
            ? `仍有 ${incoming.length} 条其他服务器的转发指向这台服务器。先修改或删除这些转发，才能删除服务器。`
            : `将从面板移除这台服务器、${nodes.length} 个节点和 ${outgoing.length} 条入口转发，并撤销 Agent 凭据。服务器上已运行的服务不会被停止。`}
          disabled={incoming.length > 0}
          onClose={() => setDialog(null)}
          onConfirm={async () => {
            await api.deleteServer(server.id);
            toast(`已删除 ${server.name}`);
            navigate({ page: "servers" });
          }}
        />
      )}
    </>
  );
}

function ServerFacts({ server }: { server: ApiServer }) {
  const { errors, index, now, refresh, version } = useFleet();
  const [busy, setBusy] = useState(false);
  const [resubmittedAfter, setResubmittedAfter] = useState<string>();
  const state = serverState(server, now);
  const job = index.config.get(server.id);
  const config = configurationState(job, !!errors.config, now, resubmittedAfter);
  const host = serverHost(server);
  const egress = [server.egress_ipv4 && "IPv4", server.egress_ipv6 && "IPv6"].filter(Boolean).join(" + ");
  async function resync() {
    setBusy(true);
    try {
      // The new receipt must be newer than the current one on the Panel's own clock.
      const previous = job ? Date.parse(job.created_at) + 1 : 0;
      await api.syncServer(server.id);
      setResubmittedAfter(new Date(previous).toISOString());
      toast("已重新提交当前配置");
      await refresh();
    } catch (failure) {
      toast(errorText(failure), "bad");
    } finally {
      setBusy(false);
    }
  }
  const addresses = [...new Set([host, server.ipv4_address, server.ipv6_address].filter(Boolean))] as string[];
  return (
    <div className="facts-grid">
      <div className="fact">
        <span className="fact-label">地址</span>
        {addresses.length ? addresses.map((address) => (
          <span className="copyable" key={address}><span className="mono">{address}</span><CopyButton value={address} label={`复制 ${address}`} /></span>
        )) : <span className="muted">等待 Agent 上报</span>}
      </div>
      <div className="fact">
        <span className="fact-label">Agent</span>
        <Status tone={state.tone}>{state.label}</Status>
        <span className="sub" title={timeLabel(server.last_seen_at)}>心跳 {relativeTime(server.last_seen_at, now)}</span>
        {server.runtime?.agent_version && <span className="sub">版本 {server.runtime.agent_version}</span>}
        {agentOutdated(server, version) && <span className="sub agent-outdated">与面板 {version} 不一致，重装 Agent 即可升级</span>}
      </div>
      <div className="fact">
        <span className="fact-label">出站</span>
        <span>{egress || <span className="muted">未上报</span>}</span>
      </div>
      <RuntimeFact server={server} />
      <div className={`fact fact-config tone-${config.tone}`}>
        <span className="fact-label">配置同步</span>
        <Status tone={config.tone}>{config.label}</Status>
        {config.tone !== "good" && <span className="sub">{config.detail}</span>}
        {config.tone === "good" && job?.finished_at && <span className="sub">{timeLabel(job.finished_at)}</span>}
        <button className="btn btn-sm" disabled={busy || !config.retry} onClick={resync}>
          {busy ? <Spinner size={14} /> : <RefreshCw size={14} />}重新同步
        </button>
      </div>
    </div>
  );
}

// Release builds report versions such as v0.1.0; development builds report "dev".
function agentOutdated(server: ApiServer, panelVersion: string) {
  const agent = server.runtime?.agent_version;
  return !!agent && !!panelVersion && agent !== panelVersion && agent !== "dev" && panelVersion !== "dev";
}

function RuntimeFact({ server }: { server: ApiServer }) {
  const { now } = useFleet();
  const runtime = server.runtime;
  if (!runtime) {
    return (
      <div className="fact fact-runtime">
        <span className="fact-label">核心</span>
        <span className="muted">未上报</span>
        <span className="sub">等待 Agent 上报</span>
      </div>
    );
  }
  const online = serverState(server, now).label === "在线";
  const singBox = unitState(server, singBoxUnit, now);
  const realm = runtime.units.filter((unit) => realmForwardId(unit.name));
  const realmRunning = realm.filter((unit) => unitState(server, unit.name, now)?.running).length;
  const realmTone = !online || !realm.length ? "neutral" : realmRunning === realm.length ? "good" : "bad";
  return (
    <div className="fact fact-runtime">
      <span className="fact-label">核心</span>
      <CoreLine server={server} core="sing-box" tone={singBox ? (singBox.running ? "good" : "bad") : "neutral"} detail={singBox && !singBox.running ? "未运行" : undefined} />
      <CoreLine server={server} core="realm" tone={realmTone}
        detail={realm.length ? (online ? `${realmRunning}/${realm.length} 运行中` : `${realm.length} 个实例`) : undefined} />
      <span className="sub" title={timeLabel(runtime.reported_at)}>上报 {relativeTime(runtime.reported_at, now)}</span>
    </div>
  );
}

function InstallPanel({ server }: { server: ApiServer }) {
  const [busy, setBusy] = useState(false);
  const [enrollment, setEnrollment] = useState<CreateServerResponse | null>(null);
  async function generate() {
    setBusy(true);
    try {
      setEnrollment(await api.renewEnrollment(server.id));
    } catch (failure) {
      toast(errorText(failure), "bad");
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="install-panel">
      <div>
        <h2>安装 Agent</h2>
        <p className="muted">以 root 在这台服务器上运行安装命令。Agent 会上报地址和已有节点，不接管已有服务。</p>
      </div>
      {enrollment ? <EnrollmentCommand enrollment={enrollment} /> : (
        <button className="btn btn-primary" disabled={busy} onClick={generate}>
          {busy ? <Spinner size={15} /> : <KeyRound size={15} />}生成安装命令
        </button>
      )}
    </div>
  );
}

function EnrollmentCommand({ enrollment }: { enrollment: CreateServerResponse }) {
  const { data } = useFleet();
  return (
    <div className="stack">
      {!coresReady(data.cores) && <p className="core-error">面板还没有选择 sing-box 和 Realm 版本，安装命令会失败。请先在「服务器 → 核心版本」中设置。</p>}
      <CodeBlock value={enrollment.enrollment_hint} label="复制安装命令" />
      <p className="hint">{Math.round(enrollment.expires_in_seconds / 60)} 分钟内有效，只能使用一次。</p>
    </div>
  );
}

function InstallDialog({ server, onClose }: { server: ApiServer; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [enrollment, setEnrollment] = useState<CreateServerResponse | null>(null);
  async function generate() {
    setBusy(true);
    setError("");
    try {
      setEnrollment(await api.renewEnrollment(server.id));
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog title="重装 Agent" onClose={onClose} busy={busy}>
      <div className="dialog-body">
        {enrollment ? <EnrollmentCommand enrollment={enrollment} /> : (
          <p>为「{server.name}」生成新的安装命令，用于重装或升级 Agent。之前生成且未使用的命令会失效。</p>
        )}
        <ErrorText>{error}</ErrorText>
      </div>
      <div className="dialog-footer">
        {enrollment ? <button className="btn btn-primary" onClick={onClose}>完成</button> : (
          <>
            <button className="btn" disabled={busy} onClick={onClose}>取消</button>
            <button className="btn btn-primary" disabled={busy} onClick={generate}>{busy && <Spinner size={15} />}生成命令</button>
          </>
        )}
      </div>
    </Dialog>
  );
}

export function ServerForm({ server, onClose }: { server?: ApiServer; onClose: () => void }) {
  const { refresh } = useFleet();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [enrollment, setEnrollment] = useState<CreateServerResponse | null>(null);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const text = (key: string) => String(form.get(key) || "").trim();
    setBusy(true);
    setError("");
    try {
      if (server) {
        await api.updateServer(server.id, { name: text("name"), address: text("address"), region: text("region") });
        await refresh();
        toast("已保存");
        onClose();
        return;
      }
      setEnrollment(await api.createServer({ name: text("name") }));
      await refresh();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy(false);
    }
  }
  if (enrollment) {
    return (
      <Dialog title="安装 Agent" onClose={onClose}>
        <div className="dialog-body">
          <p>以 root 在「{enrollment.server.name}」上运行：</p>
          <EnrollmentCommand enrollment={enrollment} />
        </div>
        <div className="dialog-footer">
          <button className="btn" onClick={() => { onClose(); navigate({ page: "server", id: enrollment.server.id }); }}>查看服务器</button>
          <button className="btn btn-primary" onClick={onClose}>完成</button>
        </div>
      </Dialog>
    );
  }
  return (
    <Dialog title={server ? "编辑服务器" : "添加服务器"} onClose={onClose} busy={busy}>
      <form onSubmit={submit}>
        <fieldset className="dialog-body form" disabled={busy}>
          <Field label="名称">
            <input name="name" defaultValue={server?.name} required maxLength={96} autoFocus placeholder="例如 东京 IIJ" />
          </Field>
          {server && (
            <>
              <Field label="连接地址" hint="导出节点和转发入口时使用。留空则使用 Agent 上报的公网地址。">
                <input name="address" defaultValue={server.address} placeholder="IPv4、IPv6 或域名" spellCheck={false} />
              </Field>
              <Field label="地区" hint="国家代码在前，例如 JP 东京">
                <input name="region" defaultValue={server.region} />
              </Field>
            </>
          )}
          {!server && <p className="hint">创建后生成一次性安装命令，地址和地区由 Agent 自动识别。</p>}
          <ErrorText>{error}</ErrorText>
        </fieldset>
        <div className="dialog-footer">
          <button type="button" className="btn" disabled={busy} onClick={onClose}>取消</button>
          <button type="submit" className="btn btn-primary" disabled={busy}>
            {busy && <Spinner size={15} />}
            {server ? "保存" : "创建"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
