#!/bin/sh
# Builds the release archives from a clean checkout on Linux with Go, Node.js
# 22.13+, npm and GNU tar.
#
#   scripts/build-release.sh v0.1.0
#
# Output in release/:
#   portolan_0.1.0_linux_amd64.tar.gz  panel for x86_64 hosts
#   portolan_0.1.0_linux_arm64.tar.gz  panel for aarch64 hosts
#   install-panel.sh                   installer that downloads one of the archives
#   SHA256SUMS
#   THIRD_PARTY_LICENSES               notices for the redistributed components
#   bin/<arch>/                        the binaries, used by the container image
set -eu

version=${1:-}
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "usage: scripts/build-release.sh v0.1.0" >&2; exit 2 ;;
esac
root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/release"
cd "$root"
rm -rf "$out"
mkdir -p "$out/stage"

echo "Building the web console..."
npm ci --no-audit --no-fund
npm run build
find internal/webui/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
cp -R dist/client/. internal/webui/dist/
rm -rf internal/webui/dist/.vite internal/webui/dist/vinext-client-entry-manifest.json

echo "Building binaries for $version..."
ldflags="-s -w -buildid= -X github.com/KurisuT7/portolan/internal/buildinfo.Version=$version"
for arch in amd64 arm64; do
  for command in portolan-panel portolan-agent portolan-runtime-import; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$out/bin/$arch/$command" "./cmd/$command"
  done
done
node scripts/third-party-licenses.mjs "$out/THIRD_PARTY_LICENSES"
# The published installer downloads its own release unless told otherwise.
sed "s/^release_version=\"\"$/release_version=\"$version\"/" scripts/install-panel.sh >"$out/install-panel.sh"
grep -q "^release_version=\"$version\"$" "$out/install-panel.sh"

# Every archive carries Agents for both architectures: the panel serves them
# to nodes regardless of its own architecture.
epoch=$(git log -1 --format=%ct)
for arch in amd64 arm64; do
  name="portolan_${version#v}_linux_$arch"
  stage="$out/stage/$name"
  mkdir -p "$stage/downloads"
  cp "$out/bin/$arch/portolan-panel" "$out/bin/$arch/portolan-runtime-import" "$stage/"
  cp scripts/install-agent.sh "$stage/downloads/install-agent.sh"
  cp "$out/bin/amd64/portolan-agent" "$stage/downloads/portolan-agent-linux-amd64"
  cp "$out/bin/arm64/portolan-agent" "$stage/downloads/portolan-agent-linux-arm64"
  cp "$out/install-panel.sh" ops/systemd/portolan-panel.service ops/Caddyfile.example \
    LICENSE README.md "$out/THIRD_PARTY_LICENSES" "$stage/"
  chmod 0755 "$stage/portolan-panel" "$stage/portolan-runtime-import" "$stage/install-panel.sh" \
    "$stage/downloads/install-agent.sh" "$stage/downloads/portolan-agent-linux-amd64" "$stage/downloads/portolan-agent-linux-arm64"
  tar -C "$out/stage" --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner -czf "$out/$name.tar.gz" "$name"
done
(cd "$out" && sha256sum -- *.tar.gz install-panel.sh >SHA256SUMS)
rm -rf "$out/stage"
echo "Release files:"
ls -l "$out"
