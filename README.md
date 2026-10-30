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

其他能力：令牌认证（HMAC 挑战）、断线自动重连、配置热重载、`verify` 配置校验、内置控制台、流量统计、运行事件、多连接管理器。

## 多连接控制台（推荐）

不想手写配置文件时，用 `lumoc ui` 打开图形控制台：新建 / 编辑 / 启停多条隧道连接，配置保存在本机 JSON 文件里，所有连接由单个进程托管。

```bash
./bin/lumoc ui                          # 默认 http://127.0.0.1:7400
./bin/lumoc ui -addr 0.0.0.0:7400 -user admin -pass 强密码 -store D:\data\configs.json
```

点「新建配置」填服务器地址、令牌和端口映射即可；开关即启用 / 停用，改动即时生效，无需重启进程。

## 构建

依赖：Go 1.26+、Node.js 20+（Go 二进制不会内嵌未构建的前端，面板需要先构建）

```bash
# 1. 构建两个 Vue 3 控制台（web/ 是 npm workspace）
cd web && npm ci && npm run build && cd ..

# 2. 编译二进制（面板经 //go:embed 打入）
make build        # 或：go build -trimpath -ldflags "-s -w" -o bin/lumos ./cmd/lumos
```

只需要后端、不需要面板时，用 `noweb` 标签跳过前端嵌入：

```bash
go build -tags noweb -o bin/lumos ./cmd/lumos
go build -tags noweb -o bin/lumoc ./cmd/lumoc
```

完整开发流程（格式化、构建、测试）：

```bash
make all      # env + fmt + web + build
make alltest  # vet + 单测 + e2e
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
cmd/lumoc            客户端入口（含 ui / engine / discover / init / server 子命令）
pkg/config           配置加载、校验与回写
pkg/toml             TOML 子集解析器
pkg/proto            线协议：分帧、认证、数据包
pkg/relay            双向转发、限速与流量统计
pkg/server           服务端：会话、注册、转发、vhost、控制台
pkg/client           客户端：控制循环、work 连接、访客隧道、打洞
pkg/manager          多连接管理器与客户端控制台
pkg/auth             Web 会话与登录限流
pkg/token            Ed25519 令牌派生与验签
pkg/totp             RFC 6238 动态口令
pkg/qrcode           独立 QR 编码器（绑定 2FA 用）
pkg/udpstream        KCP 可靠 UDP 传输
pkg/stun             STUN 反射地址探测（打洞用）
pkg/log              日志、地址掩码与运行事件环形缓冲
web/lumos            服务端控制台（Vue 3 + Vite）
web/lumoc            客户端控制台（Vue 3 + Vite）
web/shared           两端共用的 i18n 与工具函数
```

## 参与共建

LUMO 是开放源码项目，欢迎一起完善：

- 报缺陷 / 提需求：[Issues](../../issues)
- 开发约定与 PR 流程：[CONTRIBUTING.md](CONTRIBUTING.md)
- 飞牛 fnOS 客户端安装包：[lumo-fpk](https://github.com/j353719624/lumo-fpk)

## 许可证

Apache License 2.0，见 [LICENSE](LICENSE)。
