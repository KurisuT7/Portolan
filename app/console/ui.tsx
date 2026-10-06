"use client";

import { useEffect, useId, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { ArrowLeft, Check, Copy, LoaderCircle, Search, X } from "lucide-react";
import type { ApiForwardProbeHistoryPoint, ApiNode } from "../lib/api";
import { protocolShort } from "../lib/nodes";
import { hasLatency } from "../lib/quality";
import type { StatusTone } from "../lib/status";
import { errorText } from "./data";

export type Tone = StatusTone;

export function Status({ tone, children, title }: { tone: Tone; children: ReactNode; title?: string }) {
  return (
    <span className={`status tone-${tone}`} title={title}>
      <i aria-hidden="true" />
      {children}
    </span>
  );
}

export function Badge({ tone = "neutral", children }: { tone?: Tone; children: ReactNode }) {
  return <span className={`badge tone-${tone}`}>{children}</span>;
}

export function ProtocolTag({ protocol, port }: { protocol: ApiNode["protocol"]; port?: number }) {
  return (
    <span className="ptag" data-protocol={protocol}>
      {protocolShort[protocol]}
      {port != null && <b>{port}</b>}
    </span>
  );
}

export function Spinner({ size = 16 }: { size?: number }) {
  return <LoaderCircle className="spin" size={size} aria-hidden="true" />;
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function ErrorText({ children }: { children?: ReactNode }) {
  return children ? <p className="error-text" role="alert">{children}</p> : null;
}

export function Field({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  );
}

export function SearchInput({ value, onChange, placeholder }: { value: string; onChange: (value: string) => void; placeholder: string }) {
  return (
    <label className="search">
      <Search size={15} aria-hidden="true" />
      <input type="search" aria-label={placeholder} placeholder={placeholder} value={value} onChange={(event) => onChange(event.target.value)} />
    </label>
  );
}

export function Segmented<T extends string>({ label, value, options, onChange }: {
  label: string;
  value: T;
  options: ReadonlyArray<{ value: T; label: ReactNode }>;
  onChange: (value: T) => void;
}) {
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((option) => (
        <button type="button" key={option.value} aria-pressed={value === option.value} onClick={() => onChange(option.value)}>
          {option.label}
        </button>
      ))}
    </div>
  );
}

export function PageHeader({ title, status, meta, back, actions }: {
  title: ReactNode;
  status?: ReactNode;
  meta?: ReactNode;
  back?: { href: string; label: string };
  actions?: ReactNode;
}) {
  return (
    <header className="page-header">
      {back && (
        <a className="back-link" href={back.href}>
          <ArrowLeft size={15} aria-hidden="true" />
          {back.label}
        </a>
      )}
      <div className="page-title-row">
        <div className="page-title">
          <div className="page-title-line">
            <h1>{title}</h1>
            {status}
          </div>
          {meta && <p className="page-meta">{meta}</p>}
        </div>
        {actions && <div className="page-actions">{actions}</div>}
      </div>
    </header>
  );
}

export function Section({ title, count, actions, children }: { title: string; count?: number; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="section">
      <div className="section-header">
        <h2>
          {title}
          {count != null && <span className="count">{count}</span>}
        </h2>
        {actions}
      </div>
      {children}
    </section>
  );
}

export function Dialog({ title, onClose, busy = false, wide = false, children }: {
  title: string;
  onClose: () => void;
  busy?: boolean;
  wide?: boolean;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  useEffect(() => {
    const dialog = ref.current!;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.showModal();
    return () => {
      dialog.close();
      opener?.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      className={`dialog${wide ? " dialog-wide" : ""}`}
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault();
        if (!busy) onClose();
      }}
    >
      <div className="dialog-header">
        <h2 id={titleId}>{title}</h2>
        <button type="button" className="icon-btn" aria-label="关闭" disabled={busy} onClick={onClose}>
          <X size={18} />
        </button>
      </div>
      {children}
    </dialog>
  );
}

export function ConfirmDelete({ title, name, description, disabled = false, onConfirm, onClose }: {
  title: string;
  name: string;
  description: ReactNode;
  disabled?: boolean;
  onConfirm: () => Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function confirm() {
    setBusy(true);
    setError("");
    try {
      await onConfirm();
      onClose();
    } catch (failure) {
      setError(errorText(failure));
      setBusy(false);
    }
  }
  return (
    <Dialog title={title} onClose={onClose} busy={busy}>
      <div className="dialog-body">
        <p>
          删除「<strong>{name}</strong>」？
        </p>
        <p className="muted">{description}</p>
        <ErrorText>{error}</ErrorText>
      </div>
      <div className="dialog-footer">
        <button type="button" className="btn" disabled={busy} onClick={onClose}>取消</button>
        <button type="button" className="btn btn-danger" disabled={busy || disabled} onClick={confirm}>
          {busy && <Spinner size={15} />}
          删除
        </button>
      </div>
    </Dialog>
  );
}

// Passing a promise keeps Safari's user-activation requirement satisfied while the value is still loading.
export async function copyText(value: string | Promise<string>) {
  if (typeof value !== "string" && typeof ClipboardItem !== "undefined" && navigator.clipboard?.write) {
    await navigator.clipboard.write([new ClipboardItem({ "text/plain": value.then((text) => new Blob([text], { type: "text/plain" })) })]);
    return;
  }
  await navigator.clipboard.writeText(await value);
}

export function useCopied() {
  const [copied, setCopied] = useState("");
  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(""), 1600);
    return () => clearTimeout(timer);
  }, [copied]);
  return [copied, setCopied] as const;
}

