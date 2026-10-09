import assert from "node:assert/strict";
import test from "node:test";
import { imageTag, panelUpdateView, updateOutcome } from "../app/lib/update.ts";

const base = { current: "v0.2.1", latest: "v0.3.0", deployment: "systemd", updater: true };

test("offers the latest release only when it is newer", () => {
  assert.deepEqual(panelUpdateView(base), { kind: "available", target: "v0.3.0", method: "updater", failedFrom: undefined, unanswered: false });
  assert.deepEqual(panelUpdateView({ ...base, latest: "v0.2.1" }), { kind: "current" });
  assert.deepEqual(panelUpdateView({ ...base, current: "dev" }), { kind: "current" });
  assert.deepEqual(panelUpdateView({ ...base, latest: undefined, check_error: "无法连接 GitHub 检查新版本" }), { kind: "current" });
  assert.deepEqual(panelUpdateView(null), { kind: "current" });
});

test("explains how each deployment upgrades", () => {
  assert.equal(panelUpdateView({ ...base, updater: false, deployment: "docker" }).method, "docker");
  assert.equal(panelUpdateView({ ...base, updater: false }).method, "installer");
  assert.equal(panelUpdateView({ ...base, updater: false, deployment: "" }).method, "manual");
  assert.equal(imageTag("v0.3.0"), "0.3.0");
});

test("shows a queued or running update and the last failure", () => {
  assert.deepEqual(panelUpdateView({ ...base, pending: "v0.3.0" }), { kind: "updating", target: "v0.3.0" });
  const running = { state: "running", from: "v0.2.1", target: "v0.3.0", started_at: "2026-10-09T08:00:00Z" };
  assert.deepEqual(panelUpdateView({ ...base, last: running }), { kind: "updating", target: "v0.3.0" });
  const failed = { ...running, state: "failed", finished_at: "2026-10-09T08:01:00Z" };
  assert.equal(panelUpdateView({ ...base, last: failed }).failedFrom, "v0.2.1");
  assert.equal(panelUpdateView({ ...base, latest: "v0.3.1", last: failed }).failedFrom, undefined);
  assert.equal(panelUpdateView({ ...base, unanswered: true }).unanswered, true);
});

test("judges an update from the panel that came back", () => {
  const run = { from: "v0.2.1", target: "v0.3.0", started_at: "2026-10-09T08:00:00Z" };
  assert.equal(updateOutcome("v0.3.0", { ...base, current: "v0.3.0" }), "succeeded");
  assert.equal(updateOutcome("v0.3.0", { ...base, pending: "v0.3.0" }), "running");
  assert.equal(updateOutcome("v0.3.0", { ...base, last: { ...run, state: "running" } }), "running");
  assert.equal(updateOutcome("v0.3.0", { ...base, last: { ...run, state: "failed" } }), "failed");
  assert.equal(updateOutcome("v0.3.0", base), "unknown");
});
