import type { ApiJob, ApiServer } from "./api";

export const agentUpdateJob = "update-agent";
// First Agent release that installs updates sent by the panel; matches agentSelfUpdateSince in internal/api.
export const agentSelfUpdateSince = "v0.2.0";

const release = (version: string | undefined) => /^v(\d+)\.(\d+)\.(\d+)/.exec(version ?? "")?.slice(1).map(Number);

// Release builds report versions such as v0.1.0; development builds report "dev" and compare as neither older nor newer.
export function olderRelease(a: string | undefined, b: string | undefined) {
  const left = release(a);
  const right = release(b);
  if (!left || !right) return false;
  return (left.map((part, index) => part - right[index]).find((part) => part !== 0) ?? 0) < 0;
}

export type AgentUpdateState =
  | { kind: "current" }
  | { kind: "reinstall"; target: string }
  | { kind: "outdated"; target: string }
  | { kind: "updating"; pending: boolean }
  | { kind: "failed"; target: string; detail: string };

function failureDetail(job: ApiJob) {
  try {
    const result: unknown = JSON.parse(job.result || "{}");
    if (result && typeof result === "object" && "message" in result && typeof result.message === "string" && result.message) return result.message;
  } catch { /* fall through to the generic message */ }
  return "Agent 更新失败，请检查该服务器的 Agent 日志。";
}

// installable is the Agent release the panel serves; jobs are the latest update jobs per server and type.
export function agentUpdateState(server: ApiServer, installable: string, jobs: ApiJob[]): AgentUpdateState {
  const job = jobs.find((item) => item.server_id === server.id && item.type === agentUpdateJob);
  if (job && (job.state === "pending" || job.state === "running")) return { kind: "updating", pending: job.state === "pending" };
  const reported = server.runtime?.agent_version;
  if (!olderRelease(reported, installable)) return { kind: "current" };
  if (olderRelease(reported, agentSelfUpdateSince)) return { kind: "reinstall", target: installable };
  if (job?.state === "failed") return { kind: "failed", target: installable, detail: failureDetail(job) };
  return { kind: "outdated", target: installable };
}

export function agentUpdateSummary(servers: ApiServer[], installable: string, jobs: ApiJob[]) {
  const states = servers.filter((server) => server.agent_status !== "pending").map((server) => agentUpdateState(server, installable, jobs));
  const count = (...kinds: AgentUpdateState["kind"][]) => states.filter((state) => kinds.includes(state.kind)).length;
  return { updatable: count("outdated", "failed"), reinstall: count("reinstall"), updating: count("updating") };
}
