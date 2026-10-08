# 安全模型

## 管理员登录

- 管理员令牌至少 32 个字符且至少 8 种不同字符，安装脚本生成 256 位随机令牌。令牌只用于换取
  12 小时的会话，比较时使用常量时间。
- 会话 Cookie 带 HttpOnly 和 SameSite=Strict；通过 HTTPS 访问时还带 Secure 和 `__Host-` 前缀。
  会话只保存在内存里，最多 32 个，重启面板后全部失效。
- 所有非只读管理请求还要求会话绑定的 CSRF Token。
- 同一来源（IPv4 地址或 IPv6 /64）15 分钟内登录失败 5 次，锁定 15 分钟。登录成功、失败和
  被拒绝都写入日志，包含来源地址和原因，可以接入 fail2ban 等工具。
- 可选 TOTP 两步验证（RFC 6238：SHA-1、6 位、30 秒）。密钥用主密钥加密保存；每个验证码只能
  使用一次；所有来源合计 15 分钟内输错 10 次验证码，两步验证锁定 15 分钟。开启时结束其他
  会话，关闭时需要当前验证码。丢失验证器时在面板主机上运行 `portolan-panel disable-totp`。
- 来源地址只采信本机反向代理或 `PORTOLAN_TRUSTED_PROXIES` 中代理附加的 `X-Forwarded-For`，
  客户端自己发送的值不会被当作来源。
- 控制台页面的 Content-Security-Policy 只允许本站脚本和构建时确定的内联脚本哈希，并禁止被
  其他页面嵌入；API 响应同样禁止嵌入。

## Agent 身份与传输

- Agent 注册令牌随机生成、20 分钟过期且仅可使用一次；注册后换成独立的 48 字节随机 Agent
  Token。数据库只保存令牌的哈希。
- Agent 只接受 HTTPS 面板地址；不安全 HTTP 仅能在显式参数开启时用于 loopback 测试。
- Agent 不提供入站管理 API，不接收任意命令或 Shell 脚本。

## 密钥与数据

- Reality 私钥、Shadowsocks 密码、Snell PSK 和待下发任务使用 AES-256-GCM 加密后存入 SQLite。
- 主密钥只从 `PORTOLAN_MASTER_KEY` 环境读取，不进入数据库或仓库。
- 客户端链接按需解封；列表 API 不返回敏感规格。
- 服务配置文件为 root 写入、`portolan` 组只读；Agent 凭据保持 `0600 root:root`。
- sing-box 日志级别为 `warn`，不把每条代理连接的目标地址写入各服务器的日志。
- Agent 只上报生效 revision、Agent 与核心的版本、Portolan 自有 systemd 服务的状态，以及网卡和端口的累计字节数，不上报进程输出或连接的目标地址。

## 供应链

- 安装器只使用 HTTPS，启用 `curl --fail`，不包含 `--no-check-certificate`。
- sing-box 和 Realm 的版本由管理员在面板选择，只列出并接受 GitHub 官方正式版（sing-box 1.14.0、Realm 2.9.4 及以上）。面板按 GitHub 为每个发布文件公布的 SHA-256 校验后才保存；没有官方摘要或摘要不符的版本不能选用。安装器和 Agent 从面板下载时再按同一摘要校验。
- 核心不会自动更新。更新时 Agent 先用新 sing-box 对当前生效配置执行 `sing-box check`，通过才替换；重启后服务没有保持运行或没有持有端口，就换回原二进制。Realm 没有配置校验命令，只依赖重启后的检查。
- 节点安装器要求显式的 Agent 下载地址（或本地文件）和预期 SHA-256，不会下载"最新版"；面板生成的安装命令会填好这些值。
- Agent 不会自动更新。管理员下发更新后，Agent 只从面板下载随面板发布的 Agent 二进制，按任务中的 SHA-256 校验，确认新版本能运行并通过面板认证后才替换自身；新版本 3 分钟内没有连上面板就换回原二进制。
- 面板发布包的 `SHA256SUMS` 随 GitHub Release 发布，安装脚本下载发布包后先校验再安装。

## 主机隔离

- Portolan 使用自己的二进制目录、配置目录、用户、服务名和 runtime，不接管其他脚本安装的服务。
- Agent 只从固定的常见路径读取常规配置文件，拒绝符号链接和超过 4 MiB 的文件；发现的外部节点经协议校验后加密入库，并永不下发回主机。
- sing-box 与 Realm 以非 root `portolan` 用户运行，仅保留绑定低端口所需的 `CAP_NET_BIND_SERVICE`。
- systemd 单元启用 `NoNewPrivileges`、只读系统、私有临时目录、内核与控制组保护等限制。
- Agent 需要写配置并调用 systemd，因此以 root 运行；写路径限制为 `/etc/portolan` 和存放 Agent 与核心二进制的 `/usr/local/lib/portolan`。
- 流量计数使用独立的 nftables 表 `inet portolan`：链的策略为接受，优先级排在常规过滤之后，只有计数规则，不改变其他防火墙规则的结果，也不统计被它们丢弃的包。Agent 只写入这张表，不读取或修改其他表。节点安装器在缺少 `nft` 命令时安装发行版的 `nftables` 包，不启用它自带的 nftables 服务。

## 明确的不安全能力

- Shadowsocks `none`
- Snell v6 `unsafe-raw`

这两项默认关闭，模型验证、API 与 UI 都要求显式确认。它们只适合已经受保护的受控链路。
