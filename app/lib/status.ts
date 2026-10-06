import type { ApiForward, ApiJob, ApiServer, ApiUnit } from "./api";

export type StatusTone = "good" | "warn" | "bad" | "neutral";
export function serverState(server: ApiServer, now = Date.now()) {
  if (server.agent_status === "pending") return { label: "待安装", tone: "neutral" as StatusTone };
  const age = now - Date.parse(server.last_seen_at || "");
  const online = server.agent_status === "online" && Number.isFinite(age) && age >= 0 && age < 120_000;
  return { label: online ? "在线" : "离线", tone: (online ? "good" : "bad") as StatusTone };
}

export const singBoxUnit = "portolan-sing-box.service";
const realmPrefix = "portolan-realm@";

export function forwardUnit(forward: Pick<ApiForward, "id" | "engine">) {
  return forward.engine === "realm" ? `${realmPrefix}${forward.id}.service` : singBoxUnit;
}

export function realmForwardId(unit: string) {
  return unit.startsWith(realmPrefix) ? unit.slice(realmPrefix.length, -".service".length) : "";
}

const running = (unit: ApiUnit) => unit.active_state === "active" && unit.sub_state === "running";

export function unitDetail(unit: ApiUnit) {
  if (running(unit)) return "运行中";
  if (unit.sub_state === "auto-restart") return "反复重启";
  if (unit.active_state === "failed") return "启动失败";
  if (unit.active_state === "inactive") return "已停止";
  return `${unit.active_state}/${unit.sub_state}`;
}

// Service state is only claimed while the Agent heartbeat is fresh.
function reportedUnits(server: ApiServer | undefined, now: number) {
  return server?.runtime && serverState(server, now).label === "在线" ? server.runtime.units : null;
}

export function unitState(server: ApiServer | undefined, unit: string, now = Date.now()) {
  const reported = reportedUnits(server, now)?.find((item) => item.name === unit);
  return reported ? { running: running(reported), detail: unitDetail(reported) } : null;
}

export function stoppedUnits(server: ApiServer, now = Date.now()) {
  return (reportedUnits(server, now) ?? []).filter((unit) => !running(unit));
}

export function configurationState(job?: ApiJob, unavailable = false, now = Date.now(), changedAt?: string) {
  if (unavailable) return { label: "状态未更新", tone: "neutral" as StatusTone, detail: "暂时无法读取服务器配置状态。", retry: false };
  if (changedAt && (!job || Date.parse(job.created_at) < Date.parse(changedAt))) return { label: "等待状态确认", tone: "warn" as StatusTone, detail: "修改已保存，正在读取对应的配置应用回执。", retry: false };
  if (!job) return { label: "尚无回执", tone: "neutral" as StatusTone, detail: "还没有这台服务器的配置应用记录。", retry: true };
  if (job.applied === "current") return { label: "已同步", tone: "good" as StatusTone, detail: "服务器正在运行最近提交的配置。实际连接质量请查看延迟检测。", retry: true };
  if (job.applied === "ahead") return { label: "比面板记录新", tone: "bad" as StatusTone, detail: "服务器正在运行比面板记录更新的配置，面板数据可能来自旧备份。面板不会自动覆盖它；核对后再重新同步。", retry: true };
  if (job.state === "succeeded" && job.applied === "behind") return { label: "待重新下发", tone: "warn" as StatusTone, detail: "服务器报告的配置比最近回执旧，面板会自动重新下发。", retry: true };
  if (job.state === "succeeded") return { label: "已同步", tone: "good" as StatusTone, detail: "Agent 已确认最近配置。实际连接质量请查看延迟检测。", retry: true };
  if (job.state === "pending") return { label: "待应用", tone: "warn" as StatusTone, detail: "修改已保存，等待入口 Agent 领取。离线服务器会在重新连接后接收。", retry: false };
  if (job.state === "running") {
    const started = Date.parse(job.started_at || job.created_at);
    const uncertain = !Number.isFinite(started) || now - started >= 120_000;
    return { label: uncertain ? "结果未确认" : "应用中", tone: "warn" as StatusTone, detail: uncertain ? "Agent 尚未返回结果；这不等于应用失败。可重新提交当前配置以恢复同步。" : "Agent 正在检查并应用服务器配置。", retry: uncertain };
  }
  let detail = "配置应用失败，请检查服务器 Agent 日志。";
  try {
    const result: unknown = JSON.parse(job.result || "{}");
    if (result && typeof result === "object" && "message" in result && typeof result.message === "string") detail = result.message;
  } catch { /* Legacy raw process output is not shown in the console. */ }
  return { label: "应用失败", tone: "bad" as StatusTone, detail, retry: true };
}
