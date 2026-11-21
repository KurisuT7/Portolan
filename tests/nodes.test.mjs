import assert from "node:assert/strict";
import test from "node:test";
import {
  batchClientExports,
  buildNodeRequest,
  clientFormatChoices,
  clientFormats,
  createNodeDraft,
  defaultNodeName,
  nextDraftProtocol,
  nodeFormValues,
  validateNodeDrafts,
} from "../app/lib/nodes.ts";

test("a new node starts from the defaults", () => {
  const values = nodeFormValues();
  assert.equal(values.protocol, "vless-reality");
  assert.equal(values.port, "");
  assert.equal(values.flow, "xtls-rprx-vision");
  assert.equal(values.snellMode, "default");
  assert.equal(values.shadowTLS, false);
});

test("editing starts from the stored settings", () => {
  const reality = nodeFormValues({
    id: "node-a", server_id: "srv-a", protocol: "vless-reality", managed: true, name: "Edge", listen_port: 24001,
    reality: {
      flow: "", handshake_server: "www.example.com", handshake_port: 8443, server_name: "www.example.com",
      fingerprint: "firefox", transport: "grpc", transport_settings: { service_name: "edge" },
    },
  });
  assert.equal(reality.name, "Edge");
  assert.equal(reality.port, "24001");
  assert.equal(reality.flow, "");
  assert.equal(reality.handshake, "www.example.com");
  assert.equal(reality.handshakePort, "8443");
  assert.equal(reality.transport, "grpc");
  assert.equal(reality.serviceName, "edge");

  const snell = nodeFormValues({
    id: "node-b", server_id: "srv-a", protocol: "snell", managed: true, name: "Snell", listen_port: 24002,
    snell: { version: 6, allow_insecure: false },
    shadow_tls: { handshake_server: "www.example.com", handshake_port: 443 },
  });
  assert.equal(snell.snellVersion, 6);
  assert.equal(snell.snellMode, "default");
  assert.equal(snell.shadowTLS, true);
  assert.equal(snell.shadowTLSServer, "www.example.com");

  const obfuscated = nodeFormValues({
    id: "node-c", server_id: "srv-a", protocol: "snell", managed: true, name: "Snell v5", listen_port: 24003,
    snell: { version: 5, obfs_mode: "http", allow_insecure: false },
  });
  assert.equal(obfuscated.snellMode, "http");

  const plain = nodeFormValues({
    id: "node-d", server_id: "srv-a", protocol: "shadowsocks", managed: true, name: "Plain", listen_port: 24004,
    shadowsocks: { method: "none", allow_insecure: true },
  });
  assert.equal(plain.method, "none");
  assert.equal(plain.allowUnsafe, true);
});

test("client exports list the share link before the Surge line", () => {
  const both = clientFormats({ uri: "ss://YWVzLTI1Ni1nY206c2VjcmV0@203.0.113.9:8388#edge", surge_line: 'edge = ss, 203.0.113.9, 8388, encrypt-method=aes-256-gcm, password="secret", udp-relay=true' });
  assert.deepEqual(both.map((form) => form.format), ["link", "surge"]);
  assert.deepEqual(clientFormats({ surge_line: "Snell = snell, 203.0.113.9, 6160, psk=correct-horse-battery, version=6" }).map((form) => form.format), ["surge"]);
  assert.deepEqual(clientFormats({}), []);
});

test("only Shadowsocks methods Surge implements offer a choice of form", () => {
  assert.deepEqual(clientFormatChoices({ protocol: "shadowsocks", profile: "2022-blake3-aes-128-gcm · ShadowTLS" }), ["link", "surge"]);
  assert.deepEqual(clientFormatChoices({ protocol: "shadowsocks", profile: "外部 · aes-256-gcm" }), ["link", "surge"]);
  assert.deepEqual(clientFormatChoices({ protocol: "shadowsocks", profile: "2022-blake3-chacha20-poly1305" }), []);
  assert.deepEqual(clientFormatChoices({ protocol: "snell", profile: "Snell v6" }), []);
});

