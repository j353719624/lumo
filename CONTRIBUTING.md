# 参与 LUMO 开发

LUMO 欢迎一起把它做得更好。无论是报缺陷、提需求、补文档还是提代码，都是帮助。

## 开始之前

- 只有一个 Go 标准库依赖：服务端与客户端都不引入第三方库，新增依赖需要充分理由
- 协议是对外契约：改动 `pkg/proto` 的分帧或消息字段属于破坏性变更，务必在 PR 里说明兼容方案
- 控制台的中文文案与视觉风格保持统一，改动 UI 请附截图

## 本地开发

```bash
# 依赖：Go 1.26+、Node.js 20+

# 前端控制台（web/ 是 npm workspace）
cd web && npm ci && npm run build && cd ..

# 编译
make build          # 等价于 go build -trimpath -ldflags "-s -w" -o bin/lumos ./cmd/lumos

# 只编后端、跳过前端嵌入
go build -tags noweb -o bin/lumos ./cmd/lumos
go build -tags noweb -o bin/lumoc ./cmd/lumoc

# 测试
go test ./...
./hack/run-e2e.sh   # 端到端：本机起服务端与客户端跑通一条隧道
```

## 提交前自检

```bash
gofmt -l .          # 应当没有输出
go vet ./...
go test ./...
```

一次提交只做一件事。提交信息用中文，格式 `类型：简述`，类型取
`feat` / `fix` / `perf` / `refactor` / `style` / `docs` / `chore` / `test`。

## 提 PR

1. 从 `dev` 切分支，命名 `feat/xxx`、`fix/xxx`
2. 写清改动动机与影响面；涉及行为变化请说明如何验证
3. 面向用户的改动附截图或命令输出
4. 合并目标为 `dev`

## 报告问题

请在 issue 里提供：

- 现象与期望
- 复现步骤
- 版本（`lumos version` / `lumoc version`）与平台
- 相关日志（**提交前删掉 IP、令牌等敏感信息**）

## 安全

发现安全问题时请不要公开提 issue，直接通过仓库主页的联系方式私下告知。
LUMO 的日志默认对地址做掩码处理，提交日志时也请保持这个习惯。

## 行为边界

- 不引入来源不明的代码或二进制
- 不提交任何真实令牌、密钥、服务器地址
- 测试样例地址请使用 RFC 5737 文档段（192.0.2.0/24、198.51.100.0/24、203.0.113.0/24）

## 许可与版权

本项目采用 **PolyForm Noncommercial License 1.0.0**：可自由使用、修改、分发，**商业使用需作者授权**（见 [LICENSING.md](LICENSING.md)）。

提交 PR 即表示：

1. 你的贡献以同一许可并入本项目；
2. 你确认自己有权提交这段代码，且未附带与本许可冲突的条款；
3. 你同意作者可按其它条款（含商业许可）一并授权包含你贡献的版本。

这是项目能够对外提供商业授权的前提，请确认后再提交。
