# 协议矩阵

Portolan 的协议模型以 sing-box 官方配置结构为准，不复用第三方脚本的配置文件格式。字段与行为以官方 [sing-box Changelog](https://sing-box.sagernet.org/changelog/) 与各协议文档为来源。

## VLESS Reality

服务器端使用 sing-box VLESS 入站和 Reality TLS：

- 传输：TCP 或 gRPC。Xray 系客户端只接受 RAW/TCP、XHTTP 和 gRPC 上的 REALITY，因此 Portolan 不创建 HTTP、WebSocket、HTTP Upgrade 传输；Agent 发现的外部节点按原配置展示和导出。
- Flow：空值（标准）或 `xtls-rprx-vision`；Vision 只能用于 TCP。
- 可管理字段：握手目标、握手端口、SNI、Fingerprint、Short ID、最大时间差和 gRPC Service Name
- 自动生成：UUID、X25519 Reality 密钥对、Short ID
- 导出：标准 `vless://` 分享链接，参数名按分享链接约定使用 camelCase（如 `serviceName`）。

握手目标和 SNI 必须是同一个合法域名而非 IP；Portolan 不随机抓取所谓“可用 SNI”。默认值是 `aws.amazon.com`，面板拒绝把 Cloudflare 域名保存为 Reality 目标。默认值不是万能答案：正式使用前应从实际部署的服务器验证目标可达、支持 TLS 1.3 和 HTTP/2、证书链正确且不会重定向。具体字段与上游行为以官方 [VLESS 入站](https://sing-box.sagernet.org/configuration/inbound/vless/)、[TLS/Reality](https://sing-box.sagernet.org/configuration/shared/tls/) 和 [XTLS REALITY 目标筛选说明](https://github.com/XTLS/REALITY/blob/main/README.en.md) 为准。

## Shadowsocks

支持 sing-box 官方 [Shadowsocks 入站](https://sing-box.sagernet.org/configuration/inbound/shadowsocks/) 当前列出的全部方法：

- `2022-blake3-aes-128-gcm`
- `2022-blake3-aes-256-gcm`
- `2022-blake3-chacha20-poly1305`
- `aes-128-gcm`
- `aes-192-gcm`
- `aes-256-gcm`
- `chacha20-ietf-poly1305`
- `xchacha20-ietf-poly1305`
- `none`

密码会按方法生成；2022 方法使用匹配密钥长度的随机材料。`none` 没有加密，必须在 UI 和 API 中同时显式设置 `allow_insecure`。

导出遵循 [SIP002](https://shadowsocks.org/doc/sip002.html)：2022 方法的 `method:password` 使用百分号编码，其他方法使用 Base64URL。

## Snell

Snell 使用 sing-box 官方 [Snell 入站](https://sing-box.sagernet.org/configuration/inbound/snell/)：

- v5：`none` / `http` 混淆；不支持 QUIC Proxy。
- v6：`default` / `unshaped` / `unsafe-raw`。
- PSK：随机生成，长度保持在官方允许的 12–255 字节内。
- `unsafe-raw` 必须显式设置 `allow_insecure`。
- 导出：Surge 配置行，v5 的 HTTP 混淆写作 `obfs=http`，v6 的非默认模式写作 `mode=`（参见 [Surge Snell 参数](https://manual.nssurge.com/policies/snell.html)）。

Snell 入站自 sing-box 1.14.0 正式版提供，与 Reality、Shadowsocks 使用同一个核心；面板只允许把 sing-box 设为 1.14.0 及以上的正式版。sing-box 不实现 Snell v5 的 QUIC Proxy 模式。

## 增加协议方法

协议方法和生成逻辑集中在 `internal/model` 与 `internal/configgen`，UI 只提交声明式规格。模型描述 sing-box 能表达、外部节点可能使用的配置；只针对 Portolan 新建节点的限制（如 Reality 传输）在 `internal/api` 的创建入口校验。增加新方法时必须同时更新：

1. 模型白名单与安全门槛。
2. 配置生成单测。
3. 客户端导出。
4. 使用固定官方二进制的集成测试矩阵。
5. UI 选项和本文件。
