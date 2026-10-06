# 更新记录

本文件记录用户可见的变化，格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循[语义化版本](https://semver.org/lang/zh-CN/)。1.0 之前的次版本之间可能有不兼容的变化，
会在对应小节说明。

## [Unreleased]

## [0.1.0] - 2026-10-06

首个公开版本，状态为 Technical Preview。

### 新增

- 面板与网页控制台：服务器、节点、转发和核心版本的管理，控制台内嵌在面板二进制里，服务器上不需要 Node.js。
- 节点 Agent：一行命令安装，主动出站连接面板；配置先由 sing-box 校验再原子切换，服务没有正常运行或没有占用端口时恢复上一版本。
- 协议：VLESS Reality（TCP、Vision、gRPC）、Shadowsocks（2022 系列、AEAD，`none` 需显式确认）、Snell v5/v6，导出 `vless://`、`ss://` 链接和 Surge 配置行。
- 转发：Realm（默认，每条规则一个进程）或 sing-box direct；目标可以是协议节点、服务器端口或自定义地址；入口到目标的 TCP 延迟检测和七天历史。
- 识别服务器上已有的 sing-box 和 Snell 节点，只读显示和导出。
- sing-box 和 Realm 版本在面板中选择，按 GitHub 公布的 SHA-256 校验，可逐台或批量更新。
- 登录保护：按来源的失败锁定、失败日志、可选 TOTP 两步验证；控制台页面带 Content-Security-Policy。
- 服务器详情显示 Agent 版本，与面板版本不一致时提示重装。
- 安装方式：面板安装脚本（systemd，amd64/arm64，带校验、升级备份和失败回退）和 `ghcr.io/kurisut7/portolan` 多架构镜像。
- 离线恢复工具 `portolan-runtime-import`：面板数据丢失时从节点上的配置重建数据库。

### 已知限制

- 界面只有中文。
- 只有一个管理员账号。
- 节点只支持使用 systemd 的 x86_64/aarch64 Linux。

[Unreleased]: https://github.com/KurisuT7/Portolan/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/KurisuT7/Portolan/releases/tag/v0.1.0
