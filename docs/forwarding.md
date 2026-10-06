# 转发设计

Portolan 提供两个明确的转发层级，不把所有需求塞进一个复杂引擎。

| 引擎 | 适用场景 | 隔离粒度 | TCP/UDP | 更新影响 |
|---|---|---:|---:|---|
| Realm（默认） | 转发到协议节点或普通服务端口 | 每条转发一个 systemd 实例 | 支持 | 仅重启变化的实例 |
| sing-box `direct` 入站 | 依赖 TCP 半关闭的服务 | 与协议核心同进程 | 支持 | 配置变化会重启共享进程，打断本机所有节点的连接 |

## 两种目标

- **协议节点**：入口端口直接转到 Portolan 已管理或已发现的 Reality、Shadowsocks、Snell 节点；控制面自动解析节点所在服务器和监听端口。
- **服务器端口**：入口端口直接转到另一台服务器的任意端口，不需要为了占位而创建协议节点。可以选择 Portolan 服务器以跟随其地址变化，也可以填写自定义 IPv4、IPv6 或域名。

多跳时可以先创建 `B:入口端口 → C:节点/端口`，再创建 `A:入口端口 → B:入口端口`。第二条使用“服务器端口”目标，因此 B 不需要额外的虚假节点。

## 为什么默认 Realm

[Realm](https://github.com/zhboner/realm) 专注网络转发，配置小、运行开销低。Portolan 不维护一个巨大的 Realm 总配置，而是为每条转发生成独立 TOML 和 `portolan-realm@<id>.service`。配置比较以实例为单位，增删或修改一条线路不会重启其他转发，也不会打断协议节点。Realm 2.9.4 提供 Linux 零拷贝和批量 UDP，是独立四层转发的性能导向选项，但没有同条件证据证明它在所有场景下都快于 sing-box。

官方默认构建启用了 `brutal-shutdown`：一端 FIN 会关闭双向连接。转发到协议节点时客户端协议自己管理流的结束，不受影响；依赖 TCP 半关闭的服务应改用 sing-box。修改引擎需要编辑转发规则，面板不会自动切换已有规则的引擎。

## 什么时候用 sing-box

sing-box 官方 [Direct 入站](https://sing-box.sagernet.org/configuration/inbound/direct/) 保留标准 TCP 双向关闭语义，并复用协议核心，不增加常驻进程。代价是它和本机协议节点共用一个进程：这类转发的任何变化都会重启 sing-box，短暂打断本机所有节点的现有连接。

## 引擎比较

公开的基准测试都针对特定负载，不能得出通用排名。下表只比较转发语义、部署隔离和文档说明的性能机制。

| 引擎 | 依据 | 适用性 |
| --- | --- | --- |
| Realm（Rust） | Linux 零拷贝 IO；默认构建启用批量 UDP 和多线程 | 适合专用 TCP/UDP 中转；每条规则独立进程，更新影响小 |
| sing-box | 官方 direct 入站支持 TCP/UDP 目标覆盖；同时承载协议入站 | 保留标准 TCP 语义；共享进程，更新时需要明确影响范围 |
| GOST | TCP/UDP 转发、传输链、动态配置和 API | 面向更复杂的隧道和链路需求；没有证据表明它在纯转发吞吐上更快 |
| rathole、frp | 客户端/服务端反向隧道，用于内网穿透 | 拓扑不同；rathole 与 frp 的回环基准不能用来比较直连中转 |
| nftables flowtable | 内核快速路径、NAT 状态、可选硬件卸载 | 适合路由层转发；会改动主机防火墙和路由，不适合作为自动替换方案 |

面板和 Agent 用 Go 编写，但流量不经过它们，换语言不会提升转发吞吐。Portolan 不自行实现转发程序，也不发布自定义构建的 Realm。

## 没有采用的方式

GOST 适合加密隧道、反向代理、多跳链路和更复杂的传输链。它与“入口端口到节点源站”的常见需求不是同一层复杂度，因此不作为通用端口转发的依赖。

内核 NAT（nftables/iptables）在纯 IP 转发上性能很好，但它改变主机全局网络状态，回滚和隔离成本更高。Portolan 不写防火墙规则。

资料来源：

- [Realm 2.9.4 构建特性](https://github.com/zhboner/realm/blob/v2.9.4/Cargo.toml)
- [Realm IO 关闭语义](https://github.com/zhboner/realm/blob/v2.9.4/realm_io/src/lib.rs)
- [sing-box direct 入站](https://sing-box.sagernet.org/configuration/inbound/direct/)
- [GOST](https://gost.run/en/)
- [rathole 基准测试范围](https://github.com/rathole-org/rathole#benchmark)
- [frp](https://github.com/fatedier/frp)
- [Linux flowtable](https://www.kernel.org/doc/html/latest/networking/nf_flowtable.html)

## 安全与一致性

- 服务器内节点和转发共享同一端口命名空间，控制面在写入前检测跨表冲突。
- 自动端口从 20000–60000 随机选择；Agent 启动服务时还会捕获 Portolan 之外的占用。
- 目标节点转发由控制面解析服务器地址和真实监听端口，UI 不需要重复填写。
- 服务器端口转发保存目标服务器引用；Agent 持续上报可作为公网入站的 IPv4/IPv6，控制面按入口与目标共同具备的地址族选路，已有链路在栈能力变化时会更新并重新同步。自定义地址则保持用户填写的固定目标。
- `100.64.0.0/10` 属于运营商共享地址空间，也常被 Tailscale 使用；即使系统接口把它列为全局单播，Portolan 也不会把它当成公网 IPv4 入站。目标只有公网 IPv6、入口为双栈时会自动选择 IPv6。
- IPv4 与 IPv6 在模型中都保存为不带方括号的主机地址；生成 `host:port` 时统一补上 IPv6 方括号。Realm 明确使用双栈监听，sing-box 监听所有 IPv6 接口并兼容 IPv4 映射。
- 同一服务器上把入口端口转回自身会形成无限回环，控制面会拒绝这种配置。
- 所有生成文件先进入新版本目录，通过检查后才切换 `current`。

## 实时链路探测

入口 Agent 每轮同步后读取自己负责的转发，并从入口服务器直接连接目标节点或目标端口。节点目标、服务器端口目标和自定义地址使用同一条探测链路。TCP 会进行三次有超时的连接探测，向面板上报中位建连延迟、延迟离散程度、建连失败率、最近错误和 `stable` / `degraded` / `down` 状态；IPv4、IPv6 均使用规范化后的目标地址，面板本机不会冒充入口服务器测量。结果保留七天并定期清理。探测绕过入口监听，只证明入口到目标的 TCP 可达性；不代表客户端到入口或应用协议已通过验证。转发进程是否在运行由 Agent 上报的服务状态单独显示。API 字段 `loss_percent` 的值是建连失败比例，不是数据包丢失率。

纯 UDP 转发目前明确标记为“仅 UDP，无法通用探测”。在没有协议级探针前，不用一次无响应的 UDP 发送伪造成功率。以后可按 DNS、QUIC 等已知目标类型增加专用探针。
