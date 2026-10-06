# Portolan project instructions

Read `CONTRIBUTING.md` for the development setup, commands, and test-data rules. Prefer current code
and tests over prose; when they disagree, fix the prose in the same change.

## Preserve the product boundaries

- `cmd/portolan-panel` and `internal/api` own the control plane; `cmd/portolan-agent` applies only
  structured desired-state jobs through the outbound Agent connection. Do not add arbitrary remote
  shell execution or an inbound Agent management API.
- Keep externally discovered sing-box and Snell nodes read-only (`managed=false`). They may be viewed,
  exported, or selected as forwarding targets, but must not enter Portolan desired state or deletion.
- Keep configuration application versioned and atomic: validate with the real core before switching,
  check that each started unit keeps its main PID and owns its sockets, restore the prior `current`
  release on activation failure, and keep the rollback releases described in `docs/architecture.md`.
- Console status and metrics must represent real control-plane, Agent, node, forward, and probe state.
  A control-plane failure is an outage state, not demo data; do not replace missing probe data with a
  reassuring aggregate.
- Preserve the security boundaries in `docs/security.md`: secrets stay out of source and logs, login
  protections stay on, unsafe protocol options require explicit confirmation, and the installers must
  not take over independently managed services.

## Work in the maintained sources

- Go entry points are under `cmd/`; implementation packages are under `internal/`. The console is under
  `app/`, with browser-independent helpers in `app/lib/` and Node tests under `tests/`. The panel embeds
  the static export through `internal/webui`.
- `scripts/install-agent.sh` and `ops/systemd/` describe the same installed node services, and
  `scripts/install-panel.sh` installs `ops/systemd/portolan-panel.service`. When changing one surface,
  inspect the other and keep `tests/install-agent.test.mjs` and the CI install job aligned.
- Do not hand-edit generated output such as `node_modules/`, `dist/`, `.vinext/`, `release/`, or the
  copied console in `internal/webui/dist/`. Regenerate it with `npm run build` or
  `scripts/build-release.sh`.
- Tests, fixtures, and docs use only documentation addresses (`192.0.2.0/24`, `198.51.100.0/24`,
  `203.0.113.0/24`, `2001:db8::/32`), `example.com`, and generated keys.

## Validate the affected path

- Focused Go changes: test the affected packages; run `go vet` and `go test ./cmd/... ./internal/...`
  for shared interfaces, cross-package changes, or a release candidate. Linux-only apply tests run in CI.
- Browser-independent helpers: run the relevant `node --test` files. Console integration, routing, or
  shared UI changes require `npm run lint`, `npx tsc --noEmit`, and `npm test` (which builds the export).
- Protocol or config-generation changes: run the integration test with `SING_BOX_BIN` set to a real
  supported sing-box binary; a mock-only pass is insufficient.
- Port traffic accounting changes: run the `PORTOLAN_NFT_TEST=1` test against a real `nft` in a
  disposable network namespace (see `CONTRIBUTING.md`); CI runs it too.
- UI changes: exercise the rendered page and interaction on desktop and at 390 px or narrower, check
  `document.documentElement.scrollWidth`, and confirm no Content-Security-Policy violations.
- Dependency changes: run `npm audit` or review the Go module diff, and assess findings against what
  the release actually ships (the panel serves only static console files).
- Installer or release changes: run `sh -n` on the scripts and the focused Node tests; the CI install
  job exercises a real systemd install, upgrade, and container run.
- Documentation-only edits need content, link, and diff review, not builds.
- Before handoff, run `git diff --check` and review the complete diff.
