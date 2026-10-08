# 部署

本文说明面板的两种安装方式、反向代理、配置、节点服务器、备份、升级和卸载。快速上手见
[README](../README.md)。

## 安装脚本

`install-panel.sh` 支持使用 systemd 的 x86_64 和 aarch64 Linux，需要 root、`curl`、`tar`
和 `sha256sum`。

```bash
curl -fsSLO https://github.com/KurisuT7/Portolan/releases/latest/download/install-panel.sh
sudo sh install-panel.sh --public-url https://panel.example.com
```

| 参数 | 作用 |
| --- | --- |
| `--public-url URL` | 面板的 HTTPS 地址，写进配置，用在 Agent 安装命令里。首次安装必填。 |
| `--version VERSION` | 安装指定版本，例如 `v0.1.0`。默认是脚本所属的版本。 |
| `--archive FILE` | 从已下载的发布包安装，不访问 GitHub。 |

脚本从 GitHub Releases 下载与本机架构对应的发布包，按同一版本的 `SHA256SUMS` 校验后安装：

| 路径 | 内容 |
| --- | --- |
| `/usr/local/lib/portolan-panel/portolan-panel` | 面板，网页控制台已内嵌 |
| `/usr/local/lib/portolan-panel/portolan-runtime-import` | [离线恢复](runtime-recovery.md)工具 |
| `/usr/local/lib/portolan-panel/downloads/` | 提供给节点服务器的 Agent（amd64、arm64）和安装器 |
| `/etc/portolan-panel/panel.env` | 配置和密钥，`root` 所有，权限 `0600` |
| `/var/lib/portolan-panel/` | 数据库 `portolan.db`、核心压缩包 `cores/`、升级前的数据库副本 `backups/` |
| `/etc/systemd/system/portolan-panel.service` | systemd 服务，以 `portolan-panel` 系统账号运行 |

首次安装时脚本生成主密钥和管理员令牌，并在结束时打印管理员令牌。面板默认监听
`127.0.0.1:8088`，不直接暴露在公网。检查运行状态：

```bash
systemctl status portolan-panel
curl http://127.0.0.1:8088/healthz
```

### 无法访问 GitHub 时

在能访问 GitHub 的机器上下载对应架构的发布包和 `SHA256SUMS`，核对后复制到面板服务器：

```bash
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf portolan_0.1.1_linux_amd64.tar.gz
sudo sh portolan_0.1.1_linux_amd64/install-panel.sh --public-url https://panel.example.com
```

从解压目录运行时，脚本安装该目录里的文件。

## Docker

镜像 `ghcr.io/kurisut7/portolan` 提供 `linux/amd64` 和 `linux/arm64`，标签与发布版本一致
（如 `0.1.0`），`latest` 指向最新正式版。

```bash
mkdir portolan && cd portolan
curl -fsSLO https://raw.githubusercontent.com/KurisuT7/Portolan/v0.1.1/compose.yaml
printf 'PORTOLAN_MASTER_KEY=%s\nPORTOLAN_ADMIN_TOKEN=%s\nPORTOLAN_PUBLIC_URL=https://panel.example.com\n' \
  "$(openssl rand -base64 32)" "$(openssl rand -base64 32)" > panel.env
chmod 600 panel.env
docker compose up -d
docker compose logs -f
```

- 容器以 UID 65532 运行，数据保存在命名卷 `portolan-data`（挂载到
  `/var/lib/portolan-panel`）。改用宿主机目录时，先把目录属主设为 `65532:65532`。
- `compose.yaml` 使用宿主机网络，面板监听宿主机的 `127.0.0.1:8088`，由宿主机上的反向代理
  访问。不使用宿主机网络时，设置 `PORTOLAN_LISTEN=0.0.0.0:8088`，只把端口映射到
  `127.0.0.1`，并把 Docker 网桥网关的地址段写进 `PORTOLAN_TRUSTED_PROXIES`，否则面板
  看到的来源地址都是网关。
- 升级：修改 `compose.yaml` 里的镜像标签，运行 `docker compose up -d`。

