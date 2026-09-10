# cmdcode-go

[English](README.md) | 中文版

非官方 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 插件，把 **CommandCode Go（$1/月）套餐**接到 CLI 自己的 `POST /alpha/generate` 网关上。Go 套餐没有 Provider API 权限（`/provider/v1/*` 返回 `403 upgrade_required`），所以本插件复刻 CLI 的线路协议，把 OpenAI chat 流量翻译过去——和官方 `command-code` CLI 每次对话做的事完全一样。

> 从 `command-code` bundle 逆向而来（每份名单的 exact CLI 版本记在 `models.json` 里）。与 Command Code / Langbase 无关。该端点无文档，可能漂移；漂移时先更新 `cli_version`。

## 功能

- **Executor**（`both` scope，`chat-completions` 进/出）：OpenAI chat → 网关 NDJSON（`text-delta` / `reasoning-*` / `tool-call` / `finish` / `error` / `abort`）→ OpenAI 响应。Reasoning 以 OpenRouter 风格的 `reasoning_content` delta 透出，严格 OpenAI 客户端也能正常工作。
- **流式 + 缓冲**：逐 chunk 增量转发 SSE；非流式调用在服务端消费完整条 NDJSON 再返回。没有 stream id 的 host 走缓冲 fallback。
- **`response_before_translator`**：给 OpenAI→Claude 翻译补上缺失的 `data:` 前缀（那个转换器会丢掉无前缀 payload；OpenAI HTTP 层自己做分帧，所以这里必须是裸 JSON）。
- **`model_registrar` + `model_provider`**：Go 套餐名单从官方 CLI bundle 提取（当前列表见 `models.json`）。[Action](.github/workflows/models.yml) 每 6 小时从 `command-code@latest` 刷新；插件经 `models_url`（6h TTL）自动跟进，编译表做离线兜底——注册永远不需要装 CLI。
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
- 客户端没给时默认输出预算 `64000` tokens（和官方 CLI 一致），再按各模型的注册 output 上限往下 clamp；显式给的预算予以尊重。
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
| `go/gateway.go` | OpenAI ↔ `/alpha/generate` 翻译、usage 组帧、output budgets |
| `go/stream.go` | 增量 NDJSON→SSE 中继（`liveStream`）、截断处理 |
| `go/executor.go` | 流式 / 非流式 / 缓冲三条执行路径 |
| `go/models.go` | Go 套餐名单查询、allow/deny 过滤、路由（经 `activeModelTable` 统一编译表 + 远程快照） |
| `go/models_generated.go` | 编译期 fallback 表——生成文件，别手改（见下） |
| `go/models_remote.go` | `models.json` 抓取/校验/TTL 快照、离线兜底 |
| `go/config.go` | Host YAML 配置、key / URL / version 解析 |
| `go/normalize.go` | `response_before_translator` 钩子（OpenAI→Claude 补 `data:` 前缀） |
| `go/gateway_test.go` | 回归套件（远程相关测试用 `httptest`，不碰外网） |
| `models.json` | 发布的名单契约（`schema_version`、`source_cli_version`、entitled models） |
| `scripts/extract-models.py` | CLI-bundle 提取器 → `models.json` + `go/models_generated.go` |
| `.github/workflows/models.yml` | 6 小时名单刷新（extract → fmt/vet/test → auto-commit） |
| `DEPLOY.md` | 部署指南：编译矩阵、安装、验收、排障、回滚 |

## 模型名单自动化

Go 套餐没有服务端名单接口，所以名单复刻官方 CLI 自己的门禁（`opensource` 分类减 `blockedModels`，未分类 id fail open——和 `evaluateModelAccess` 同逻辑）：

```bash
git clone --depth 1 --filter=blob:none --sparse https://github.com/anomalyco/models.dev /tmp/models.dev
git -C /tmp/models.dev sparse-checkout set models
python3 scripts/extract-models.py \
  --cli "$(npm root -g)/command-code/dist/cli.mjs" \
  --package "$(npm root -g)/command-code/package.json" \
  --prev models.json --models-dev /tmp/models.dev/models \
  --models-dev-rev "$(git -C /tmp/models.dev rev-parse --short HEAD)" \
  --out models.json --gen go/models_generated.go
```

Output budgets 按 bundle 值 → [anomalyco/models.dev](https://github.com/anomalyco/models.dev) `[limit].output`（stem 精确匹配，`-free` 别名计入；超 context 窗口（超单位容差）的值丢弃）→ `--prev` carry → context ≤204800 给 32768 → 默认 65536。Contexts 按 Tr 覆盖 → 目录值 → models.dev（仅 CLI 自己 fallback 到 200000 默认时）→ 200000。每条记 `output_source`/`context_source`（`bundle`/`modelsdev`/`carry`/`heuristic`，`cli`/`modelsdev`/`default`）。Action 每 6 小时对 `command-code@latest` 跑一遍提取器，经 `gofmt`/`go vet`/`go test` 门禁后提交 `models.json` + `go/models_generated.go`。
