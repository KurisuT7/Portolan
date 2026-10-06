import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import test from "node:test";

// The panel embeds this static export; see internal/webui.
const exported = new URL("../dist/client/", import.meta.url);

test("exports the connection gate without presenting invented resource state", async () => {
  const html = await readFile(new URL("index.html", exported), "utf8");
  assert.match(html, /<title>Portolan<\/title>/i);
  assert.match(html, /lang="zh-CN"/);
  assert.match(html, /连接面板/);
  assert.doesNotMatch(html, /HK experimental|预览模式|服务器在线率/);

  const assets = [
    ...html.matchAll(/(?:src|href)="(\/_next\/static\/[^"?#]+)/g),
  ].map((match) => match[1]);
  assert.ok(assets.length > 0, "page must reference built assets");
  for (const asset of new Set(assets))
    await access(new URL(`.${asset}`, exported));
});

test("exports no server routes", async () => {
  const html = await readFile(new URL("404.html", exported), "utf8");
  assert.match(html, /<title>Portolan<\/title>/i);
  await assert.rejects(access(new URL("lib", exported)));
});