## 反向代理

面板必须通过 HTTPS 访问：登录 Cookie 只在 HTTPS 下发送，Agent 也只接受 HTTPS 面板地址。
Agent 用最长 60 秒的长轮询等待任务，代理的读取超时要大于 60 秒。

Caddy 会自动申请证书：

```caddyfile
panel.example.com {
	encode zstd gzip
	reverse_proxy 127.0.0.1:8088
	header Strict-Transport-Security "max-age=31536000"
}
```

Nginx（证书自行配置）：

```nginx
server {
    listen 443 ssl;
    server_name panel.example.com;
    ssl_certificate     /etc/ssl/panel.example.com/fullchain.pem;
    ssl_certificate_key /etc/ssl/panel.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8088;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 90s;
    }
}
```

面板只信任本机代理附加的 `X-Forwarded-For`，从右往左取第一个不受信任的地址作为客户端
地址，用于登录限速、日志和 Agent 注册。代理前面还有 CDN 时，把 CDN 的地址段同时加入
代理和 `PORTOLAN_TRUSTED_PROXIES` 的信任列表，否则面板记录到的是 CDN 节点的地址，
同一个 CDN 节点后面的所有人会共用一个登录失败计数。

## 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `PORTOLAN_MASTER_KEY` | 无，必填 | 32 字节随机数的 Base64（`openssl rand -base64 32`），加密数据库里的协议密钥。丢失后已保存的节点密钥无法恢复。 |
| `PORTOLAN_ADMIN_TOKEN` | 无，必填 | 管理员令牌，至少 32 个字符且至少 8 种不同字符。修改后重启面板生效，所有会话随之失效。 |
| `PORTOLAN_PUBLIC_URL` | 按请求推断 | 面板的 HTTPS 地址，用在 Agent 安装命令和两步验证的账户名里。 |
| `PORTOLAN_LISTEN` | `127.0.0.1:8088` | 监听地址。 |
| `PORTOLAN_DATABASE` | `portolan.db` | SQLite 数据库路径。核心压缩包保存在同目录的 `cores/` 下，该目录必须可写。安装脚本和镜像设为 `/var/lib/portolan-panel/portolan.db`。 |
| `PORTOLAN_DOWNLOADS_DIR` | 可执行文件旁的 `downloads/` | 提供给节点的 Agent 和安装器所在目录。未找到时面板不能生成安装命令。 |
| `PORTOLAN_GEOIP_DB` | 不启用 | 本地 MMDB 文件路径，用来识别服务器地区。 |
| `PORTOLAN_TRUSTED_PROXIES` | 仅本机 | 额外信任的反向代理，逗号分隔的地址或 CIDR。 |
| `PORTOLAN_SECURE_COOKIES` | `true` | 只在本机用 HTTP 测试时设为 `false`。 |

面板会从 `api.github.com` 读取 sing-box 和 Realm 的版本列表，并从 GitHub 下载发布文件。
未登录 GitHub 的 API 每小时有请求次数限制，选择版本失败时稍后重试即可。

### 地区识别（可选）

配置 GeoLite2/GeoIP2 City 或 DB-IP City Lite 的 MMDB 文件后，Agent 注册时面板在本地查询
服务器地区，不调用第三方接口，也不会覆盖手动修改过的地区。使用 DB-IP Lite（CC BY 4.0）时，
控制台页脚自动显示所要求的署名。更新 MMDB 文件后重启面板生效。

## 节点服务器

在面板「添加服务器」或「重装 Agent」得到的命令会从面板下载安装器、Agent 和选定版本的
sing-box、Realm，逐个按 SHA-256 校验。安装内容：

- `/usr/local/lib/portolan/`：Agent、sing-box、Realm 二进制；
- `/etc/portolan/`：Agent 凭据 `agent.json`（`root` 所有，`0600`）和版本化配置 `runtime/`；
- `portolan-agent.service`（root 运行）、`portolan-sing-box.service` 和
  `portolan-realm@<转发 ID>.service`（以 `portolan` 系统账号运行）。

