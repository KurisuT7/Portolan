# Portolan

[![CI](https://github.com/KurisuT7/Portolan/actions/workflows/ci.yml/badge.svg)](https://github.com/KurisuT7/Portolan/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Portolan 是一个自托管的代理节点与中转控制面。在一个网页控制台里管理多台 Linux 服务器上的 VLESS Reality、Shadowsocks、Snell 节点和端口转发：每台服务器运行一次安装命令，之后的建节点、改转发、导出客户端配置和升级核心都在面板里完成。

> 当前版本是 Technical Preview。关键路径有自动化测试，但还没有经过大规模、长时间的使用。升级前请备份面板数据，生产环境先在一台服务器上试用。

界面目前只有中文。

## 功能

- **一次安装接入服务器**：面板生成一行安装命令，Agent 主动出站连接面板，服务器上不需要开放管理端口。
- **协议节点**：VLESS Reality（TCP、Vision、gRPC）、Shadowsocks（2022 系列与 AEAD）、Snell v5/v6，全部由 sing-box 承载。密钥、UUID、端口由面板生成。
- **端口转发**：入口端口转到协议节点、另一台服务器的端口或自定义地址；默认每条转发一个独立的 Realm 进程，也可选 sing-box。
- **客户端导出**：标准 `vless://`、`ss://` 分享链接和 Surge 的 Snell 配置行；经转发入口访问时自动换成入口地址和端口。
- **安全应用配置**：Agent 先用真实 sing-box 校验新配置再原子切换，服务没有正常运行或没有占用端口时自动恢复上一版本。
- **核心版本管理**：在面板选择 sing-box 和 Realm 的官方版本，面板按 GitHub 公布的 SHA-256 校验后分发，可逐台或一次更新全部服务器。
- **识别已有节点**：Agent 只读扫描服务器上已有的 sing-box 和 Snell 配置，作为"外部只读"节点显示和导出，不会改写或删除它们。
- **链路检测**：入口服务器定期测量到目标的 TCP 建连延迟、抖动和失败率，保留七天历史。
- **登录保护**：管理员令牌登录，失败过多按来源临时锁定，可选 TOTP 两步验证；协议密钥在数据库中加密保存。

## 工作方式

```text
浏览器 ──HTTPS──▶ 反向代理 ──▶ Portolan 面板（API + 控制台，SQLite）
                                   ▲
                 服务器上的 Agent ──┘ 主动出站 HTTPS 长轮询
                   │
                   ├─ sing-box：所有协议节点和 sing-box 转发
                   └─ Realm：每条 Realm 转发一个 systemd 实例
```

面板只保存期望状态，Agent 只执行结构化的配置任务，不接受任意命令。详细设计见 [docs/architecture.md](docs/architecture.md)。

## 准备

**面板服务器**

- Linux（systemd，x86_64 或 aarch64），或任何能运行 Docker 的 Linux 主机。
- 一个指向面板服务器的域名，并用 Caddy、Nginx 等反向代理提供 HTTPS。Agent 只接受 HTTPS 面板地址。
- 面板能访问 `api.github.com` 和 GitHub 的发布文件下载地址，用来获取 sing-box 和 Realm。节点服务器不需要访问 GitHub。

**节点服务器**

- 使用 systemd 的 Linux，x86_64 或 aarch64（Debian、Ubuntu 等），有 root 权限，装有 `curl`、`tar`、`sha256sum`。
- 能通过 HTTPS 访问面板地址。

## 安装面板

### 方式一：安装脚本

在面板服务器上以 root 运行，把 `panel.example.com` 换成你的域名：

```bash
curl -fsSLO https://github.com/KurisuT7/Portolan/releases/latest/download/install-panel.sh
sudo sh install-panel.sh --public-url https://panel.example.com
```

脚本会下载对应架构的发布包并按 `SHA256SUMS` 校验，创建 `portolan-panel` 系统用户和 systemd 服务，生成主密钥和管理员令牌，最后打印管理员令牌。面板只监听 `127.0.0.1:8088`。

然后让反向代理把你的域名转到面板。以 Caddy 为例，在 Caddyfile 里加入：

```caddyfile
panel.example.com {
	reverse_proxy 127.0.0.1:8088
}
```

重新加载 Caddy 后打开 `https://panel.example.com`。Nginx 配置、各文件位置和离线安装方法见 [docs/deployment.md](docs/deployment.md)。

### 方式二：Docker Compose

```bash
mkdir portolan && cd portolan
curl -fsSLO https://raw.githubusercontent.com/KurisuT7/Portolan/v0.1.0/compose.yaml
printf 'PORTOLAN_MASTER_KEY=%s\nPORTOLAN_ADMIN_TOKEN=%s\nPORTOLAN_PUBLIC_URL=https://panel.example.com\n' \
  "$(openssl rand -base64 32)" "$(openssl rand -base64 32)" > panel.env
chmod 600 panel.env
docker compose up -d
```

容器使用宿主机网络，同样只监听 `127.0.0.1:8088`，反向代理配置与方式一相同。管理员令牌就是 `panel.env` 里 `PORTOLAN_ADMIN_TOKEN` 的值。

> `PORTOLAN_MASTER_KEY` 用来加密数据库里的协议密钥。丢失它，已保存的节点密钥就无法恢复。请把它和数据库一起备份。

## 第一次使用

1. 打开面板，用管理员令牌登录。
2. 在「服务器 → 核心版本」里为 sing-box 和 Realm 各选一个版本。面板会下载两种架构的压缩包并校验，之后才能生成安装命令。
3. 在「服务器 → 添加服务器」填一个名称，复制生成的安装命令，以 root 在节点服务器上运行。命令 20 分钟内有效，只能用一次。
4. 服务器显示"在线"后，在「节点 → 添加节点」选择服务器和协议。创建后复制分享链接，或复制 Surge 配置行。
5. 需要中转时，在「转发 → 添加转发」选择入口服务器、入口端口和目标节点或端口。
6. 建议点右上角的盾牌图标开启两步验证。

## 升级

- **面板**：用安装时的两条命令重新下载并运行最新的 `install-panel.sh`（可以省略 `--public-url`）。配置和数据保留，升级前的数据库副本保存在 `/var/lib/portolan-panel/backups/`；新版本启动失败时脚本会自动恢复原版本。Docker 用户修改 `compose.yaml` 里的镜像版本后运行 `docker compose up -d`。
- **Agent**：面板升级后，服务器详情会提示 Agent 版本不一致。点「重装 Agent」生成新命令，在该服务器上运行一次。
- **sing-box 和 Realm**：在「核心版本」里换目标版本，先在一台服务器的详情里更新试用，再点「全部更新」。

## 配置

面板从环境变量读取配置，安装脚本写在 `/etc/portolan-panel/panel.env`：

| 变量 | 说明 |
| --- | --- |
| `PORTOLAN_MASTER_KEY` | 必填。32 字节随机数的 Base64，加密协议密钥。 |
| `PORTOLAN_ADMIN_TOKEN` | 必填。管理员令牌，至少 32 个字符。 |
| `PORTOLAN_PUBLIC_URL` | 面板的 HTTPS 地址，用在 Agent 安装命令里。 |
| `PORTOLAN_LISTEN` | 监听地址，默认 `127.0.0.1:8088`。 |
| `PORTOLAN_GEOIP_DB` | 可选。本地 MMDB 文件路径，用来自动识别服务器地区。 |
| `PORTOLAN_TRUSTED_PROXIES` | 可选。除本机外还信任哪些反向代理的 `X-Forwarded-For`。 |

完整列表和说明见 [docs/deployment.md](docs/deployment.md#环境变量)。

## 不支持的内容

- 多用户、订阅、流量统计和计费。Portolan 管理的是你自己的服务器，不是面向用户售卖代理服务的后台。
- Windows、macOS 和非 systemd 的 Linux 节点。
- 在服务器上执行任意命令，或接管其他脚本安装的服务。
- 内核级转发（nftables/iptables）和 GOST 这类隧道链路。
- 默认的 Realm 构建在一端关闭时会关闭整条 TCP 连接；依赖 TCP 半关闭的服务请选 sing-box 转发，见 [docs/forwarding.md](docs/forwarding.md)。

## 文档

- [架构](docs/architecture.md)：期望状态、Agent 应用流程、运行状态与核心更新。
- [部署](docs/deployment.md)：安装细节、反向代理、环境变量、备份、升级和卸载。
- [协议](docs/protocol-matrix.md) 与 [转发](docs/forwarding.md)：支持的协议参数、导出格式和转发引擎的取舍。
- [安全模型](docs/security.md)：登录、密钥、供应链和主机隔离。
- [离线恢复](docs/runtime-recovery.md)：面板数据丢失但服务器还在运行时如何重建。

## 参与开发

开发环境、测试和提交约定见 [CONTRIBUTING.md](CONTRIBUTING.md)。安全问题请按 [SECURITY.md](SECURITY.md) 私下报告。

## 许可证

[MIT](LICENSE)。发布包附带的第三方组件许可见各发布包中的 `THIRD_PARTY_LICENSES`。

Portolan 与 sing-box、Realm、Xray、Surge 和 Snell 的作者没有关联，这些名称只用来说明兼容的软件和协议。
