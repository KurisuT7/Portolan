"use client";

import { useEffect, useMemo, useState, type FormEvent } from "react";
import { CircleAlert, LogOut, RefreshCw, Search, Shield, ShieldCheck } from "lucide-react";
import { ApiError, type TotpSetup } from "../lib/api";
import { buildIndex } from "../lib/fleet";
import { clockLabel } from "../lib/format";
import { routeHref, type Route } from "../lib/routing";
import { api, errorText, FleetProvider, resourceNames, useConsole, type ConsoleControl, type Resource } from "./data";
import { HistoryProvider } from "./history";
import { useRoute } from "./hooks";
import { CodeBlock, Dialog, ErrorText, Field, Spinner, toast, Toaster } from "./ui";
import { OverviewPage } from "./overview";
import { ServerForm, ServerPage, ServersPage } from "./servers";
import { NodeForm, NodesPage } from "./nodes";
import { ForwardForm, ForwardsPage } from "./forwards";
import { ForwardPage } from "./forward";
import { CommandPalette, type PaletteAction } from "./palette";
import { ThemeMenu, useThemeSync } from "./theme";

const tabs: Array<{ label: string; route: Route; pages: Route["page"][] }> = [
  { label: "总览", route: { page: "overview" }, pages: ["overview"] },
  { label: "服务器", route: { page: "servers" }, pages: ["servers", "server"] },
  { label: "节点", route: { page: "nodes" }, pages: ["nodes"] },
  { label: "转发", route: { page: "forwards" }, pages: ["forwards", "forward"] },
];

export default function Console() {
  const control = useConsole();
  useThemeSync();
  if (control.session !== "ready") return <Gate control={control} />;
  return <Shell control={control} />;
}

function Logo() {
  return (
    <svg className="logo" viewBox="0 0 24 24" aria-hidden="true">
      <rect width="24" height="24" rx="7" />
      <path d="M5.5 12h3.5m0 0 8-5m-8 5h9.5M9 12l8 5" />
      <circle cx="9" cy="12" r="1.7" />
    </svg>
  );
}

function Gate({ control }: { control: ConsoleControl }) {
  const [retrying, setRetrying] = useState(false);
  async function retry() {
    setRetrying(true);
    await control.connect();
    setRetrying(false);
  }
  return (
    <div className="gate">
      <div className="gate-card">
        <div className="brand"><Logo />Portolan</div>
        {control.session === "login" && <Login login={control.login} />}
        {control.session === "error" && (
          <div className="gate-form">
            <div>
              <h1>无法连接面板</h1>
              <p className="muted">{control.connectionError}</p>
            </div>
            <button className="btn btn-primary btn-block" disabled={retrying} onClick={retry}>
              {retrying && <Spinner size={15} />}
              重新连接
            </button>
          </div>
        )}
        {control.session === "checking" && (
          <p className="gate-loading" role="status"><Spinner />正在连接面板…</p>
        )}
      </div>
    </div>
  );
}

function waitLabel(seconds: number) {
  return seconds >= 60 ? `${Math.ceil(seconds / 60)} 分钟` : `${Math.max(seconds, 1)} 秒`;
}

function securityError(failure: unknown) {
  if (!(failure instanceof ApiError)) return errorText(failure);
  if (failure.code === "login_locked") return `失败次数过多，请 ${waitLabel(failure.retryAfter)}后再试`;
  if (failure.code === "totp_locked") return `验证码错误次数过多，请 ${waitLabel(failure.retryAfter)}后再试`;
  if (failure.code === "totp_invalid") return "验证码不正确或已经用过";
  if (failure.code === "totp_setup_expired") return "设置已超时，请重新开始";
  if (failure.status === 401) return "管理员令牌不正确";
  return errorText(failure);
}

function CodeInput() {
  return (
    <input name="code" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9 ]{6,7}" maxLength={7} required autoFocus />
  );
}

function enteredCode(form: HTMLFormElement) {
  return String(new FormData(form).get("code") || "").replace(/\s/g, "");
}

function Login({ login }: { login: (token: string, code?: string) => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // Kept after the panel asks for a two-step verification code.
  const [token, setToken] = useState("");
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const entered = token || String(new FormData(event.currentTarget).get("token") || "");
    setBusy(true);
    setError("");
    try {
      await login(entered, token ? enteredCode(event.currentTarget) : "");
    } catch (failure) {
      if (failure instanceof ApiError && failure.code === "totp_required") setToken(entered);
      else setError(securityError(failure));
      setBusy(false);
    }
  }
  return (
    <form className="gate-form" onSubmit={submit}>
      <h1>{token ? "两步验证" : "登录"}</h1>
      {token ? (
        <Field label="验证码" hint="验证器应用里显示的 6 位数字">
          <CodeInput />
        </Field>
      ) : (
        <label className="field">
          <span className="field-label">管理员令牌</span>
          <input name="token" type="password" autoComplete="current-password" required autoFocus />
        </label>
      )}
      <ErrorText>{error}</ErrorText>
      <button className="btn btn-primary btn-block" type="submit" disabled={busy}>
        {busy && <Spinner size={15} />}
        {token ? "验证" : "登录"}
      </button>
      {token && (
        <button type="button" className="btn btn-block" disabled={busy} onClick={() => { setToken(""); setError(""); }}>
          重新输入令牌
        </button>
      )}
    </form>
  );
}

