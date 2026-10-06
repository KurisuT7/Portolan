#!/bin/sh
set -eu

# Portolan panel installer. It installs into Portolan-owned paths, creates the
# portolan-panel system account and a systemd service, and never changes a web
# server or another service. Running it again upgrades the panel and keeps its
# configuration and data.

repository="KurisuT7/Portolan"
# Release builds set this to their own version.
release_version=""
prefix=/usr/local/lib/portolan-panel
config_dir=/etc/portolan-panel
env_file=$config_dir/panel.env
state_dir=/var/lib/portolan-panel
unit=/etc/systemd/system/portolan-panel.service

public_url=""
version=""
archive=""

usage() {
  cat <<'EOF'
Install or upgrade the Portolan panel on a Linux host with systemd.

  sh install-panel.sh --public-url https://panel.example.com

Options:
  --public-url URL   HTTPS address of the panel, used in Agent install commands.
                     Required for the first installation.
  --version VERSION  Release to download, for example v0.1.0. Defaults to the
                     release this script came from, or the latest release.
  --archive FILE     Install from a downloaded release archive instead.

Run as root. Without --archive the script downloads the archive for this
host's architecture from GitHub and checks it against the release's
SHA256SUMS. When it runs from an extracted archive, it installs that archive.
EOF
}

die() {
  printf 'portolan install: %s\n' "$*" >&2
  exit 1
}

need_value() {
  [ "$#" -ge 2 ] || die "$1 requires a value"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --public-url) need_value "$@"; public_url=$2; shift 2 ;;
    --version) need_value "$@"; version=$2; shift 2 ;;
    --archive) need_value "$@"; archive=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "run as root"
[ -d /run/systemd/system ] || die "systemd is required"
for tool in curl tar sha256sum base64 head; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac
if [ -n "$public_url" ]; then
  case "$public_url" in https://?*) ;; *) die "--public-url must start with https://" ;; esac
fi
if [ ! -f "$env_file" ] && [ -z "$public_url" ]; then
  die "--public-url is required for the first installation"
fi

tmp_dir=$(mktemp -d /tmp/portolan-install.XXXXXX)
cleanup() { rm -rf -- "$tmp_dir"; }
trap cleanup EXIT HUP INT TERM

download() {
  curl --fail --location --silent --show-error --retry 3 --retry-delay 2 \
    --proto '=https' --proto-redir '=https' --tlsv1.2 --output "$2" "$1"
}

# Pick the files to install: an extracted archive next to this script, an
# archive given with --archive, or a verified download.
script_dir=$(cd "$(dirname "$0")" 2>/dev/null && pwd || true)
if [ -z "$archive" ] && [ -n "$script_dir" ] && [ -x "$script_dir/portolan-panel" ] && [ -d "$script_dir/downloads" ]; then
  source_dir=$script_dir
else
  if [ -z "$archive" ]; then
    version=${version:-$release_version}
    if [ -z "$version" ]; then
      latest=$(curl --fail --silent --show-error --location --proto '=https' --output /dev/null \
        --write-out '%{url_effective}' "https://github.com/$repository/releases/latest") || die "cannot find the latest release"
      version=${latest##*/}
    fi
    case "$version" in v[0-9]*) ;; *) die "invalid version: $version" ;; esac
    name="portolan_${version#v}_linux_$arch.tar.gz"
    base="https://github.com/$repository/releases/download/$version"
    printf 'Downloading Portolan %s for %s...\n' "$version" "$arch"
    download "$base/$name" "$tmp_dir/$name"
    download "$base/SHA256SUMS" "$tmp_dir/SHA256SUMS"
    expected=$(awk -v file="$name" '{ listed = $2; sub(/^\*/, "", listed) } listed == file { print $1 }' "$tmp_dir/SHA256SUMS")
    [ -n "$expected" ] || die "SHA256SUMS has no entry for $name"
    actual=$(sha256sum "$tmp_dir/$name" | awk '{print $1}')
    [ "$actual" = "$expected" ] || die "checksum mismatch for $name"
    archive="$tmp_dir/$name"
  fi
  [ -f "$archive" ] || die "archive does not exist: $archive"
  mkdir "$tmp_dir/archive"
  tar -xzf "$archive" -C "$tmp_dir/archive"
  source_dir=$(find "$tmp_dir/archive" -mindepth 1 -maxdepth 1 -type d -name 'portolan_*' -print -quit)
  [ -n "$source_dir" ] && [ -x "$source_dir/portolan-panel" ] || die "the archive layout is unexpected"
fi
for file in portolan-panel portolan-runtime-import portolan-panel.service downloads/install-agent.sh \
  downloads/portolan-agent-linux-amd64 downloads/portolan-agent-linux-arm64; do
  [ -f "$source_dir/$file" ] || die "the release is missing $file"
done
"$source_dir/portolan-panel" version >/dev/null 2>&1 || die "the panel binary does not run on this host"
new_version=$("$source_dir/portolan-panel" version)

if ! getent group portolan-panel >/dev/null 2>&1; then
  groupadd --system portolan-panel
fi
if ! id portolan-panel >/dev/null 2>&1; then
  useradd --system --gid portolan-panel --home-dir /nonexistent --shell /usr/sbin/nologin portolan-panel
