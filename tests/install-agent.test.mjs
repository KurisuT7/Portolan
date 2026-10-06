import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("reinstall restarts an already running Agent", async () => {
  const installer = await readFile(new URL("../scripts/install-agent.sh", import.meta.url), "utf8");

  assert.match(installer, /systemctl enable portolan-agent\.service/);
  assert.match(installer, /systemctl restart portolan-agent\.service/);
  assert.doesNotMatch(installer, /systemctl enable --now portolan-agent\.service/);
});

test("installer takes panel-selected cores and upgrades running cores safely", async () => {
  const [installer, bootstrap, unit] = await Promise.all([
    readFile(new URL("../scripts/install-agent.sh", import.meta.url), "utf8"),
    readFile(new URL("../internal/api/downloads.go", import.meta.url), "utf8"),
    readFile(new URL("../ops/systemd/portolan-agent.service", import.meta.url), "utf8"),
  ]);

  assert.doesNotMatch(installer, /VERSION=|channel|mirror|github\.com/);
  for (const flag of ["--sing-box-url", "--sing-box-sha256", "--realm-url", "--realm-sha256"]) {
    assert.ok(installer.includes(`${flag})`), `installer must accept ${flag}`);
    assert.ok(bootstrap.includes(flag), `bootstrap must pass ${flag}`);
  }
  assert.match(installer, /verify "\$sing_archive"/);
  assert.match(installer, /verify "\$realm_archive"/);
  // The Agent replaces core binaries in place when the panel requests an update.
  assert.match(installer, /^ReadWritePaths=\/etc\/portolan \/usr\/local\/lib\/portolan$/m);
  assert.match(unit, /^ReadWritePaths=\/etc\/portolan \/usr\/local\/lib\/portolan$/m);
  const firstInstall = installer.indexOf("install -o root -g root -m 0755 \"$agent_binary\"");
  const check = installer.indexOf('"$sing_binary" check -c "$active/00-base.json"');
  assert.ok(check > 0, "the new core must check the active release");
  assert.ok(check < firstInstall, "the check must precede binary replacement");
  for (const run of ['"$sing_binary" version >/dev/null', '"$realm_binary" -v >/dev/null']) {
    const index = installer.indexOf(run);
    assert.ok(index > 0 && index < firstInstall, `${run} must run before anything is replaced`);
  }
  const coreRestart = installer.indexOf("systemctl restart portolan-sing-box.service");
  assert.ok(coreRestart > installer.indexOf("portolan-agent enroll"), "running cores restart only after enrollment revoked the previous Agent");
  assert.ok(coreRestart < installer.indexOf("systemctl restart portolan-agent.service"), "cores restart before the new Agent starts");
  assert.match(installer, /if \[ "\$sing_changed" = true \] && systemctl is-active --quiet portolan-sing-box\.service; then/);
  assert.match(installer, /'portolan-realm@\*' \|\n\s+while read -r unit _; do systemctl restart "\$unit"; done/);
});

test("Agent service permits public interface address discovery", async () => {
  const [installer, unit] = await Promise.all([
    readFile(new URL("../scripts/install-agent.sh", import.meta.url), "utf8"),
    readFile(new URL("../ops/systemd/portolan-agent.service", import.meta.url), "utf8"),
  ]);
  const generatedUnit = installer.match(
    /cat >\/etc\/systemd\/system\/portolan-agent\.service <<'EOF'\n([\s\S]*?)\nEOF/,
  );

  assert.ok(generatedUnit, "installer should contain the Agent systemd unit");
  assert.match(generatedUnit[1], /^RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK$/m);
  assert.match(unit, /^RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK$/m);
});

test("sing-box service permits route update subscriptions", async () => {
  const [installer, unit] = await Promise.all([
    readFile(new URL("../scripts/install-agent.sh", import.meta.url), "utf8"),
    readFile(new URL("../ops/systemd/portolan-sing-box.service", import.meta.url), "utf8"),
  ]);
  const generatedUnit = installer.match(
    /cat >\/etc\/systemd\/system\/portolan-sing-box\.service <<'EOF'\n([\s\S]*?)\nEOF/,
  );

  assert.ok(generatedUnit, "installer should contain the sing-box systemd unit");
  assert.match(generatedUnit[1], /^RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK$/m);
  assert.match(unit, /^RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK$/m);
});