export function CopyButton({ value, label, children, className }: { value: string; label: string; children?: ReactNode; className?: string }) {
  const [copied, setCopied] = useCopied();
  async function copy() {
    try {
      await copyText(value);
      setCopied(value);
    } catch {
      toast("浏览器未允许访问剪贴板，请手动选中后复制", "bad");
    }
  }
  const Icon = copied ? Check : Copy;
  return (
    <button type="button" className={className ?? (children ? "btn" : "icon-btn icon-btn-sm")} aria-label={label} title={label} onClick={copy}>
      <Icon size={children ? 15 : 14} aria-hidden="true" />
      {children && (copied ? "已复制" : children)}
    </button>
  );
}

export function CodeBlock({ value, label }: { value: string; label: string }) {
  return (
    <div className="code-block">
      <pre tabIndex={0}>{value}</pre>
      <CopyButton value={value} label={label}>复制</CopyButton>
    </div>
  );
}

export function Sparkline({ points, tone, height = 44 }: { points: ApiForwardProbeHistoryPoint[]; tone: Tone; height?: number }) {
  const width = 300;
  const measured = points.map((point, index) => ({ index, point })).filter(({ point }) => hasLatency(point));
  if (measured.length < 2) return <div className="spark spark-empty" style={{ height }} />;
  const values = measured.map(({ point }) => point.latency);
  const max = Math.max(...values) * 1.1;
  const min = Math.min(...values) * 0.85;
  const x = (index: number) => (index / Math.max(1, points.length - 1)) * width;
  const y = (value: number) => height - 2 - ((value - min) / Math.max(1, max - min)) * (height - 6);
  const line = measured.map(({ index, point }, position) => `${position ? "L" : "M"}${x(index).toFixed(1)},${y(point.latency).toFixed(1)}`).join(" ");
  const area = `${line} L${x(measured.at(-1)!.index).toFixed(1)},${height} L${x(measured[0].index).toFixed(1)},${height} Z`;
  const gradient = `spark-${tone}`;
  return (
    <svg className={`spark tone-${tone}`} viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" style={{ height }} aria-hidden="true">
      <defs>
        <linearGradient id={gradient} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0" stopColor="currentColor" stopOpacity="0.28" />
          <stop offset="1" stopColor="currentColor" stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={area} fill={`url(#${gradient})`} />
      <path d={line} fill="none" stroke="currentColor" strokeWidth="1.6" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

type ToastItem = { id: number; text: string; tone: "good" | "bad" };
const noToasts: ToastItem[] = [];
let toasts = noToasts;
let toastSequence = 0;
const toastListeners = new Set<() => void>();

function publishToasts(next: ToastItem[]) {
  toasts = next;
  toastListeners.forEach((listener) => listener());
}

export function toast(text: string, tone: ToastItem["tone"] = "good") {
  const item = { id: ++toastSequence, text, tone };
  publishToasts([...toasts.slice(-2), item]);
  setTimeout(() => publishToasts(toasts.filter((entry) => entry !== item)), tone === "bad" ? 6000 : 2600);
}

function subscribeToasts(listener: () => void) {
  toastListeners.add(listener);
  return () => toastListeners.delete(listener);
}

export function Toaster() {
  const items = useSyncExternalStore(subscribeToasts, () => toasts, () => noToasts);
  return (
    <div className="toasts" aria-live="polite">
      {items.map((item) => (
        <div key={item.id} className={`toast tone-${item.tone}`} role={item.tone === "bad" ? "alert" : "status"}>
          {item.tone === "good" ? <Check size={15} aria-hidden="true" /> : <X size={15} aria-hidden="true" />}
          {item.text}
        </div>
      ))}
    </div>
  );
}
