// Writes the licenses of everything a release archive redistributes: Go
// modules linked into the binaries, npm packages bundled into the console
// (dist/client-packages.json from `npm run build`) and the bundled fonts.
// Usage: node scripts/third-party-licenses.mjs OUTPUT_FILE
import { execFileSync } from "node:child_process";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const output = process.argv[2];
if (!output) {
  console.error("usage: node scripts/third-party-licenses.mjs OUTPUT_FILE");
  process.exit(2);
}

function licenseFile(directory, name) {
  const candidates = readdirSync(directory).filter((file) => /^(licen[cs]e|copying)(\.(md|txt))?$/i.test(file));
  if (!candidates.length) throw new Error(`no license file for ${name} in ${directory}`);
  return readFileSync(join(directory, candidates[0]), "utf8").trim();
}

const sections = [];

const goroot = execFileSync("go", ["env", "GOROOT"], { encoding: "utf8" }).trim();
const goVersion = execFileSync("go", ["env", "GOVERSION"], { encoding: "utf8" }).trim();
sections.push({ name: `Go standard library and runtime (${goVersion})`, text: licenseFile(goroot, "Go") });

const modules = execFileSync("go", ["list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}",
  "./cmd/portolan-panel", "./cmd/portolan-agent", "./cmd/portolan-runtime-import"], { encoding: "utf8" });
for (const line of [...new Set(modules.split("\n").filter(Boolean))].sort()) {
  const [path, version, directory] = line.split("\t");
  sections.push({ name: `${path} ${version}`, text: licenseFile(directory, path) });
}

// Packages that declare their license in package.json but ship no license file
// use the file of a package published from the same repository.
const licenseSource = { "@vitejs/plugin-rsc": "@vitejs/plugin-react" };

const bundled = JSON.parse(readFileSync("dist/client-packages.json", "utf8"));
for (const name of bundled) {
  const directory = join("node_modules", name);
  const manifest = JSON.parse(readFileSync(join(directory, "package.json"), "utf8"));
  const source = licenseSource[name] ? join("node_modules", licenseSource[name]) : directory;
  sections.push({ name: `${name} ${manifest.version} (npm, bundled into the web console)`, text: licenseFile(source, name) });
}

const font = "third_party/geist-font/OFL.txt";
if (!existsSync(font)) throw new Error(`missing ${font}`);
sections.push({ name: "Geist and Geist Mono fonts (bundled into the web console)", text: readFileSync(font, "utf8").trim() });

const rule = "=".repeat(78);
const body = sections.map((section) => `${rule}\n${section.name}\n${rule}\n\n${section.text}\n`).join("\n");
writeFileSync(output, `Portolan redistributes the following third-party software.\n\n${body}`);
console.log(`wrote ${sections.length} notices to ${output}`);
