import assert from "node:assert/strict";
import test from "node:test";
import { nodeFormValues } from "../app/lib/nodes.ts";

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
