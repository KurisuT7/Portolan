import { olderRelease } from "./agent.ts";
import type { ApiPanelUpdate } from "./api";

export const releaseNotesUrl = (version: string) => `https://github.com/KurisuT7/Portolan/releases/tag/${encodeURIComponent(version)}`;

export type PanelUpdateView =
  | { kind: "current" }
  // method: "updater" updates from the console, "docker" and "installer" need a command on the panel host,
  // "manual" is a deployment the panel does not know.
  | { kind: "available"; target: string; method: "updater" | "docker" | "installer" | "manual"; failedFrom?: string; unanswered: boolean }
  | { kind: "updating"; target: string };

export function panelUpdateView(status: ApiPanelUpdate | null): PanelUpdateView {
  if (!status) return { kind: "current" };
  const running = status.last?.state === "running" ? status.last.target : "";
  if (status.pending || running) return { kind: "updating", target: status.pending || running };
  const latest = status.latest ?? "";
  if (!olderRelease(status.current, latest)) return { kind: "current" };
  const method = status.updater ? "updater" : status.deployment === "docker" ? "docker" : status.deployment === "systemd" ? "installer" : "manual";
  const failed = status.last?.state === "failed" && status.last.target === latest;
  return { kind: "available", target: latest, method, failedFrom: failed ? status.last?.from : undefined, unanswered: !!status.unanswered };
}

// What became of an update this browser started, judged from the panel after it came back.
export function updateOutcome(target: string, status: ApiPanelUpdate): "succeeded" | "failed" | "running" | "unknown" {
  if (status.current === target) return "succeeded";
  if (status.pending === target || (status.last?.target === target && status.last.state === "running")) return "running";
  if (status.last?.target === target && status.last.state === "failed") return "failed";
  return "unknown";
}

// The image tag for a release: v0.3.0 is published as ghcr.io/kurisut7/portolan:0.3.0.
export const imageTag = (version: string) => version.replace(/^v/, "");
