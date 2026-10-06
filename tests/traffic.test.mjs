import assert from "node:assert/strict";
import test from "node:test";
import { axisLabel, byteScale, cycleDays, cycleWord, fleetTraffic, formatBytes, formatRate, indexTraffic, periodLabel, portErrorText, portsCounted, seriesTotal, totalBytes, trafficOf } from "../app/lib/traffic.ts";

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

test("history labels show hours, days, calendar months and cycles in local time", () => {
  const local = (...parts) => new Date(...parts).toISOString();
  assert.equal(periodLabel("24h", { start: local(2026, 9, 5, 15), end: local(2026, 9, 5, 16) }), "10/05 15:00");
  assert.equal(periodLabel("30d", { start: local(2026, 8, 7), end: local(2026, 8, 8) }), "09/07");
  assert.equal(periodLabel("12m", { start: local(2025, 10, 1), end: local(2025, 11, 1) }), "2025 年 11 月");
  assert.equal(periodLabel("12m", { start: local(2026, 1, 28), end: local(2026, 2, 31) }), "2026/02/28 – 03/30");
  assert.equal(axisLabel("30d", new Date(2026, 8, 7)), "9/7");
  assert.equal(axisLabel("12m", new Date(2025, 10, 1)), "11 月");
  assert.equal(axisLabel("12m", new Date(2026, 8, 15)), "9/15");
});

test("cycles are named by their start and end the day before the next reset", () => {
  assert.equal(cycleWord({ since: new Date(2026, 9, 1).toISOString() }), "本月");
  assert.equal(cycleWord({ since: new Date(2026, 8, 15).toISOString() }), "本期");
  assert.equal(cycleWord(undefined), "本月");
  assert.equal(cycleDays(new Date(2026, 8, 15).toISOString(), 15), "09/15 – 10/14");
  // A month without the reset day restarts on its last day.
  assert.equal(cycleDays(new Date(2026, 0, 31).toISOString(), 31), "01/31 – 02/27");
  assert.equal(cycleDays(new Date(2026, 1, 28).toISOString(), 31), "02/28 – 03/30");
});

test("fleet traffic counts reporting servers only, and port traffic needs working counters", () => {
  const month = new Date(2026, 9, 1).toISOString();
  const traffic = {
    items: [
      { kind: "server", id: "a", server_id: "a", since: month, rx_bytes: 100, tx_bytes: 50, rx_rate: 0, tx_rate: 0, reported_at: "2026-10-06T06:00:00Z" },
      { kind: "server", id: "b", server_id: "b", since: month, rx_bytes: 10, tx_bytes: 5, rx_rate: 0, tx_rate: 0, reported_at: "2026-10-06T06:00:00Z", port_error: "nft_missing" },
      { kind: "node", id: "n", server_id: "a", since: month, rx_bytes: 7, tx_bytes: 3, rx_rate: 1, tx_rate: 2 },
    ],
  };
  assert.deepEqual(fleetTraffic(traffic), { rx: 110, tx: 55, calendar: true });
  const cycle = { ...traffic.items[1], since: new Date(2026, 8, 15).toISOString() };
  assert.equal(fleetTraffic({ items: [traffic.items[0], cycle] }).calendar, false);
  assert.equal(fleetTraffic({ items: [] }), null);
  const index = indexTraffic(traffic);
  assert.equal(totalBytes(trafficOf(index, "node", "n")), 10);
  assert.equal(portsCounted(trafficOf(index, "server", "a")), true);
  assert.equal(portsCounted(trafficOf(index, "server", "b")), false);
  assert.equal(portsCounted(undefined), false);
  assert.match(portErrorText("nft_missing"), /nftables/);
  assert.equal(portErrorText(undefined), "");
  assert.deepEqual(seriesTotal([{ start: "", end: "", rx_bytes: 1, tx_bytes: 2 }, { start: "", end: "", rx_bytes: 3, tx_bytes: 4 }]), { rx: 4, tx: 6 });
});
