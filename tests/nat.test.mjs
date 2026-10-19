import assert from "node:assert/strict";
import test from "node:test";

import { displayPort, publicPort, serverEndpoint } from "../app/lib/endpoints.ts";
import { describeMapping, hostRanges, mappedPortCount, mappingRows, natPortHint, parseMappingRows } from "../app/lib/nat.ts";

const nat = {
  address: "203.0.113.40",
  ipv6_address: "2001:db8::40",
  nat_ports: [{ public: 10001, host: 10001, count: 20 }, { public: 30001, host: 20001, count: 10 }],
};

test("publishes the mapped port over IPv4 and the host port over IPv6", () => {
  assert.equal(publicPort(nat, "203.0.113.40", 10005), 10005);
  assert.equal(publicPort(nat, "203.0.113.40", 20003), 30003);
  assert.equal(publicPort(nat, "nat.example.com", 20003), 30003);
  assert.equal(publicPort(nat, "203.0.113.40", 20011), null);
  assert.equal(publicPort(nat, "2001:db8::40", 20011), 20011);
  assert.equal(publicPort({ address: "203.0.113.41" }, "203.0.113.41", 45678), 45678);
  assert.equal(serverEndpoint(nat, 20003), "203.0.113.40:30003");
  assert.equal(serverEndpoint(nat, 20011), "");
  assert.equal(serverEndpoint({ ...nat, address: "2001:db8::40" }, 20011), "[2001:db8::40]:20011");
  assert.equal(displayPort(nat, 20009), 30009);
});

test("parses provider port ranges into mappings", () => {
  assert.deepEqual(parseMappingRows([{ public: "30001-30020", host: "10001" }, { public: " 40001 ～ 40005 ", host: "" }, { public: "", host: "" }]), {
    mappings: [{ public: 30001, host: 10001, count: 20 }, { public: 40001, host: 40001, count: 5 }],
  });
  assert.deepEqual(parseMappingRows([{ public: "30022", host: "22" }]), { mappings: [{ public: 30022, host: 22, count: 1 }] });
  assert.deepEqual(parseMappingRows([{ public: "30001-30020", host: "10001-10020" }]), { mappings: [{ public: 30001, host: 10001, count: 20 }] });
  for (const rows of [
    [{ public: "abc", host: "" }],
    [{ public: "30020-30001", host: "" }],
    [{ public: "30001-30020", host: "10001-10005" }],
    [{ public: "30001", host: "65536" }],
    [{ public: "65530-65535", host: "65533" }],
    [{ public: "30001-30020", host: "" }, { public: "30010", host: "40000" }],
    [{ public: "30001-30020", host: "10001" }, { public: "40001", host: "10005" }],
  ]) {
    assert.ok("error" in parseMappingRows(rows), JSON.stringify(rows));
  }
});

test("describes mappings for the form and the server page", () => {
  const mappings = [{ public: 30001, host: 10001, count: 20 }, { public: 40001, host: 40001, count: 5 }, { public: 30022, host: 22, count: 1 }];
  assert.deepEqual(mappingRows(mappings), [
    { public: "30001-30020", host: "10001-10020" },
    { public: "40001-40005", host: "" },
    { public: "30022", host: "22" },
  ]);
  assert.equal(describeMapping(mappings[0]), "30001-30020 → 10001-10020");
  assert.equal(describeMapping(mappings[1]), "40001-40005");
  assert.equal(hostRanges(mappings), "22、10001-10020、40001-40005");
  assert.equal(mappedPortCount(mappings), 26);
});

test("hints what NAT mappings make of an entered host port", () => {
  assert.equal(natPortHint({}, "443"), undefined);
  assert.equal(natPortHint(nat, ""), "NAT 映射内的本机端口：10001-10020、20001-20010");
  assert.equal(natPortHint(nat, "10003"), undefined);
  assert.equal(natPortHint(nat, "20004"), "公网端口 30004");
  assert.match(natPortHint(nat, "443"), /^不在 NAT 端口映射内/);
});
