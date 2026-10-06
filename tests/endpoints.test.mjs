import assert from "node:assert/strict";
import test from "node:test";

import {
  formatEndpoint,
  isPublicIPv4,
  preferredIngressHost,
} from "../app/lib/endpoints.ts";

test("formats copy-ready IPv4, IPv6 and domain endpoints", () => {
  assert.equal(formatEndpoint("203.0.113.10", 443), "203.0.113.10:443");
  assert.equal(formatEndpoint("2001:db8::10", 8443), "[2001:db8::10]:8443");
  assert.equal(formatEndpoint("[2001:db8::10]", 8443), "[2001:db8::10]:8443");
  assert.equal(formatEndpoint("edge.example.com", 2053), "edge.example.com:2053");
});

test("selects a usable ingress address for client copy", () => {
  assert.equal(preferredIngressHost({
    address: "edge.example.com",
    ipv4: "203.0.113.10",
    ipv6: "2001:db8::10",
  }), "edge.example.com");
  assert.equal(preferredIngressHost({
    address: "自动识别公网地址",
    ipv4: "203.0.113.10",
    ipv6: "2001:db8::10",
  }), "203.0.113.10");
  assert.equal(preferredIngressHost({
    address: "100.64.0.8",
    ipv4: "198.51.100.8",
    ipv6: "2001:db8::8",
  }), "198.51.100.8");
  assert.equal(preferredIngressHost({
    address: "",
    ipv4: "",
    ipv6: "2001:db8::20",
  }), "2001:db8::20");
  assert.equal(preferredIngressHost(undefined), "");
  assert.equal(isPublicIPv4("100.64.1.2"), false);
  assert.equal(isPublicIPv4("203.0.113.10"), true);
});
