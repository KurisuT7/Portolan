import assert from "node:assert/strict";
import test from "node:test";
import { parseThemePreference, resolveTheme, themeBootScript, themeStorageKey } from "../app/lib/theme.ts";

test("the theme follows the system unless a choice is stored", () => {
  assert.equal(parseThemePreference("light"), "light");
  assert.equal(parseThemePreference("blue"), "system");
  assert.equal(parseThemePreference(null), "system");
  assert.equal(resolveTheme("system", true), "light");
  assert.equal(resolveTheme("system", false), "dark");
  assert.equal(resolveTheme("dark", true), "dark");

  for (const [stored, prefersLight, expected] of [["light", false, "light"], ["dark", true, "dark"], [null, true, "light"], ["x", false, "dark"]]) {
    const attributes = {};
    const context = {
      localStorage: { getItem: (key) => (key === themeStorageKey ? stored : null) },
      matchMedia: () => ({ matches: prefersLight }),
      document: { documentElement: { setAttribute: (name, value) => { attributes[name] = value; } } },
    };
    new Function("localStorage", "matchMedia", "document", themeBootScript)(context.localStorage, context.matchMedia, context.document);
    assert.equal(attributes["data-theme"], expected);
  }
});
