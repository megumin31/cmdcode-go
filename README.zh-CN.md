# cmdcode-go

[English](README.md) | 中文版

非官方 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 插件，把 **CommandCode Go（$1/月）套餐**接到 CLI 自己的 `POST /alpha/generate` 网关上。Go 套餐没有 Provider API 权限（`/provider/v1/*` 返回 `403 upgrade_required`），所以本插件复刻 CLI 的线路协议，把 OpenAI chat 流量翻译过去——和官方 `command-code` CLI 每次对话做的事完全一样。

> 从 `command-code` bundle 逆向而来（每份名单的 exact CLI 版本记在 `models.json` 里）。与 Command Code / Langbase 无关。该端点无文档，可能漂移；漂移时先更新 `cli_version`。

## 功能

- **Executor**（`both` scope，`chat-completions` 进/出）：OpenAI chat → 网关 NDJSON（`text-delta` / `reasoning-*` / `tool-call` / `finish` / `error` / `abort`）→ OpenAI 响应。Reasoning 以 OpenRouter 风格的 `reasoning_content` delta 透出，严格 OpenAI 客户端也能正常工作。
- **流式 + 缓冲**：逐 chunk 增量转发 SSE；非流式调用在服务端消费完整条 NDJSON 再返回。没有 stream id 的 host 走缓冲 fallback。
- **`response_before_translator`**：给 OpenAI→Claude 翻译补上缺失的 `data:` 前缀（那个转换器会丢掉无前缀 payload；OpenAI HTTP 层自己做分帧，所以这里必须是裸 JSON）。
- **`model_registrar` + `model_provider`**：Go 套餐名单仅从官方包内 models.md 提取（当前列表见 `models.json`）。[Action](.github/workflows/models.yml) 每 6 小时从 `command-code@latest` 刷新；插件经 `models_url`（6h TTL）自动跟进，编译表做离线兜底——注册永远不需要装 CLI。
- **`model_router`**：认领 `cmdcode-go/<id>`、canonical 名和短名；其余放行给别的 provider。
- **Tool 往返**：tool schema 强制转成网关要求的 `{type: object}` 记录（缺失/null 的 schema 变成 `{"type":"object","properties":{}}` 而不是整轮失败）；`tool_choice: none` 时剥掉 tools。
- **用量统计**：两条路径 `usage` 恒存在。`finish` 事件缺 `totalUsage` 时下游拿到 0（官方 CLI harness 默认行为），永不为 null。每 100 轮往 stderr 打一行本地审计：
  `cmdcode-go: usage turns=N with_totalUsage=X zero_fallback=Y truncated=Z`
- **截断即失败**：一轮没收到 `finish` 事件就作为错误上抛，host 可重试（对标官方 502），绝不伪造 `stop`。

## 依赖

- CLIProxyAPI v7 host（`github.com/router-for-me/CLIProxyAPI/v7` v7.2.149）
- Go 1.26+（仅编译用）
- 一个 CommandCode 账号 key（`user_...`）

## 安装

```bash
cd go && go build -buildmode=c-shared -o cmdcode-go.so .
cp cmdcode-go.so <cliproxyapi>/plugins/
# 然后重启 host（插件启动时加载；只更新名单不用重编——见“模型名单自动化”和 DEPLOY.md）
```

> 一定在跑 host 的机器上编译：`.so` 是 OS/arch 绑定的（macOS 编出来的放 Linux 上会 `invalid ELF header`）。需要 Go 1.26+、CGO、gcc + libc 头文件。分步部署指南：[DEPLOY.md](DEPLOY.md)。

跑回归测试（不需要网络和 key）：

```bash
cd go && go test ./...
```

### 让你的 agent 来部署

把下面这段直接发给你的 agent：

```text
请照着 https://github.com/megumin31/cmdcode-go/blob/main/DEPLOY.md 部署 cmdcode-go 插件：
1. 先确认 CLIProxyAPI 跑在哪台机器，一律在那台机器本机编译，不要跨平台拷贝 .so
2. 备份旧插件 → 安装 → 重启 host
3. 按文档第 4 节逐项验收，把 `models refreshed` 那行和服务状态贴给我
4. 遇到问题先查第 6 节故障表，搞不定再问我
```

