# 参与开发

欢迎提交问题和改进。较大的改动请先开 issue 说明要解决的问题，避免方向不一致。

## 开发环境

- Go 1.26 或更新版本（CI 使用 1.27）
- Node.js 22.13 或更新版本和 npm（CI 使用 Node.js 24）

源码结构：

- `cmd/`：面板、Agent 和离线恢复工具的入口
- `internal/`：实现。`api` 是面板 API，`store` 是 SQLite 存储，`configgen` 生成 sing-box/Realm 配置和客户端导出，`apply` 负责节点上的版本化应用与回滚，`webui` 内嵌控制台
- `app/`：网页控制台（React，由 vinext 构建并静态导出）；与浏览器无关的逻辑在 `app/lib/`
- `tests/`：控制台和安装器的 Node 测试
- `scripts/`：节点安装器、面板安装脚本和发布构建
- `ops/`：systemd 单元和反向代理示例

## 本地运行

启动面板（PowerShell 写法见下方）：

```bash
export PORTOLAN_MASTER_KEY=$(openssl rand -base64 32)
export PORTOLAN_ADMIN_TOKEN=$(openssl rand -base64 32)
echo "$PORTOLAN_ADMIN_TOKEN"
go run ./cmd/portolan-panel --secure-cookies=false --database ./data/portolan.db
```

```powershell
$env:PORTOLAN_MASTER_KEY = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
$env:PORTOLAN_ADMIN_TOKEN = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
$env:PORTOLAN_ADMIN_TOKEN
go run ./cmd/portolan-panel --secure-cookies=false --database ./data/portolan.db
```

`./data` 目录需要先创建。不构建控制台时面板只提供 API；另开一个终端启动控制台开发服务器：

```bash
npm install
npm run dev
```

开发服务器把 `/api` 和 `/healthz` 转发到 `PORTOLAN_API_ORIGIN`（默认 `http://127.0.0.1:8088`）。

## 测试

提交前运行与改动相关的检查：

```bash
go vet ./cmd/... ./internal/...
go test ./cmd/... ./internal/...
npm run lint
npx tsc --noEmit
npm test
```

`npm test` 会先构建控制台。改动协议或配置生成时，还要用真实的 sing-box 检查生成的配置：

```bash
SING_BOX_BIN=/path/to/sing-box go test ./internal/configgen/ -run TestGeneratedFragmentsPassSingBoxCheck -count=1
```

改动端口流量计数时，在 Linux 上用真实的 nftables 运行计数测试。测试会改动规则集，所以放在单独的网络命名空间里：

```bash
go test -c -o traffic.test ./internal/traffic
sudo unshare --net sh -c 'ip link set lo up && PORTOLAN_NFT_TEST=1 ./traffic.test -test.run NFT -test.v'
```

部分应用与回滚测试只在 Linux 上运行。改动界面时，在桌面宽度和 390 px 以下的手机宽度各检查一遍，页面不应出现横向滚动。

构建与发布相同的产物（需要 Linux、GNU tar）：

```bash
scripts/build-release.sh v0.0.0-dev
```

产物在 `release/` 下；`docker build .` 会使用其中的二进制。

## 约定

- 测试和文档里只使用虚构数据：地址用 `192.0.2.0/24`、`198.51.100.0/24`、`203.0.113.0/24`、
  `2001:db8::/32` 或 `example.com`，不要放真实服务器地址、域名、密钥或订阅链接。
- 保持以下边界：Agent 只执行结构化任务，不执行任意命令；Agent 发现的外部节点只读，不进入
  期望状态，也不能被删除；配置先用真实核心校验，再原子切换，失败时恢复上一版本。
- 状态和指标必须反映真实数据。面板连不上时显示中断，不用示例数据或乐观的汇总代替。
- 增加打包进控制台的 npm 依赖时，确认 `scripts/third-party-licenses.mjs` 能找到它的许可证文件。
- 提交信息使用 [Conventional Commits](https://www.conventionalcommits.org/zh-hans/v1.0.0/)，
  例如 `fix(agent): restore the previous release when activation fails`。

## 发布流程

1. 在 `CHANGELOG.md` 里把 `Unreleased` 的内容整理成新版本小节，更新 `compose.yaml` 的镜像标签。
2. 合并到 `main` 并等待 CI 通过。
3. 推送标签 `vX.Y.Z`。发布工作流会构建发布包和多架构镜像，并创建草稿 Release。Agent 的版本号由
   `scripts/build-release.sh` 从 git 历史得出：Agent 依赖的 Go 包或 `go.mod`/`go.sum` 最后一次变化
   所在的发布（测试文件不算）。面板只能在线替换 Agent 二进制；如果这次发布改了
   `scripts/install-agent.sh` 且已安装的服务器需要这些改动，在 CHANGELOG 里写明需要重装 Agent。
4. 核对草稿的附件、校验和与说明后再发布。