test("multi-node draft helpers pick distinct protocols and predictable default names", () => {
  const first = createNodeDraft("d1");
  assert.equal(nextDraftProtocol([first]), "shadowsocks");
  const second = createNodeDraft("d2", "shadowsocks");
  assert.equal(nextDraftProtocol([first, second]), "snell");
  const third = createNodeDraft("d3", "snell");
  assert.equal(nextDraftProtocol([first, second, third]), "vless-reality");

  assert.equal(defaultNodeName(first, [first], 0, "Tokyo"), "Tokyo");
  assert.equal(defaultNodeName(first, [first, third], 0, "Tokyo"), "Tokyo Reality");
  assert.equal(defaultNodeName(third, [first, third], 1, "Tokyo"), "Tokyo Snell");

  const dupReality = createNodeDraft("d4", "vless-reality");
  assert.equal(defaultNodeName(first, [first, dupReality, third], 0, "Tokyo"), "Tokyo Reality 1");
  assert.equal(defaultNodeName(dupReality, [first, dupReality, third], 1, "Tokyo"), "Tokyo Reality 2");
  assert.equal(defaultNodeName(third, [first, dupReality, third], 2, "Tokyo"), "Tokyo Snell");
  assert.equal(defaultNodeName({ ...dupReality, name: "Custom" }, [first, dupReality], 1, "Tokyo"), "Custom");
});

test("validateNodeDrafts catches oversized names, duplicate batch ports, and occupied server ports", () => {
  const a = { ...createNodeDraft("a", "vless-reality"), port: "24001" };
  const b = { ...createNodeDraft("b", "snell"), port: "24001" };
  assert.deepEqual(validateNodeDrafts([a, b], "Tokyo"), {
    index: 1,
    message: "第 2 个节点：端口 24001 在待创建列表中重复。",
  });

  const c = { ...createNodeDraft("c", "snell"), port: "24002" };
  assert.deepEqual(validateNodeDrafts([a, c], "Tokyo", new Set([24002])), {
    index: 1,
    message: "第 2 个节点：监听端口 24002 已被这台服务器上的节点或转发占用。",
  });

  const tooLong = { ...createNodeDraft("long", "vless-reality"), name: "x".repeat(97) };
  assert.deepEqual(validateNodeDrafts([a, tooLong], "Tokyo"), {
    index: 1,
    message: "第 2 个节点：名称最多 96 字节（约 32 个汉字）。",
  });

  const ok = buildNodeRequest(a, "Tokyo Reality");
  assert.equal(ok.protocol, "vless-reality");
  assert.equal(ok.listen_port, 24001);
});

test("batchClientExports keeps links and Surge lines separated and skips singletons", () => {
  const reality = { uri: "vless://one@203.0.113.10:24001#r" };
  const ss = {
    uri: "ss://two@203.0.113.10:24002#s",
    surge_line: 's = ss, 203.0.113.10, 24002, encrypt-method=aes-256-gcm, password="secret", udp-relay=true',
  };
  const snell = { surge_line: "sn = snell, 203.0.113.10, 24003, psk=psk-1, version=6" };

  assert.deepEqual(batchClientExports([reality, snell]), []);
  assert.deepEqual(batchClientExports([reality, ss]), [
    { format: "link", label: "复制全部链接（2）", value: `${reality.uri}\n${ss.uri}`, count: 2 },
  ]);
  assert.deepEqual(batchClientExports([ss, snell]), [
    { format: "surge", label: "复制全部 Surge 配置（2）", value: `${ss.surge_line}\n${snell.surge_line}`, count: 2 },
  ]);
  assert.deepEqual(batchClientExports([reality, ss, snell]), [
    { format: "link", label: "复制全部链接（2）", value: `${reality.uri}\n${ss.uri}`, count: 2 },
    { format: "surge", label: "复制全部 Surge 配置（2）", value: `${ss.surge_line}\n${snell.surge_line}`, count: 2 },
  ]);
});
