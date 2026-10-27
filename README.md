# LUMO

LUMO 是一个自研的内网穿透 / 反向代理工具，把 NAT 或防火墙后面的本地服务安全地暴露到公网。服务端 `lumos` 部署在有公网 IP 的机器上，客户端 `lumoc` 部署在内网机器上，二者通过加密握手建立控制通道，按需建立数据连接转发流量。

- **零第三方依赖**：只用 Go 标准库，二进制干净、供应链面积极小
- **协议自研**：长度分帧 JSON 控制消息 + HMAC-SHA256 令牌认证，不可与其它同类工具互通
- **中文控制台**：内置服务端 / 客户端 Web 面板，实时状态、流量与运行事件

## 功能（当前版本 0.1.0）

| 代理类型 | 说明 |
|---|---|
| TCP | 远程端口映射，公网直连内网 TCP 服务 |
| UDP | 远程端口映射，按访客地址回包 |
| HTTP | 按域名路由的虚拟主机（vhostHTTPPort） |
| HTTPS | 按 TLS SNI 路由，加密端到端保留在本地服务 |

其他能力：令牌认证（HMAC 挑战）、断线自动重连、配置热重载、`verify` 配置校验、内置控制台、流量统计、运行事件。

## 构建

依赖：Go 1.26+（无需 Node，前端已内嵌源码随编译打入）

```bash
go build -trimpath -ldflags "-s -w" -o bin/lumos ./cmd/lumos
go build -trimpath -ldflags "-s -w" -o bin/lumoc ./cmd/lumoc
```

## 快速开始

服务端 `lumos.toml`：

```toml
bindPort = 7000
vhostHTTPPort = 18090

[auth]
token = "换成长随机串"

[webServer]
addr = "127.0.0.1"
port = 7500
user = "admin"
password = "换掉"
```

客户端 `lumoc.toml`：

```toml
serverAddr = "服务器IP"
serverPort = 7000

[auth]
token = "与服务端一致"

[webServer]
addr = "127.0.0.1"
port = 7400
user = "admin"
password = "换掉"

# TCP 隧道
[[proxies]]
name = "ssh"
type = "tcp"
localIP = "127.0.0.1"
localPort = 22
remotePort = 6022

# HTTP 隧道（按域名）
[[proxies]]
name = "site"
type = "http"
localPort = 8080
customDomains = ["app.example.com"]
```

启动与校验：

```bash
./bin/lumos verify -c lumos.toml   # 只校验配置
./bin/lumos -c lumos.toml
./bin/lumoc -c lumoc.toml
```

打开 `http://127.0.0.1:7500`（服务端控制台）或 `http://127.0.0.1:7400`（客户端控制台），使用 `webServer` 配置的账号密码登录。客户端控制台里点"重新加载配置"可热更新隧道，无需重启。

## 安全须知

- `auth.token` 必填，建议 32 位以上随机串；认证使用 HMAC-SHA256，令牌本身不在网络上传输
- 面板默认只监听 `127.0.0.1`；对外暴露前请更换强密码
- vhost 端口对公网开放时，请在前置反向代理或防火墙上做好访问控制

## 目录结构

```
cmd/lumos            服务端入口
cmd/lumoc            客户端入口
internal/config      配置加载与校验
internal/toml        TOML 子集解析器
internal/proto       线协议：分帧、认证、数据包
internal/relay       双向转发与流量统计
internal/server      服务端：会话、注册、转发、vhost、控制台
internal/client      客户端：控制循环、work 连接、控制台
internal/dash        内嵌 Web 面板资源
internal/log         日志与运行事件环形缓冲
```

## 许可证

Apache License 2.0，见 [LICENSE](LICENSE)。