统计节点和转发的流量需要 `nft` 命令。缺少时安装器用系统的包管理器（apt、dnf、yum、apk、zypper 或 pacman）安装 `nftables` 包，但不启用该包自带的 nftables 服务，主机的防火墙规则不变。安装失败时只统计服务器整机流量，之后装好 `nft` 命令，Agent 会自动开始统计端口流量。

安装器不会升级系统软件包，也不会修改、停止或删除其他服务。重新运行安装命令即升级 Agent：
安装器先确认新的 sing-box 能运行并接受当前配置，再替换二进制并重启变化的服务。

卸载节点上的 Portolan（会停止该服务器上 Portolan 管理的全部节点和转发），然后在面板里删除
这台服务器：

```bash
systemctl disable --now portolan-agent.service portolan-sing-box.service
for unit in /etc/systemd/system/multi-user.target.wants/portolan-realm@*.service; do
  [ -e "$unit" ] && systemctl disable --now "$(basename "$unit")"
done
rm -f /etc/systemd/system/portolan-agent.service /etc/systemd/system/portolan-sing-box.service \
  /etc/systemd/system/portolan-realm@.service
systemctl daemon-reload
nft delete table inet portolan 2>/dev/null || true
rm -rf /etc/portolan /usr/local/lib/portolan
userdel portolan
```

## 备份与恢复

面板备份需要同时保存：

- 数据库：停止面板后复制 `portolan.db`（以及存在时的 `portolan.db-wal`），或者在运行中用
  `sqlite3 /var/lib/portolan-panel/portolan.db ".backup /path/to/portolan.db"` 生成一致的副本；
- `panel.env`，尤其是 `PORTOLAN_MASTER_KEY`。

恢复时停止面板，放回数据库和 `panel.env`，确认数据库属于 `portolan-panel` 且权限为
`0600`，再启动。只有数据库而没有原主密钥时，节点密钥无法解密。两者都丢失、但服务器仍在
运行时，按 [离线恢复](runtime-recovery.md) 重建。

## 升级与回退

再次运行最新的 `install-panel.sh` 即升级。脚本停止面板，把当前二进制和数据库复制一份，
安装新版本并检查 `/healthz`；新版本没有正常启动时自动换回原来的二进制、服务文件和数据库。
升级成功后，升级前的数据库副本仍保留在 `/var/lib/portolan-panel/backups/`，确认无误后可以
删除。

需要回到旧版本时运行 `sudo sh install-panel.sh --version v0.1.0`（换成目标版本）。如果新版本
已经改动过数据库，再用 `backups/` 里升级前的副本恢复。

面板升级后，在每台服务器详情里点「重装 Agent」并运行新命令，让 Agent 与面板版本一致。

## 丢失两步验证设备

在面板服务器上关闭两步验证，然后用管理员令牌登录，重新开启：

```bash
sudo -u portolan-panel /usr/local/lib/portolan-panel/portolan-panel disable-totp \
  --database /var/lib/portolan-panel/portolan.db
```

Docker：

```bash
docker compose exec portolan /usr/local/lib/portolan-panel/portolan-panel disable-totp
```

## 卸载面板

```bash
systemctl disable --now portolan-panel
rm -f /etc/systemd/system/portolan-panel.service
systemctl daemon-reload
rm -rf /usr/local/lib/portolan-panel
# 以下会删除配置、密钥和数据库，先确认已经备份：
rm -rf /etc/portolan-panel /var/lib/portolan-panel
userdel portolan-panel
```

## 常见问题

- **安装命令提示面板尚未选择版本**：先在「服务器 → 核心版本」为 sing-box 和 Realm 选择版本。
- **登录提示失败次数过多**：同一来源 15 分钟内失败 5 次会被锁定 15 分钟。失败记录在日志里：
  `journalctl -u portolan-panel | grep "administrator login"`。
- **服务器一直显示待安装或离线**：确认服务器能访问 `PORTOLAN_PUBLIC_URL`，查看
  `journalctl -u portolan-agent`。