function QrCode({ matrix, label }: { matrix: TotpSetup["qr"]; label: string }) {
  const quiet = 4;
  const size = matrix.size + quiet * 2;
  let modules = "";
  matrix.rows.forEach((row, y) => {
    for (let x = 0; x < row.length; x++) if (row[x] === "1") modules += `M${x + quiet} ${y + quiet}h1v1h-1z`;
  });
  return (
    <svg className="qr" viewBox={`0 0 ${size} ${size}`} role="img" aria-label={label} shapeRendering="crispEdges">
      <rect width={size} height={size} fill="#fff" />
      <path d={modules} fill="#000" />
    </svg>
  );
}

function SecurityDialog({ control, onClose }: { control: ConsoleControl; onClose: () => void }) {
  const enabled = !!control.info?.totp_enabled;
  const [setup, setSetup] = useState<TotpSetup | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function start() {
    setBusy(true);
    setError("");
    try {
      setSetup(await api.setupTotp());
    } catch (failure) {
      setError(securityError(failure));
    } finally {
      setBusy(false);
    }
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const code = enteredCode(event.currentTarget);
    setBusy(true);
    setError("");
    try {
      if (enabled) await api.disableTotp(code);
      else await api.enableTotp(code);
      await control.reloadSession();
      toast(enabled ? "已关闭两步验证" : "已开启两步验证，其他已登录的会话已退出");
      onClose();
    } catch (failure) {
      if (failure instanceof ApiError && failure.code === "totp_setup_expired") setSetup(null);
      setError(securityError(failure));
      setBusy(false);
    }
  }
  return (
    <Dialog title="两步验证" onClose={onClose} busy={busy}>
      <form onSubmit={submit}>
        <div className="dialog-body">
          {enabled ? (
            <p>已开启。登录时需要管理员令牌和验证器应用里的验证码。输入当前验证码即可关闭。</p>
          ) : setup ? (
            <>
              <p>用验证器应用扫描二维码，然后输入应用显示的 6 位验证码。</p>
              <QrCode matrix={setup.qr} label="两步验证二维码" />
              <div className="field">
                <span className="field-label">无法扫码时，在应用里手动输入密钥</span>
                <CodeBlock value={setup.secret} label="复制密钥" />
              </div>
            </>
          ) : (
            <p>
              开启后，登录时除了管理员令牌，还要输入验证器应用（如 Google Authenticator、Microsoft Authenticator、1Password）里的 6 位验证码。
              丢失验证器时，在面板服务器上运行 <code>portolan-panel disable-totp</code> 关闭。
            </p>
          )}
          {(enabled || setup) && (
            <Field label="验证码">
              <CodeInput />
            </Field>
          )}
          <ErrorText>{error}</ErrorText>
        </div>
        <div className="dialog-footer">
          <button type="button" className="btn" disabled={busy} onClick={onClose}>取消</button>
          {enabled || setup ? (
            <button type="submit" className={`btn ${enabled ? "btn-danger" : "btn-primary"}`} disabled={busy}>
              {busy && <Spinner size={15} />}
              {enabled ? "关闭两步验证" : "确认开启"}
            </button>
          ) : (
            <button type="button" className="btn btn-primary" disabled={busy} onClick={start}>
              {busy && <Spinner size={15} />}
              开始设置
            </button>
          )}
        </div>
      </form>
    </Dialog>
  );
}

