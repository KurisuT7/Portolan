import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

globalThis.window = { setTimeout, clearTimeout };

const source = await readFile(
  new URL("../app/lib/api.ts", import.meta.url),
  "utf8",
);
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    target: ts.ScriptTarget.ES2022,
    module: ts.ModuleKind.ESNext,
  },
});
const { PortolanApi, ApiError } = await import(
  `data:text/javascript;base64,${Buffer.from(outputText).toString("base64")}`
);

test("mutations use session CSRF and logout clears it", async (t) => {
  t.mock.method(globalThis, "fetch", async (_url, init) => {
    const headers = new Headers(init.headers);
    if (init.method === "POST" && _url === "/api/v1/session") {
      assert.equal(headers.has("X-CSRF-Token"), false);
      return Response.json({ csrf_token: "test-csrf" });
    }
    assert.equal(headers.get("X-CSRF-Token"), "test-csrf");
    assert.equal(init.credentials, "same-origin");
    return new Response(null, { status: 204 });
  });
  const api = new PortolanApi();
  await api.login("example-test-token");
  await api.deleteForward("forward/with/slash");
  assert.equal(
    fetch.mock.calls[1].arguments[0],
    "/api/v1/forwards/forward%2Fwith%2Fslash",
  );
  await api.logout();
  t.mock.method(globalThis, "fetch", async (_url, init) => {
    assert.equal(new Headers(init.headers).has("X-CSRF-Token"), false);
    return new Response(null, { status: 204 });
  });
  await api.deleteForward("another");
});

test("external cancellation stays distinguishable from a connection failure", async (t) => {
  t.mock.method(globalThis, "fetch", async (_url, init) => {
    init.signal.throwIfAborted();
    throw new Error("unexpected request");
  });
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(
    new PortolanApi().servers(controller.signal),
    (error) => error.name === "AbortError" && !(error instanceof ApiError),
  );
});

test("response body consumption stays inside the request timeout", async (t) => {
  let expire;
  let cleared = false;
  let bodyStarted;
  const started = new Promise((resolve) => { bodyStarted = resolve; });
  t.mock.method(window, "setTimeout", (callback) => { expire = callback; return 1; });
  t.mock.method(window, "clearTimeout", () => { cleared = true; });
  t.mock.method(globalThis, "fetch", async (_url, init) => ({
    ok: true, status: 200,
    json: () => new Promise((_resolve, reject) => {
      init.signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
      bodyStarted();
    }),
  }));
  const request = new PortolanApi().servers();
  await started;
  assert.equal(cleared, false, "timeout must remain armed until body completes");
  expire();
  await assert.rejects(request, (error) => error instanceof ApiError && /超时/.test(error.message));
  assert.equal(cleared, true);
});

test("forward edits use PUT, preserve CSRF, and export overrides are encoded", async (t) => {
  const calls = [];
  t.mock.method(globalThis, "fetch", async (url, init) => {
    calls.push({ url, init });
    if (url === "/api/v1/session") return Response.json({ csrf_token: "test-csrf" });
    return Response.json({ id: "rule/1" });
  });
  const api = new PortolanApi();
  await api.login("test-admin-token");
  const payload = { name: "原始名称", ingress_server_id: "entry", listen_port: 25001, networks: ["tcp"], engine: "realm", enabled: false, target_host: "example.com", target_port: 443 };
  await api.updateForward("rule/1", payload);
  assert.equal(calls[1].url, "/api/v1/forwards/rule%2F1");
  assert.equal(calls[1].init.method, "PUT");
  assert.equal(new Headers(calls[1].init.headers).get("X-CSRF-Token"), "test-csrf");
  assert.deepEqual(JSON.parse(calls[1].init.body), payload);
  await api.exportNode("node/1", { address: "2001:db8::1", port: 25001, name: "原始节点 & 名称" });
  const exportURL = new URL(calls[2].url, "http://localhost");
  assert.equal(exportURL.pathname, "/api/v1/nodes/node%2F1/export");
  assert.equal(exportURL.searchParams.get("address"), "2001:db8::1");
  assert.equal(exportURL.searchParams.get("name"), "原始节点 & 名称");
  await api.syncServer("entry/1");
  assert.equal(calls[3].url, "/api/v1/servers/entry%2F1/sync");
  assert.equal(calls[3].init.method, "POST");
});

test("authentication failures preserve HTTP status for the login gate", async (t) => {
  t.mock.method(globalThis, "fetch", async () =>
    Response.json({ error: "session expired" }, { status: 401 }),
  );
  await assert.rejects(
    new PortolanApi().nodes(),
    (error) => error instanceof ApiError && error.status === 401,
  );
});
