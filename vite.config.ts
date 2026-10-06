import { mkdirSync, writeFileSync } from "node:fs";
import vinext from "vinext";
import { defineConfig, type Plugin } from "vite";

// The development server forwards API calls to a locally running panel.
const panel = process.env.PORTOLAN_API_ORIGIN ?? "http://127.0.0.1:8088";

// Records the npm packages bundled into the browser console so release
// archives can carry their licenses (scripts/third-party-licenses.mjs).
function bundledPackages(): Plugin {
  const packages = new Set<string>();
  return {
    name: "portolan-bundled-packages",
    apply: "build",
    generateBundle(_options, bundle) {
      if (this.environment?.name !== "client") return;
      for (const output of Object.values(bundle)) {
        if (output.type !== "chunk") continue;
        for (const id of output.moduleIds) {
          const path = id.replaceAll("\\", "/");
          const index = path.lastIndexOf("/node_modules/");
          if (index < 0) continue;
          const [first, second] = path.slice(index + "/node_modules/".length).split("/");
          packages.add(first.startsWith("@") ? `${first}/${second}` : first);
        }
      }
      mkdirSync("dist", { recursive: true });
      writeFileSync("dist/client-packages.json", JSON.stringify([...packages].sort(), null, 2) + "\n");
    },
  };
}

export default defineConfig({
  server: {
    proxy: {
      "/api": panel,
      "/healthz": panel,
    },
  },
  plugins: [vinext(), bundledPackages()],
});