## 配置

```yaml
plugins:
  dir: "plugins"
  configs:
    cmdcode-go:
      enabled: true
      priority: 1
      # api_key: "user_..."    # 或 COMMANDCODE_KEY 环境变量 / ~/.commandcode/auth.json
      # base_url: "https://api.commandcode.ai"
      # cli_version: "1.47.1"  # 必须跟紧已装 CLI；旧版本会被网关拒绝
      # project_slug: "cliproxyapi"
      # permission_mode: "standard"  # 透传为 permissionMode
      # models: ["deepseek/deepseek-v4-flash"]  # allowlist，空 = 全部
      # disable_models: ["xai/grok-4.5"]        # denylist
      # models_url: "https://raw.githubusercontent.com/megumin31/cmdcode-go/main/models.json"
      # models_file: "/etc/cmdcode-go/models.json"  # 本地覆盖，变更自动重载
      # models_refresh_interval: "6h"  # Go duration；"0" 关闭远程刷新
```

Key 解析顺序：host auth 属性 → 插件 `api_key` → `COMMANDCODE_KEY`（还有 `COMMANDCODE_API_KEY`、`COMMAND_CODE_API_KEY`）→ CLI 登录文件（`~/.commandcode/auth.json`、`$COMMANDCODE_CONFIG_DIR/auth.json`）。

## 行为说明

- 网关拒绝 `stream:false`；插件永远发 `stream:true`。
- 客户端没给时取 `64000` 与模型参考/默认预算的较小值；显式预算只受已确认的 `gateway_output_limit` 限制，不受元数据兜底值截断。
- `reasoning_effort` clamp 到网关枚举（`low|medium|high|xhigh|max`）；未知值退化为模型默认，不断整轮。
- 推理重的模型先花 `max_tokens` 想事情——预算给小了会拿到 `finish_reason: length` + 空内容。给足预算（观测到单轮思考用掉 300+ tokens）。
- Chunk payload 保持裸 JSON——host 自己加 `data:` 分帧和结尾的 `data: [DONE]`。预分帧会 double。
- 上游 503 按 HTTP 503 原样透出并保留 `isRetryable`（免费档模型高并发会 shed load；客户端应重试）。
- 模型名单：`model.register`/`model.static` 时从 `models_file`（变更即重载）或 `models_url`（TTL `models_refresh_interval`，默认 6h，8s 抓取超时）刷新。抓取/解析/校验任一步失败都保留旧快照或编译表——名单永不为空。拉到的 payload 必须是 `schema_version: 1` 且包含锚点模型 `deepseek/deepseek-v4-flash`。

## 验证状态

- `go vet` + `go test` 全绿（33 tests，`-race` 干净：translation、tool round-trip、`tool_choice:none`、adversarial inputs、host-YAML config、NDJSON edges、incremental-stream parser、framing hook、effort sanitizer、schema coercion、model allow/deny filtering、routing contract、key precedence、UUID shape、error-status mapping、zero-fallback usage、truncation contract、usage-turn counters、remote roster fetch/cache/file/disabled、roster config keys）。
- 经真实 host 二进制压力验证：12 路并行混合模型 burst、12× 顺序 sustained（12/12）、4× 中途取消瞬间恢复、单轮 3787-token 输出、200KB tool_result 输入、图片输入、脚本化 agentic tool loop，以及三种协议下官方 `openai` + `anthropic` SDK 套件。

## 项目结构

