# 离线恢复面板数据

面板数据库和主密钥都丢失、但节点服务器上的 Agent 和服务还在运行时，可以用
`portolan-runtime-import` 从各服务器的 `agent.json` 和当前生效的配置版本重建一个新的
面板数据库。它只写一个新的数据库文件：不连接面板、不重启 Agent、不替换现有数据库，
也不复用旧的 Agent 凭据。

还有可用的数据库备份和原主密钥时，直接恢复备份即可，不需要这份流程。

## 能恢复与不能推导的内容

程序直接从生成后的 sing-box/Realm 配置恢复以下值：

- server ID；
- node/forward ID 与监听端口；
- Reality UUID、私钥、Short ID、SNI、握手目标、flow；
- Shadowsocks method/password；
- Snell version/PSK/mode；
- sing-box/Realm 转发网络、目标地址和端口。

下列字段没有完整写入服务器上的配置，必须在恢复清单里明确提供，程序不会猜默认值：

- server 名称、地址、地区和出站地址族；
- node 名称与原 profile；
- Reality 公钥、fingerprint，以及配置中仍可逐值验证的 transport 设置；
- Shadowsocks/Snell 的 `allow_insecure`；
- forward 名称及 `target_server_id`/`target_node_id` 关联。没有关联时也必须显式写空字符串。

Reality 公钥会由配置里的私钥重新推导并与清单比对。版本清单（`manifest.json`）记录的
对象数多于实际配置文件数时，说明有停用或缺失的对象，导入会停止，因为这些对象无法从
当前版本还原。WebSocket、HTTP Upgrade 等传输的客户端 `host` 不会写入服务端配置，
导入器不接受在清单中补填，需要从可信的旧客户端配置恢复。

延迟历史、探测记录、旧的安装令牌和 Agent 凭据、创建时间和已停用的对象无法恢复。

## 收集服务器上的配置

在每台服务器上找到当前生效的版本目录：

```sh
readlink -f /etc/portolan/runtime/current
```

把 `/etc/portolan/agent.json` 和上面输出的整个目录复制到面板服务器的
`/var/tmp/portolan-recovery/snapshots/<服务器名>/` 下，清单也放在
`/var/tmp/portolan-recovery/plan.json`。只复制 `current` 这个符号链接不够。
`agent.json` 含有该服务器的 Agent 凭据，复制过程中按密钥对待。

## 清单格式

路径相对于清单文件解析，也可以使用绝对路径。下面只展示结构；协议密钥会从
`runtime_release` 读取，不要复制到清单里。

```json
{
  "schema": 1,
  "servers": [
    {
      "agent_config": "snapshots/server-a/agent.json",
      "runtime_release": "snapshots/server-a/42",
      "server": {
        "id": "srv-example",
        "name": "Server A",
        "address": "server-a.example.com",
        "ipv4_address": "",
        "ipv6_address": "",
        "egress_ipv4": true,
        "egress_ipv6": false,
        "region": "HK"
      },
      "nodes": {
        "node-reality-example": {
          "name": "Reality A",
          "profile": "REALITY · xtls-rprx-vision",
          "public_key": "PUBLIC_KEY_FROM_EXISTING_CLIENT_EXPORT",
          "fingerprint": "chrome",
          "transport": "tcp",
          "transport_settings": {}
        },
        "node-ss-example": {
          "name": "SS A",
          "profile": "aes-256-gcm",
          "allow_insecure": false
        }
      },
      "forwards": {
        "fwd-example": {
          "name": "Forward A",
          "target_server_id": "",
          "target_node_id": ""
        }
      }
    }
  ]
}
```

清单中的每个 node/forward 必须与配置文件一一对应；多出或缺少条目都会失败。

## 生成新数据库

以下命令针对安装脚本的默认路径，以 root 在面板服务器上运行。导入器拒绝以 root 身份
写数据库，以免生成面板账号无法读取的文件，所以用 `systemd-run` 以 `portolan-panel`
账号运行，并让 systemd 从环境文件读取主密钥，密钥不会出现在命令参数里。

1. 先在 `/etc/portolan-panel/panel.env` 里换上新生成的主密钥（`openssl rand -base64 32`）
   并备份它。
2. 只让面板账号读取恢复材料，然后只做检查：

   ```sh
   chown -R root:portolan-panel /var/tmp/portolan-recovery
   chmod -R u=rwX,g=rX,o= /var/tmp/portolan-recovery
   systemd-run --wait --pipe --uid=portolan-panel --gid=portolan-panel \
     /usr/local/lib/portolan-panel/portolan-runtime-import --plan /var/tmp/portolan-recovery/plan.json --check
   ```

   成功时输出 `runtime recovery plan valid` 以及服务器、节点、转发的数量。
3. 停止面板并写出新数据库。输出路径必须不存在：

   ```sh
   systemctl stop portolan-panel
   systemd-run --wait --pipe --uid=portolan-panel --gid=portolan-panel \
     -p EnvironmentFile=/etc/portolan-panel/panel.env -p UMask=0077 \
     /usr/local/lib/portolan-panel/portolan-runtime-import --plan /var/tmp/portolan-recovery/plan.json \
     --output /var/lib/portolan-panel/portolan.recovered.db
   ```

   导入器先写同目录的临时库，完成 WAL checkpoint 和 `PRAGMA quick_check`，确认文件属于
   当前账号且权限为 `0600` 后再原子改名；不会覆盖已有的数据库、WAL 或 SHM 文件。
4. 把原数据库（如果有）移走，再启用新库并启动面板：

   ```sh
   cd /var/lib/portolan-panel
   for file in portolan.db portolan.db-wal portolan.db-shm; do [ ! -e "$file" ] || mv "$file" "$file.before-recovery"; done
   mv portolan.recovered.db portolan.db
   systemctl start portolan-panel
   ```

5. 恢复后旧的 Agent 凭据都已失效。在每台服务器的详情里点「重装 Agent」，在该服务器上
   运行新的安装命令。重新注册前，服务器上的 sing-box 和 Realm 继续按原配置运行。
6. 确认面板和各服务器恢复正常后，删除 `/var/tmp/portolan-recovery`。

使用 Docker 时，容器以 UID 65532 运行。在 compose 目录里换好 `panel.env` 的主密钥，
停止面板，让该 UID 读取恢复材料，再把材料挂载进容器运行导入器：

```sh
docker compose stop
chown -R 65532:65532 /var/tmp/portolan-recovery
docker compose run --rm -v /var/tmp/portolan-recovery:/recovery:ro \
  --entrypoint /usr/local/lib/portolan-panel/portolan-runtime-import portolan \
  --plan /recovery/plan.json --output /var/lib/portolan-panel/portolan.recovered.db
```

镜像里没有 shell，改名需要借一个临时容器。数据卷名是 compose 项目名（默认为目录名）
加 `_portolan-data`，可以用 `docker volume ls` 确认：

```sh
docker run --rm -v portolan_portolan-data:/data busybox \
  sh -c 'cd /data && for f in portolan.db portolan.db-wal portolan.db-shm; do [ ! -e "$f" ] || mv "$f" "$f.before-recovery"; done && mv portolan.recovered.db portolan.db'
docker compose up -d
```

之后同样逐台「重装 Agent」。