fi

first_install=false
admin_token=""
if [ ! -f "$env_file" ]; then
  first_install=true
  install -d -o root -g root -m 0700 "$config_dir"
  master_key=$(head -c 32 /dev/urandom | base64)
  admin_token=$(head -c 32 /dev/urandom | base64)
  umask 077
  cat >"$env_file" <<EOF
# Portolan panel settings. systemd reads this file as root; keep it 0600.
# Losing PORTOLAN_MASTER_KEY makes the stored node secrets unrecoverable: back it up.
PORTOLAN_MASTER_KEY=$master_key
PORTOLAN_ADMIN_TOKEN=$admin_token
PORTOLAN_PUBLIC_URL=$public_url
PORTOLAN_LISTEN=127.0.0.1:8088
# Optional: a GeoLite2/GeoIP2 City or DB-IP City Lite MMDB file for server regions.
#PORTOLAN_GEOIP_DB=/var/lib/GeoIP/dbip-city-lite.mmdb
# Reverse proxies whose X-Forwarded-For is trusted, besides this host.
#PORTOLAN_TRUSTED_PROXIES=
EOF
  umask 022
  chmod 0600 "$env_file"
elif [ -n "$public_url" ] && ! grep -q "^PORTOLAN_PUBLIC_URL=$public_url\$" "$env_file"; then
  printf 'Keeping the existing %s; edit PORTOLAN_PUBLIC_URL there to change the address.\n' "$env_file"
fi
listen=$(sed -n 's/^PORTOLAN_LISTEN=//p' "$env_file" | tail -n 1)
listen=${listen:-127.0.0.1:8088}

# An upgrade keeps the previous binaries and a copy of the stopped database so
# a failed start can be undone.
previous=""
backup=""
if [ -x "$prefix/portolan-panel" ]; then
  previous="$prefix.previous"
  systemctl stop portolan-panel.service 2>/dev/null || true
  rm -rf -- "$previous"
  cp -a "$prefix" "$previous"
  [ ! -f "$unit" ] || cp -p "$unit" "$previous/portolan-panel.service.installed"
  if [ -f "$state_dir/portolan.db" ]; then
    backup="$state_dir/backups/portolan-$(date -u +%Y%m%dT%H%M%SZ).db"
    install -d -o portolan-panel -g portolan-panel -m 0700 "$state_dir/backups"
    cp -p "$state_dir/portolan.db" "$backup"
    [ ! -f "$state_dir/portolan.db-wal" ] || cp -p "$state_dir/portolan.db-wal" "$backup-wal"
  fi
fi

install -d -o root -g root -m 0755 "$prefix" "$prefix/downloads"
install -o root -g root -m 0755 "$source_dir/portolan-panel" "$prefix/portolan-panel"
install -o root -g root -m 0755 "$source_dir/portolan-runtime-import" "$prefix/portolan-runtime-import"
for file in install-agent.sh portolan-agent-linux-amd64 portolan-agent-linux-arm64; do
  install -o root -g root -m 0755 "$source_dir/downloads/$file" "$prefix/downloads/$file"
done
install -o root -g root -m 0644 "$source_dir/portolan-panel.service" "$unit"
systemctl daemon-reload
systemctl enable portolan-panel.service >/dev/null
systemctl restart portolan-panel.service

healthy=false
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if curl --fail --silent --max-time 2 "http://$listen/healthz" >/dev/null 2>&1; then
    healthy=true
    break
  fi
  sleep 1
done
if [ "$healthy" != true ]; then
  if [ -n "$previous" ]; then
    systemctl stop portolan-panel.service || true
    rm -rf -- "$prefix"
    mv "$previous" "$prefix"
    if [ -f "$prefix/portolan-panel.service.installed" ]; then
      mv "$prefix/portolan-panel.service.installed" "$unit"
      systemctl daemon-reload
    fi
    if [ -n "$backup" ]; then
      install -o portolan-panel -g portolan-panel -m 0600 "$backup" "$state_dir/portolan.db"
      rm -f "$state_dir/portolan.db-wal" "$state_dir/portolan.db-shm"
      [ ! -f "$backup-wal" ] || install -o portolan-panel -g portolan-panel -m 0600 "$backup-wal" "$state_dir/portolan.db-wal"
    fi
    systemctl restart portolan-panel.service || true
    die "Portolan $new_version did not become healthy; the previous version and database were restored. Check: journalctl -u portolan-panel"
  fi
  die "the panel did not become healthy. Check: journalctl -u portolan-panel"
fi
rm -rf -- "${previous:-$prefix.previous}"

printf '\nPortolan panel %s is running on %s.\n' "$new_version" "$listen"
if [ "$first_install" = true ]; then
  printf '\nAdministrator token (also stored in %s):\n\n  %s\n\n' "$env_file" "$admin_token"
  printf 'Next: serve %s over HTTPS with a reverse proxy to %s,\n' "$public_url" "$listen"
  printf 'for example with the Caddyfile in docs/deployment.md, then log in.\n'
else
  [ -z "$backup" ] || printf 'The database before the upgrade is kept at %s.\n' "$backup"
fi
