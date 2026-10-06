"use client";

import { useEffect, useState } from "react";
import { ArrowUpCircle, RefreshCw } from "lucide-react";
import type { ApiCoreRelease, ApiServer, CoreName } from "../lib/api";
import { coreLabels, coreNames, coreSummary, coreTarget, serverCoreState } from "../lib/cores";
import { api, errorText, useFleet } from "./data";
import { Dialog, ErrorText, Spinner, Status, toast, type Tone } from "./ui";

export function CoreDialog({ onClose }: { onClose: () => void }) {
  return (
    <Dialog title="核心版本" onClose={onClose}>
      <div className="dialog-body">
        {coreNames.map((core) => <CoreTargetRow key={core} core={core} />)}
        <p className="hint">
          新装和重装的服务器使用目标版本。更新时 Agent 先用新的 sing-box 校验当前配置（Realm 没有校验命令），重启后服务没有正常运行就自动换回原版本。
          更新 sing-box 会短暂中断该服务器所有节点的连接，更新 Realm 会重启它的所有 Realm 转发。建议先更新一台试用。
        </p>
      </div>
      <div className="dialog-footer">
        <button className="btn btn-primary" onClick={onClose}>完成</button>
      </div>
    </Dialog>
  );
}

function CoreTargetRow({ core }: { core: CoreName }) {
  const { data, errors, refresh } = useFleet();
  const target = coreTarget(data.cores, core)?.version;
  const [releases, setReleases] = useState<ApiCoreRelease[] | null>(null);
  const [selected, setSelected] = useState("");
  const [busy, setBusy] = useState<"" | "target" | "rollout">("");
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState("");
  const summary = coreSummary(data.servers, core, data.cores);
  const label = coreLabels[core];
  useEffect(() => {
    api.coreReleases(core).then(setReleases).catch((failure) => {
      setReleases([]);
      setError(errorText(failure));
    });
  }, [core]);
  const versions = [...new Set([...(releases ?? []).map((release) => release.version), ...(target ? [target] : [])])];
  const choice = selected || target || versions[0] || "";

  async function saveTarget() {
    setBusy("target");
    setError("");
    try {
      await api.setCoreTarget(core, choice);
      toast(`${label} 目标版本已设为 ${choice}`);
      await refresh();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  }

  async function rollout() {
    setBusy("rollout");
    setError("");
    try {
      const { queued } = await api.rolloutCore(core);
      toast(`已向 ${queued} 台服务器下发 ${label} 更新`);
      setConfirming(false);
      await refresh();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  }

  return (
    <section className="core-row">
      <div className="core-row-head">
        <b>{label}</b>
        <span className="sub">{target ? `目标 ${target}` : "尚未选择目标版本"}</span>
      </div>
      <div className="core-row-controls">
        <select aria-label={`${label} 版本`} value={choice} disabled={!versions.length || !!busy} onChange={(event) => setSelected(event.target.value)}>
          {!versions.length && <option value="">{releases ? "没有可用版本" : "正在读取 GitHub 版本…"}</option>}
          {versions.map((version) => <option key={version} value={version}>{version}{version === releases?.[0]?.version ? " · 最新" : ""}</option>)}
        </select>
        <button className="btn btn-sm" disabled={!choice || choice === target || !!busy || !!errors.cores} onClick={saveTarget}>
          {busy === "target" && <Spinner size={14} />}{busy === "target" ? "下载并校验中" : "设为目标"}
        </button>
      </div>
      {target && (
        <div className="core-row-foot">
          <span className="sub">
            {summary.current}/{summary.total} 台已是 {target}
            {summary.updating > 0 && ` · ${summary.updating} 台更新中`}
            {summary.unreported > 0 && ` · ${summary.unreported} 台尚未上报`}
          </span>
          {confirming ? (
            <span className="core-line">
              <button className="btn btn-sm" disabled={!!busy} onClick={() => setConfirming(false)}>取消</button>
              <button className="btn btn-sm btn-primary" disabled={!!busy} onClick={rollout}>
                {busy === "rollout" && <Spinner size={14} />}确认更新 {summary.outdated} 台
              </button>
            </span>
          ) : (
            <button className="btn btn-sm" disabled={!summary.outdated || !!busy} onClick={() => setConfirming(true)}>
              <ArrowUpCircle size={14} />全部更新
            </button>
          )}
        </div>
      )}
      <ErrorText>{error}</ErrorText>
    </section>
  );
}

// One core of a server: installed version, service state and update action.
export function CoreLine({ server, core, tone, detail }: { server: ApiServer; core: CoreName; tone: Tone; detail?: string }) {
  const { data, errors, refresh } = useFleet();
  const [busy, setBusy] = useState(false);
  const state = serverCoreState(server, core, data.cores);
  const installed = state.kind === "unreported" ? "" : state.installed;
  async function update() {
    setBusy(true);
    try {
      await api.updateServerCore(server.id, core);
      toast(`已向 ${server.name} 下发 ${coreLabels[core]} 更新`);
      await refresh();
    } catch (failure) {
      toast(errorText(failure), "bad");
    } finally {
      setBusy(false);
    }
  }
  const action = state.kind === "outdated" || state.kind === "failed";
  return (
    <>
      <div className="core-line">
        <Status tone={state.kind === "updating" ? "warn" : tone}>
          {coreLabels[core]} {installed || "版本未知"}
          {detail && ` · ${detail}`}
          {state.kind === "updating" && (state.pending ? " · 等待更新" : " · 更新中")}
        </Status>
        {action && (
          <button className="btn btn-sm" disabled={busy || !!errors.cores} onClick={update}>
            {busy ? <Spinner size={14} /> : state.kind === "failed" ? <RefreshCw size={14} /> : <ArrowUpCircle size={14} />}
            {state.kind === "failed" ? "重试" : `更新到 ${state.target}`}
          </button>
        )}
      </div>
      {state.kind === "failed" && <span className="core-error">{state.detail}</span>}
    </>
  );
}
