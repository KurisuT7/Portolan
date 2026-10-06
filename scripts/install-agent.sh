#!/bin/sh
set -eu

# Portolan node installer. It intentionally installs into Portolan-owned paths
# and never replaces an existing sing-box, Snell, Realm, GOST or Caddy
# service that belongs to another project.

panel_url=""
enrollment_token=""
agent_url=""
agent_file=""
agent_sha256=""
sing_box_url=""
sing_box_sha256=""
realm_url=""
realm_sha256=""

usage() {
  cat <<'EOF'
Usage:
  install-agent.sh --panel https://panel.example.com --token ONE_TIME_TOKEN \
    (--agent-url HTTPS_URL | --agent-file LOCAL_PATH) --agent-sha256 SHA256 \
    --sing-box-url HTTPS_URL --sing-box-sha256 SHA256 \
    --realm-url HTTPS_URL --realm-sha256 SHA256

The panel's install command fills in every value, including the sing-box and
Realm archives of the versions selected in the panel. The enrollment token is
one-use and expires after 20 minutes. The installer supports Linux x86_64 and
aarch64 with systemd. Running it again upgrades an installed Agent and its
cores. Existing non-Portolan services are never modified.
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
    --panel) need_value "$@"; panel_url=$2; shift 2 ;;
    --token) need_value "$@"; enrollment_token=$2; shift 2 ;;
    --agent-url) need_value "$@"; agent_url=$2; shift 2 ;;
    --agent-file) need_value "$@"; agent_file=$2; shift 2 ;;
    --agent-sha256) need_value "$@"; agent_sha256=$2; shift 2 ;;
    --sing-box-url) need_value "$@"; sing_box_url=$2; shift 2 ;;
    --sing-box-sha256) need_value "$@"; sing_box_sha256=$2; shift 2 ;;
    --realm-url) need_value "$@"; realm_url=$2; shift 2 ;;
    --realm-sha256) need_value "$@"; realm_sha256=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "run as root"
[ -d /run/systemd/system ] || die "systemd is required"
command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"
[ -n "$panel_url" ] || die "--panel is required"
[ -n "$enrollment_token" ] || die "--token is required"
case "$panel_url" in https://*) ;; *) die "--panel must use HTTPS" ;; esac
for checksum in "$agent_sha256" "$sing_box_sha256" "$realm_sha256"; do
  printf '%s' "$checksum" | grep -Eq '^[0-9a-fA-F]{64}$' || die "every --*-sha256 value must be a SHA-256 digest"
done
if [ -n "$agent_url" ] && [ -n "$agent_file" ]; then
  die "use only one of --agent-url or --agent-file"
fi
if [ -z "$agent_url" ] && [ -z "$agent_file" ]; then
  die "one of --agent-url or --agent-file is required"
fi
for url in "$sing_box_url" "$realm_url" ${agent_url:+"$agent_url"}; do
  case "$url" in https://*) ;; *) die "Agent, sing-box and Realm URLs must use HTTPS" ;; esac
done
case "$(uname -m)" in
  x86_64|amd64|aarch64|arm64) ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

tmp_dir=$(mktemp -d /tmp/portolan-install.XXXXXX)
cleanup() { rm -rf -- "$tmp_dir"; }
trap cleanup EXIT HUP INT TERM

download() {
  url=$1
  output=$2
  curl --fail --location --silent --show-error --retry 3 --retry-delay 2 \
    --proto '=https' --proto-redir '=https' --tlsv1.2 --output "$output" "$url"
}

verify() {
  file=$1
  expected=$2
  actual=$(sha256sum "$file" | awk '{print $1}')
  [ "$actual" = "$expected" ] || die "checksum mismatch for $(basename "$file")"
}

printf 'Downloading Agent, sing-box and Realm...\n'
sing_archive="$tmp_dir/sing-box.tar.gz"
download "$sing_box_url" "$sing_archive"
verify "$sing_archive" "$(printf '%s' "$sing_box_sha256" | tr 'A-F' 'a-f')"

realm_archive="$tmp_dir/realm.tar.gz"
download "$realm_url" "$realm_archive"
verify "$realm_archive" "$(printf '%s' "$realm_sha256" | tr 'A-F' 'a-f')"

agent_binary="$tmp_dir/portolan-agent"
if [ -n "$agent_file" ]; then
  [ -f "$agent_file" ] || die "agent file does not exist: $agent_file"
  cp -- "$agent_file" "$agent_binary"
else
  download "$agent_url" "$agent_binary"
fi
verify "$agent_binary" "$(printf '%s' "$agent_sha256" | tr 'A-F' 'a-f')"