| File | Role |
|---|---|
| `go/main.go` | c-shared 导出、host RPC 分发 |
| `go/request.go` + `go/response.go` | OpenAI ↔ `/alpha/generate` 翻译、usage 组帧、output budgets |
| `go/stream.go` | 增量 NDJSON→SSE 中继（`EventDecoder`）、截断处理 |
| `go/executor.go` | 流式 / 非流式 / 缓冲三条执行路径 |
| `go/models.go` | Go 套餐名单查询、allow/deny 过滤、路由（经 `activeModelTable` 统一编译表 + 远程快照） |
| `go/models_generated.go` | 编译期 fallback 表——生成文件，别手改（见下） |
| `go/models_remote.go` | `models.json` 抓取/校验/TTL 快照、离线兜底 |
| `go/config.go` | Host YAML 配置、key / URL / version 解析 |
| `go/normalize.go` | `response_before_translator` 钩子（OpenAI→Claude 补 `data:` 前缀） |
| `go/gateway_test.go` | 回归套件（远程相关测试用 `httptest`，不碰外网） |
| `models.json` | 发布的名单契约（`schema_version`、`source_cli_version`、entitled models） |
| `scripts/extract-models.py` | models.md 提取器 → `models.json` + `go/models_generated.go` |
| `.github/workflows/models.yml` | 6 小时名单刷新（extract → fmt/vet/test → auto-commit） |
| `DEPLOY.md` | 部署指南：编译矩阵、安装、验收、排障、回滚 |

## 模型名单自动化

模型 ID、名称、最低套餐和 reasoning efforts 只来自精确版本 npm 包中的
`dist/bundled/command-code-knowledge/reference/models.md`，仅 Go 行进入名单。
不再解析或运行 cli.mjs；models.dev 只补元数据，不能改变成员关系。

Action 每 6 小时解析 latest 为精确版本，通过 `npm pack --ignore-scripts` 下载，
只解出 package.json 和 models.md。记录包 integrity、文档 SHA-256、models.dev 完整提交号。
包版本没变也可更新 models.dev 元数据；相同输入生成相同输出。

Context 优先使用 Command Code 文档窗口（K/M 按十进制解析），缺失时使用 models.dev，
最后默认 200000。其他供应商的精确整数不能自动覆盖当前网关声明的窗口。
Output 优先 models.dev，超过 context 则拒绝；然后使用上一版明确来自 models.dev 的值，
最后采用标为 fallback 的预算（小窗口 32768，否则 65536，并不超过 context）。
这些预算不代表已验证的 Command Code 网关上限。模态未知记 null。

models.dev 只接受完整 ID 不区分大小写精确匹配，或全局唯一的精确短名匹配；
保留标点和 free/fast/preview 等后缀，有歧义不猜测。

缺列、未知套餐、重复 ID、短名冲突或异常模型数量会失败，保留线上旧名单。
大幅变动需人工检查后通过 workflow_dispatch 的 allow_large_drift 放行。
解析器使用固定版本文档测试，路由与预算测试使用固定数据，不钉死实时套餐。
保留 JSON schema 1 兼容旧插件；新版插件支持文档名单移除旧 anchor 模型。

本地生成（PKG 为解包后的 package 目录，META 为 models.dev 仓库）：

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/extract-models.py \
  --models-md "$PKG/dist/bundled/command-code-knowledge/reference/models.md" \
  --package "$PKG/package.json" --prev models.json \
  --models-dev "$META/models" --models-dev-rev "$(git -C "$META" rev-parse HEAD)" \
  --out models.json --gen go/models_generated.go
```

## 组件设计与正确性边界

项目仍是单个 Go package / c-shared 插件，按五个职责组织：
Plugin（生命周期）、ConfigStore（配置）、ModelRegistry（模型快照与刷新）、
GatewayClient（HTTP 通信）、EventDecoder（统一事件解析）。
详细职责、资源上限和取消机制见 [ARCHITECTURE.md](ARCHITECTURE.md)。

关闭插件时会取消并等待后台流和宿主回调；模型刷新有失败冷却、并发抑制和配置代际检查。
标准 YAML 解析支持列表、引号和注释，非法配置不会部分生效。
每次请求使用固定配置与名单快照；图文顺序不变；损坏 NDJSON 会报错，不再静默丢弃。
所有输出模式共用同一终止规则及缓存 token 用量格式。

模型的 vision 与 reasoning efforts 同时进入编译名单和动态注册。
fallback 输出预算只用于默认请求，不再伪装成硬上限。
用户显式指定的预算仅在名单提供 gateway_output_limit 时截断。
当前 ABI 无直接的客户端取消通知：断开后通过下一次 emit 失败终止，静默上游仍受请求超时约束。
新增测试及 CI 使用竞态检查并实际构建 c-shared 库。
