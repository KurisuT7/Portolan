import assert from "node:assert/strict";
import test from "node:test";
import { configurationState, serverState } from "../app/lib/status.ts";
import { latencySegments, sampleIndexAt, buildQualityIncidents } from "../app/lib/quality.ts";

const now = Date.parse("2026-09-12T00:00:00Z");
const job = { id: "sync-1", server_id: "server-1", type: "sync", state: "succeeded", created_at: "2026-09-11T23:58:00Z" };

test("server status distinguishes enrollment, fresh, stale and future heartbeats", () => {
  assert.equal(serverState({ agent_status: "pending" }, now).label, "待安装");
  assert.equal(serverState({ agent_status: "online", last_seen_at: "2026-09-11T23:59:00Z" }, now).label, "在线");
  for (const at of ["2026-09-11T23:58:00Z", "2026-09-12T00:01:00Z", "invalid", ""]) {
    assert.equal(serverState({ agent_status: "online", last_seen_at: at }, now).label, "离线");
  }
});

test("configuration state never presents old or unavailable receipts as current success", () => {
  assert.equal(configurationState(job, false, now).label, "已同步");
  assert.equal(configurationState(job, true, now).label, "状态未更新");
  assert.equal(configurationState(job, false, now, "2026-09-11T23:59:00Z").label, "等待状态确认");
  assert.equal(configurationState(undefined, false, now).label, "尚无回执");
  assert.equal(configurationState({ ...job, state: "pending" }, false, now).label, "待应用");
  const running = configurationState({ ...job, state: "running", started_at: job.created_at }, false, now);
  assert.equal(running.label, "结果未确认");
  assert.equal(running.retry, true);
  assert.equal(configurationState({ ...job, state: "running", started_at: "2026-09-11T23:59:30Z" }, false, now).label, "应用中");
});

test("configuration errors show safe summaries rather than raw legacy process output", () => {
  const legacy = configurationState({ ...job, state: "failed", result: "private-key=do-not-display" }, false, now);
  assert.equal(legacy.label, "应用失败");
  assert.doesNotMatch(legacy.detail, /private-key/);
  const structured = configurationState({ ...job, state: "failed", result: JSON.stringify({ message: "核心配置校验失败，未切换到新配置。" }) }, false, now);
  assert.equal(structured.detail, "核心配置校验失败，未切换到新配置。");
});

test("latency paths break at missing, failed and UDP buckets", () => {
  const success = { checked_at: "2026-09-11T23:00:00Z", status: "stable", attempts: 3, successes: 3, latency: 20, jitter: 0, loss: 0, availability: 100 };
  const points = [success, { ...success, status: "unknown", successes: 0, attempts: 0 }, success, { ...success, status: "down", successes: 0 }, success, { ...success, status: "unsupported", successes: 0 }];
  const paths = latencySegments(points, 600, 100, 40);
  assert.equal(paths.length, 3);
  assert.ok(paths.every((path) => path.startsWith("M") && !path.includes("L")));
  assert.equal(latencySegments([success, success], 200, 100, 40)[0], "M50.00,50.00 L150.00,50.00");
  assert.deepEqual(latencySegments([], 200, 100, 40), []);
  assert.deepEqual(latencySegments([{ ...success, latency: NaN }], 200, 100, 40), []);
});

test("chart pointer selection is bounded and missing buckets split incidents", () => {
  assert.equal(sampleIndexAt(-1, 60), 0);
  assert.equal(sampleIndexAt(1, 60), 59);
  assert.equal(sampleIndexAt(0.5, 60), 30);
  assert.equal(sampleIndexAt(0.5, 0), -1);
  const down = { attempts: 3, successes: 0, latency: 0, jitter: 0, loss: 100, availability: 0, status: "down" };
  const incidents = buildQualityIncidents([
    { ...down, checked_at: "2026-09-11T23:00:00Z" },
    { ...down, checked_at: "2026-09-11T23:15:00Z", status: "unknown", attempts: 0, availability: null },
    { ...down, checked_at: "2026-09-11T23:30:00Z" },
  ], "2026-09-11T23:45:00Z");
  assert.equal(incidents.length, 2);
  assert.equal(incidents[0].end, "2026-09-11T23:15:00Z");
});