function Shell({ control }: { control: ConsoleControl }) {
  const route = useRoute();
  const { data, errors, fetchedAt, refresh } = control;
  const index = useMemo(() => buildIndex(data), [data]);
  const version = control.info?.version ?? "";
  const fleet = useMemo(() => ({ data, index, errors, now: fetchedAt, refresh, version }), [data, index, errors, fetchedAt, refresh, version]);
  const [palette, setPalette] = useState(false);
  const [security, setSecurity] = useState(false);
  const [creating, setCreating] = useState<PaletteAction | null>(null);
  const pageKey = "id" in route ? `${route.page}:${route.id}` : route.page;
  useEffect(() => {
    window.scrollTo(0, 0);
  }, [pageKey]);
  useEffect(() => {
    function shortcut(event: KeyboardEvent) {
      if (event.key.toLowerCase() === "k" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        setPalette((open) => !open);
      }
    }
    window.addEventListener("keydown", shortcut);
    return () => window.removeEventListener("keydown", shortcut);
  }, []);
  return (
    <FleetProvider value={fleet}>
      <HistoryProvider forwards={data.forwards}>
        <div className="app">
          <Header route={route} control={control} onSearch={() => setPalette(true)} onSecurity={() => setSecurity(true)} />
          <main className="main">
            <ConnectionNotice control={control} />
            {route.page === "overview" && <OverviewPage />}
            {route.page === "servers" && <ServersPage />}
            {route.page === "server" && <ServerPage key={route.id} id={route.id} />}
            {route.page === "nodes" && <NodesPage server={route.server} />}
            {route.page === "forwards" && <ForwardsPage server={route.server} />}
            {route.page === "forward" && <ForwardPage key={route.id} id={route.id} />}
          </main>
          <footer className="footer">
            <span>Portolan {version}</span>
            {control.info?.geoip_provider === "dbip" && <a href="https://db-ip.com" target="_blank" rel="noreferrer">IP Geolocation by DB-IP</a>}
          </footer>
          {security && <SecurityDialog control={control} onClose={() => setSecurity(false)} />}
          {palette && <CommandPalette onClose={() => setPalette(false)} onAction={setCreating} />}
          {creating === "server" && <ServerForm onClose={() => setCreating(null)} />}
          {creating === "node" && data.servers.length > 0 && <NodeForm onClose={() => setCreating(null)} />}
          {creating === "forward" && data.servers.length > 0 && <ForwardForm onClose={() => setCreating(null)} />}
          <Toaster />
        </div>
      </HistoryProvider>
    </FleetProvider>
  );
}

function Header({ route, control, onSearch, onSecurity }: { route: Route; control: ConsoleControl; onSearch: () => void; onSecurity: () => void }) {
  const failures = Object.keys(control.errors).length;
  const tone = failures === Object.keys(resourceNames).length ? "bad" : failures ? "warn" : "good";
  const syncLabel = tone === "bad" ? "连接中断" : tone === "warn" ? "部分未更新" : control.syncedAt ? clockLabel(control.syncedAt) : "同步中";
  async function logout() {
    try {
      await control.logout();
    } catch (failure) {
      toast(errorText(failure), "bad");
    }
  }
  return (
    <header className="topbar">
      <div className="topbar-inner">
        <a className="brand" href="#/"><Logo />Portolan</a>
        <nav className="tabs" aria-label="主导航">
          {tabs.map((tab) => (
            <a key={tab.label} href={routeHref(tab.route)} aria-current={tab.pages.includes(route.page) ? "page" : undefined}>
              {tab.label}
            </a>
          ))}
        </nav>
        <div className="topbar-actions">
          <button className="search-trigger" onClick={onSearch} aria-label="搜索与命令">
            <Search size={15} aria-hidden="true" />
            <span>搜索</span>
            <kbd>⌘K</kbd>
          </button>
          <button
            className={`sync tone-${tone}`}
            disabled={control.refreshing}
            onClick={() => void control.refresh()}
            title={control.syncedAt ? `最近同步 ${clockLabel(control.syncedAt)}，点击刷新` : "刷新"}
            aria-label="刷新数据"
          >
            <i aria-hidden="true" />
            <span>{syncLabel}</span>
            <RefreshCw size={13} className={control.refreshing ? "spin" : ""} aria-hidden="true" />
          </button>
          <ThemeMenu />
          <button className="icon-btn" onClick={onSecurity} aria-label="两步验证" title={control.info?.totp_enabled ? "两步验证已开启" : "两步验证未开启"}>
            {control.info?.totp_enabled ? <ShieldCheck size={17} /> : <Shield size={17} />}
          </button>
          <button className="icon-btn" onClick={logout} aria-label="退出登录" title="退出登录">
            <LogOut size={17} />
          </button>
        </div>
      </div>
    </header>
  );
}

function ConnectionNotice({ control }: { control: ConsoleControl }) {
  const failures = Object.entries(control.errors) as Array<[Resource, string]>;
  if (!failures.length) return null;
  const all = failures.length === Object.keys(resourceNames).length;
  return (
    <div className={`notice tone-${all ? "bad" : "warn"}`} role="alert">
      <CircleAlert size={17} aria-hidden="true" />
      <div className="notice-body">
        <strong>{all ? "无法读取控制面数据" : `${failures.map(([key]) => resourceNames[key]).join("、")}未更新`}</strong>
        <span>显示的是上次成功读取的内容，不代表当前状态。</span>
        <details>
          <summary>详情</summary>
          <ul>
            {failures.map(([key, message]) => (
              <li key={key}>
                {resourceNames[key]}：{message}
                {control.lastSuccess[key] ? ` · 上次成功 ${clockLabel(control.lastSuccess[key]!)}` : " · 尚未成功读取"}
              </li>
            ))}
          </ul>
        </details>
      </div>
      <button className="btn btn-sm" disabled={control.refreshing} onClick={() => void control.refresh()}>重试</button>
    </div>
  );
}