mkdir -p "$tmp_dir/sing" "$tmp_dir/realm"
tar -xzf "$sing_archive" -C "$tmp_dir/sing"
tar -xzf "$realm_archive" -C "$tmp_dir/realm"
sing_binary=$(find "$tmp_dir/sing" -type f -name sing-box -print -quit)
realm_binary=$(find "$tmp_dir/realm" -type f -name realm -print -quit)
[ -n "$sing_binary" ] && [ -f "$sing_binary" ] || die "sing-box archive layout is unexpected"
[ -n "$realm_binary" ] && [ -f "$realm_binary" ] || die "Realm archive layout is unexpected"

# The new cores must run on this host and accept the active configuration
# before anything changes.
"$sing_binary" version >/dev/null || die "the new sing-box does not run on this host; nothing was changed"
"$realm_binary" -v >/dev/null || die "the new Realm does not run on this host; nothing was changed"
active=/etc/portolan/runtime/current/sing-box
if [ -f "$active/00-base.json" ]; then
  "$sing_binary" check -c "$active/00-base.json" -C "$active/conf.d" ||
    die "the new sing-box rejects the active configuration; nothing was changed"
fi
same_file() { [ -f "$2" ] && [ "$(sha256sum <"$1")" = "$(sha256sum <"$2")" ]; }
sing_changed=true
realm_changed=true
if same_file "$sing_binary" /usr/local/lib/portolan/sing-box; then sing_changed=false; fi
if same_file "$realm_binary" /usr/local/lib/portolan/realm; then realm_changed=false; fi

if ! getent group portolan >/dev/null 2>&1; then
  groupadd --system portolan
fi
if ! id portolan >/dev/null 2>&1; then
  useradd --system --gid portolan --home-dir /nonexistent --shell /usr/sbin/nologin portolan
fi

install -d -o root -g root -m 0755 /usr/local/lib/portolan
install -d -o root -g portolan -m 2770 /etc/portolan /etc/portolan/runtime
install -o root -g root -m 0755 "$agent_binary" /usr/local/lib/portolan/portolan-agent
install -o root -g root -m 0755 "$sing_binary" /usr/local/lib/portolan/sing-box
install -o root -g root -m 0755 "$realm_binary" /usr/local/lib/portolan/realm

cat >/etc/systemd/system/portolan-agent.service <<'EOF'
[Unit]
Description=Portolan desired-state agent
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=root
Group=root
UMask=0027
ExecStart=/usr/local/lib/portolan/portolan-agent run --config /etc/portolan/agent.json
Restart=always
RestartSec=5s
TimeoutStopSec=20s
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=/etc/portolan /usr/local/lib/portolan
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
LockPersonality=yes
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
[Install]
WantedBy=multi-user.target
EOF

cat >/etc/systemd/system/portolan-sing-box.service <<'EOF'
[Unit]
Description=Portolan managed sing-box core
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=portolan
Group=portolan
UMask=0027
ExecStartPre=/usr/local/lib/portolan/sing-box check -c /etc/portolan/runtime/current/sing-box/00-base.json -C /etc/portolan/runtime/current/sing-box/conf.d
ExecStart=/usr/local/lib/portolan/sing-box run -c /etc/portolan/runtime/current/sing-box/00-base.json -C /etc/portolan/runtime/current/sing-box/conf.d
Restart=on-failure
RestartSec=3s
LimitNOFILE=1048576
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=strict
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
LockPersonality=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
[Install]
WantedBy=multi-user.target
EOF

cat >/etc/systemd/system/portolan-realm@.service <<'EOF'
[Unit]
Description=Portolan Realm forwarding instance %i
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=portolan
Group=portolan
UMask=0027
ExecStart=/usr/local/lib/portolan/realm -c /etc/portolan/runtime/current/realm/%i.toml
Restart=on-failure
RestartSec=2s
LimitNOFILE=1048576
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=strict
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
LockPersonality=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
[Install]
WantedBy=multi-user.target
EOF

printf 'Enrolling Portolan Agent...\n'
/usr/local/lib/portolan/portolan-agent enroll --panel "$panel_url" --token "$enrollment_token" --config /etc/portolan/agent.json
chown root:root /etc/portolan/agent.json
chmod 0600 /etc/portolan/agent.json

systemctl daemon-reload
# Running cores keep executing a replaced binary until they restart. The new
# enrollment revoked the previous Agent token, so it cannot fetch a job meanwhile.
if [ "$sing_changed" = true ] && systemctl is-active --quiet portolan-sing-box.service; then
  systemctl restart portolan-sing-box.service
fi
if [ "$realm_changed" = true ]; then
  systemctl list-units --type=service --state=active --plain --no-legend 'portolan-realm@*' |
    while read -r unit _; do systemctl restart "$unit"; done
fi
systemctl enable portolan-agent.service
# `enable --now` does not restart an already running Agent after a reinstall.
# Always restart so the process loads the newly enrolled server ID and token.
systemctl restart portolan-agent.service
printf '\nPortolan Agent is installed. Existing proxy services were not modified.\n'
/usr/local/lib/portolan/sing-box version | head -n 1
/usr/local/lib/portolan/realm -v
printf 'Check status: systemctl status portolan-agent --no-pager\n'
