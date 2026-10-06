import assert from "node:assert/strict";
import test from "node:test";

import {
  buildQualityIncidents,
  getProbePointWindow,
  isProbeFresh,
  probePointIssue,
  probeStatusLabel,
} from "../app/lib/quality.ts";

const stable = {
  checked_at: "2026-07-26T00:00:00Z",
  attempts: 60,
  successes: 60,
  latency: 31.8,
  jitter: 1.1,
  loss: 0,
  availability: 100,
  status: "stable",
};

test("groups consecutive unhealthy buckets and explains the failing metric", () => {
  const points = [
    stable,
    {
      ...stable,
      checked_at: "2026-07-26T00:15:00Z",
      successes: 59,
      loss: 1.67,
      availability: 98.33,
      status: "degraded",
      reasons: ["packet_loss"],
    },
    {
      ...stable,
      checked_at: "2026-07-26T00:30:00Z",
      latency: 540,
      status: "degraded",
      reasons: ["high_latency"],
    },
    { ...stable, checked_at: "2026-07-26T00:45:00Z" },
    {
      ...stable,
      checked_at: "2026-07-26T01:00:00Z",
      successes: 0,
      latency: 0,
      jitter: 0,
      loss: 100,
      availability: 0,
      status: "down",
      reasons: ["unreachable"],
    },
  ];

  const incidents = buildQualityIncidents(points, "2026-07-26T01:15:00Z");

  assert.equal(incidents.length, 2);
  assert.equal(incidents[0].title, "多项指标异常");
  assert.equal(incidents[0].duration, "30 分钟");
  assert.match(incidents[0].detail, /最低可用率 98\.3%/);
  assert.match(incidents[0].detail, /峰值延迟 540\.0 ms/);
  assert.equal(incidents[1].title, "连接中断");
  assert.equal(incidents[1].duration, "15 分钟");
  assert.match(incidents[1].detail, /最低可用率 0\.00%/);
});

test("returns exact bucket windows and compact point explanations", () => {
  const points = [
    stable,
    {
      ...stable,
      checked_at: "2026-07-26T00:15:00Z",
      successes: 57,
      loss: 5,
      availability: 95,
      status: "degraded",
      reasons: ["packet_loss"],
    },
  ];

  assert.deepEqual(getProbePointWindow(points, 0, "2026-07-26T00:30:00Z"), {
    start: "2026-07-26T00:00:00Z",
    end: "2026-07-26T00:15:00Z",
  });
  assert.deepEqual(getProbePointWindow(points, 1, "2026-07-26T00:30:00Z"), {
    start: "2026-07-26T00:15:00Z",
    end: "2026-07-26T00:30:00Z",
  });
  assert.equal(probeStatusLabel("degraded"), "质量下降");
  assert.equal(probePointIssue(points[1]), "建连失败 5.00%");
});

test("accepts only current probe snapshots as live data", () => {
  const now = Date.parse("2026-07-26T00:02:00Z");
  const probe = {
    forward_id: "forward-1",
    server_id: "server-1",
    checked_at: "2026-07-26T00:01:00Z",
    attempts: 3,
    successes: 3,
    latency_ms: 20,
    jitter_ms: 1,
    loss_percent: 0,
    status: "stable",
  };

  assert.equal(isProbeFresh(probe, now), true);
  assert.equal(isProbeFresh({ ...probe, checked_at: "2026-07-26T00:00:00Z" }, now), false);
  assert.equal(isProbeFresh({ ...probe, checked_at: "2026-07-26T00:02:01Z" }, now), false);
  assert.equal(isProbeFresh({ ...probe, checked_at: "not-a-date" }, now), false);
});
