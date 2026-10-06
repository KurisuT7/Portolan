import type { ApiCores, ApiJob, ApiServer, CoreName } from "./api";

export const coreNames: CoreName[] = ["sing-box", "realm"];
export const coreLabels: Record<CoreName, string> = { "sing-box": "sing-box", realm: "Realm" };

export function coreTarget(cores: ApiCores, core: CoreName) {
  return cores.targets.find((target) => target.core === core);
}

export function coresReady(cores: ApiCores) {
  return coreNames.every((core) => coreTarget(cores, core));
}

export function installedCore(server: ApiServer, core: CoreName) {
  return (core === "realm" ? server.runtime?.realm_version : server.runtime?.sing_box_version) || "";
}

export type CoreState =
  | { kind: "unreported" }
  | { kind: "untargeted"; installed: string }
  | { kind: "current"; installed: string }
  | { kind: "outdated"; installed: string; target: string }
  | { kind: "updating"; installed: string; pending: boolean }
  | { kind: "failed"; installed: string; target: string; detail: string };

function failureDetail(job: ApiJob) {
  try {
    const result: unknown = JSON.parse(job.result || "{}");
    if (result && typeof result === "object" && "message" in result && typeof result.message === "string" && result.message) return result.message;
  } catch { /* fall through to the generic message */ }
  return "核心更新失败，请检查服务器 Agent 日志。";
}

// A server whose Agent has not reported its runtime yet has no known core versions.
export function serverCoreState(server: ApiServer, core: CoreName, cores: ApiCores): CoreState {
  if (!server.runtime) return { kind: "unreported" };
  const installed = installedCore(server, core);
  const job = cores.jobs.find((item) => item.server_id === server.id && item.type === `update-${core}`);
  if (job && (job.state === "pending" || job.state === "running")) return { kind: "updating", installed, pending: job.state === "pending" };
  const target = coreTarget(cores, core);
  if (!target) return { kind: "untargeted", installed };
  if (installed === target.version) return { kind: "current", installed };
  // A failure recorded before the target last changed belongs to an older target.
  if (job?.state === "failed" && Date.parse(job.created_at) >= Date.parse(target.updated_at)) {
    return { kind: "failed", installed, target: target.version, detail: failureDetail(job) };
  }
  return { kind: "outdated", installed, target: target.version };
}

export function coreSummary(servers: ApiServer[], core: CoreName, cores: ApiCores) {
  const states = servers.filter((server) => server.agent_status !== "pending").map((server) => serverCoreState(server, core, cores));
  const count = (...kinds: CoreState["kind"][]) => states.filter((state) => kinds.includes(state.kind)).length;
  return { total: states.length, current: count("current"), outdated: count("outdated", "failed"), updating: count("updating"), unreported: count("unreported") };
}
