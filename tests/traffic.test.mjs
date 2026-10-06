import assert from "node:assert/strict";
import test from "node:test";
import { axisLabel, byteScale, fleetTraffic, formatBytes, formatRate, indexTraffic, periodLabel, portErrorText, portsCounted, seriesTotal, totalBytes, trafficEdges, trafficOf } from "../app/lib/traffic.ts";

test("byte amounts use binary multiples with readable precision", () => {
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatBytes(1023), "1023 B");
  assert.equal(formatBytes(1536), "1.50 KB");
  assert.equal(formatBytes(15.25 * 1024 ** 2), "15.3 MB");
  assert.equal(formatBytes(512 * 1024 ** 3), "512 GB");
  assert.equal(formatBytes(3 * 1024 ** 4), "3.00 TB");
  assert.equal(formatRate(2.5 * 1024 ** 2), "2.50 MB/s");
  assert.equal(formatBytes(-5), "0 B");
});

test("byte axes choose a unit and a round step", () => {
  assert.deepEqual(byteScale(0), { unit: "KB", divisor: 1024, step: 0.25, maximum: 1, intervals: 4 });
  const scale = byteScale(7.3 * 1024 ** 3);
  assert.equal(scale.unit, "GB");
  assert.equal(scale.step, 2);
  assert.equal(scale.maximum, 8);
  assert.ok(scale.maximum * scale.divisor >= 7.3 * 1024 ** 3);
});

test("history periods follow the local calendar and end with the current one", () => {
  const now = new Date(2026, 9, 6, 14, 25);
  const hours = trafficEdges("24h", now);
  assert.equal(hours.length, 25);
  assert.deepEqual(hours.at(-2), new Date(2026, 9, 6, 14));
  assert.deepEqual(hours.at(-1), new Date(2026, 9, 6, 15));
  const days = trafficEdges("30d", now);
  assert.equal(days.length, 31);
  assert.deepEqual(days[0], new Date(2026, 8, 7));
  assert.deepEqual(days.at(-1), new Date(2026, 9, 7));
  const months = trafficEdges("12m", now);
  assert.equal(months.length, 13);
  assert.deepEqual(months[0], new Date(2025, 10, 1));
  assert.deepEqual(months.at(-1), new Date(2026, 10, 1));
  assert.equal(periodLabel("24h", hours[0]), "10/05 15:00");
  assert.equal(periodLabel("12m", months[0]), "2025 年 11 月");
  assert.equal(axisLabel("30d", days[0]), "9/7");
});

test("fleet traffic counts reporting servers only, and port traffic needs working counters", () => {
  const traffic = {
    since: "2026-09-30T16:00:00Z",
    items: [
      { kind: "server", id: "a", server_id: "a", rx_bytes: 100, tx_bytes: 50, rx_rate: 0, tx_rate: 0, reported_at: "2026-10-06T06:00:00Z" },
      { kind: "server", id: "b", server_id: "b", rx_bytes: 10, tx_bytes: 5, rx_rate: 0, tx_rate: 0, reported_at: "2026-10-06T06:00:00Z", port_error: "nft_missing" },
      { kind: "node", id: "n", server_id: "a", rx_bytes: 7, tx_bytes: 3, rx_rate: 1, tx_rate: 2 },
    ],
  };
  assert.deepEqual(fleetTraffic(traffic), { rx: 110, tx: 55 });
  assert.equal(fleetTraffic({ since: "", items: [] }), null);
  const index = indexTraffic(traffic);
  assert.equal(totalBytes(trafficOf(index, "node", "n")), 10);
  assert.equal(portsCounted(trafficOf(index, "server", "a")), true);
  assert.equal(portsCounted(trafficOf(index, "server", "b")), false);
  assert.equal(portsCounted(undefined), false);
  assert.match(portErrorText("nft_missing"), /nftables/);
  assert.equal(portErrorText(undefined), "");
  assert.deepEqual(seriesTotal([{ start: "", rx_bytes: 1, tx_bytes: 2 }, { start: "", rx_bytes: 3, tx_bytes: 4 }]), { rx: 4, tx: 6 });
});
