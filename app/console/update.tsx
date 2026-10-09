"use client";

import { useCallback, useEffect, useState } from "react";
import { ArrowUpCircle } from "lucide-react";
import { ApiError, type ApiPanelUpdate } from "../lib/api";
import { imageTag, panelUpdateView, releaseNotesUrl, updateOutcome, type PanelUpdateView } from "../lib/update";
import { api, errorText } from "./data";
import { CodeBlock, Dialog, ErrorText, Spinner, toast } from "./ui";

// Remembers the release this tab asked for, so the outcome can be shown after the restart signs everyone out.
const storageKey = "portolan.panel-update";
const checkEvery = 30 * 60_000;
const pollEvery = 2_000;
const pollLimit = 15 * 60_000;

function rememberedTarget() {
  try {
    return sessionStorage.getItem(storageKey) ?? "";
  } catch {
    return "";
  }
}

function rememberTarget(target: string) {
  try {
    if (target) sessionStorage.setItem(storageKey, target);
    else sessionStorage.removeItem(storageKey);
  } catch { /* the outcome toast is a convenience */ }
}

// installing: the panel answers while the updater downloads and installs; restarting: the panel does not answer.
export type UpdatePhase = "installing" | "restarting" | "reloading";

export function usePanelUpdate() {
  const [status, setStatus] = useState<ApiPanelUpdate | null>(null);
  const [watching, setWatching] = useState("");
  const [phase, setPhase] = useState<UpdatePhase>("installing");

  const load = useCallback(async (signal?: AbortSignal) => {
    const next = await api.panelUpdate(signal);
    setStatus(next);
    return next;
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    async function check(first: boolean) {
      try {
        const next = await load(controller.signal);
        const target = rememberedTarget();
        if (!first || !target) return;
        const outcome = updateOutcome(target, next);
        if (outcome === "running") return setWatching(target);
        rememberTarget("");
        if (outcome === "succeeded") toast(`面板已更新到 ${target}`);
        if (outcome === "failed") toast(`更新到 ${target} 没有成功，面板仍是 ${next.current}`, "bad");
      } catch { /* the next check retries; a missing update notice does not block the console */ }
    }
    void check(true);
    const timer = setInterval(() => void check(false), checkEvery);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [load]);

  // An update started here, or one the panel reports, possibly from another tab.
  const target = watching || status?.pending || (status?.last?.state === "running" ? status.last.target : "");

  useEffect(() => {
    if (!target) return;
    let timer = 0;
    let stopped = false;
    const deadline = Date.now() + pollLimit;
    async function tick() {
      try {
        const { version } = await api.health();
        if (version === target) {
          setPhase("reloading");
          window.location.reload();
          return;
        }
        const next = await load();
        setPhase("installing");
        const outcome = updateOutcome(target, next);
        if (outcome !== "running") {
          rememberTarget("");
          setWatching("");
          toast(`更新到 ${target} 没有成功，面板仍是 ${next.current}`, "bad");
          return;
        }
      } catch (failure) {
        // A panel that restarted has no sessions; after signing in again the outcome is shown.
        if (failure instanceof ApiError && failure.status === 401) {
          window.location.reload();
          return;
        }
        setPhase("restarting");
      }
      if (stopped) return;
      if (Date.now() > deadline) {
        setWatching("");
        toast("更新没有在 15 分钟内完成，请在面板服务器上查看 journalctl -u portolan-panel-update", "bad");
        return;
      }
      timer = window.setTimeout(tick, pollEvery);
    }
    timer = window.setTimeout(tick, pollEvery);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [target, load]);

  async function start(version: string) {
    const { pending } = await api.startPanelUpdate(version);
    rememberTarget(pending);
    setPhase("installing");
    setWatching(pending);
    await load().catch(() => undefined);
  }

  const view: PanelUpdateView = target ? { kind: "updating", target } : panelUpdateView(status);
  return { status, view, phase, start };
}

export type PanelUpdateControl = ReturnType<typeof usePanelUpdate>;

export function PanelUpdateButton({ view, onClick }: { view: PanelUpdateView; onClick: () => void }) {
  if (view.kind === "current") return null;
  const updating = view.kind === "updating";
  return (
    <button
      className="update-trigger"
      onClick={onClick}
      aria-label={updating ? `面板正在更新到 ${view.target}` : `面板有新版本 ${view.target}`}
      title={updating ? "面板正在更新" : "面板有新版本"}
    >
      {updating ? <Spinner size={14} /> : <ArrowUpCircle size={15} aria-hidden="true" />}
      <span>{view.target}</span>
    </button>
  );
}

const phaseText: Record<UpdatePhase, string> = {
  installing: "正在下载、校验并安装新版本",
  restarting: "面板正在重启，页面暂时连不上",
  reloading: "已更新，正在重新加载",
};

export function PanelUpdateDialog({ control, onClose }: { control: PanelUpdateControl; onClose: () => void }) {
  const { status, view, phase } = control;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  if (view.kind === "current" || !status) return null;
  const current = status.current;
  async function start(version: string) {
    setBusy(true);
    setError("");
    try {
      await control.start(version);
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy(false);
    }
  }
  const notes = <a className="text-link" href={releaseNotesUrl(view.target)} target="_blank" rel="noreferrer">更新说明</a>;
  return (
    <Dialog title="更新面板" onClose={onClose} busy={busy}>
      <div className="dialog-body">
        <div className="update-versions">
          <span className="mono">{current}</span>
          <span aria-hidden="true">→</span>
          <strong className="mono">{view.target}</strong>
          {notes}
        </div>
        {view.kind === "updating" ? (
          <>
            <p className="update-progress" role="status"><Spinner size={15} />{phaseText[phase]}</p>
            <p className="muted">完成后页面会自动刷新，需要重新登录。关闭这个窗口不影响更新。</p>
          </>
        ) : view.method === "updater" ? (
          <>
            <p>面板会下载新版本，按发布的校验和核对后安装并重启。重启期间页面会断开半分钟左右，之后需要重新登录。节点和转发不受影响；新版本没有正常启动时会自动换回 {current}。</p>
            {view.failedFrom && <ErrorText>上次更新到 {view.target} 没有成功，面板仍是 {current}。原因可以在面板服务器上用 journalctl -u portolan-panel-update 查看。</ErrorText>}
            {view.unanswered && <ErrorText>更新服务没有响应。在面板服务器上运行 systemctl status portolan-panel-update.path 检查。</ErrorText>}
          </>
        ) : view.method === "docker" ? (
          <>
            <p>在面板主机上把 <code>compose.yaml</code> 里的镜像改为 <code>ghcr.io/kurisut7/portolan:{imageTag(view.target)}</code>，然后在同一目录运行：</p>
            <CodeBlock value="docker compose up -d" label="复制命令" />
          </>
        ) : view.method === "installer" ? (
          <>
            <p>在面板服务器上以 root 运行：</p>
            <CodeBlock value={"curl -fsSLO https://github.com/KurisuT7/Portolan/releases/latest/download/install-panel.sh\nsudo sh install-panel.sh"} label="复制命令" />
          </>
        ) : (
          <p>按部署面板时的方式升级，步骤见{notes}。</p>
        )}
        <ErrorText>{error}</ErrorText>
      </div>
      <div className="dialog-footer">
        <button type="button" className="btn" disabled={busy} onClick={onClose}>{view.kind === "available" && view.method === "updater" ? "取消" : "关闭"}</button>
        {view.kind === "available" && view.method === "updater" && (
          <button type="button" className="btn btn-primary" disabled={busy} onClick={() => void start(view.target)}>
            {busy && <Spinner size={15} />}
            {view.failedFrom ? "重试" : `更新到 ${view.target}`}
          </button>
        )}
      </div>
    </Dialog>
  );
}
