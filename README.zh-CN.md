# cpa-cmdcode-go

[English](README.md) | 中文

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的 Command Code Go ($1/月套餐) 插件。

直接将 Command Code Go 提供的所有模型接入 CLIProxyAPI，支持标准的 OpenAI 兼容客户端调用，无需在后台额外运行 Command Code CLI。

## 特性

- **完整支持**：流式（SSE）与非流式对话、Tool Calls（函数调用）、Reasoning Content（思考过程）、多模态识图。
- **独立模型命名空间**：模型注册为 `cmdcode-go/<模型ID>`（例如 `cmdcode-go/deepseek/deepseek-v4-flash`、`cmdcode-go/moonshotai/Kimi-K3` 等），避免与其他渠道同名模型冲突。
- **开箱即用凭据识别**：支持直接配置 `api_key`、读取 `COMMANDCODE_KEY` 环境变量，或自动读取本地已有 `cmd login` 登录态（`~/.commandcode/auth.json`）。
- **动态模型清单**：内置编译清单，同时默认自动从云端同步最新模型（通过 GitHub Actions 追踪官方 CLI 更新），也可指定本地 `models.json`。

## 快速开始

### 1. 编译插件

```bash
git clone https://github.com/megumin31/cpa-cmdcode-go.git
cd cpa-cmdcode-go
mkdir -p build
LIBRARY="$(cd go && go run ./cmd/buildmeta --field filename)"
(cd go && CGO_ENABLED=1 go build -buildmode=c-shared -o "../build/$LIBRARY" .)
```

编译生成 `build/cmdcode-go-v0.1.0.so`，将其拷贝到 CLIProxyAPI 的 plugins 目录即可。

### 2. 配置 CLIProxyAPI

在 CLIProxyAPI 的 `config.yaml` 中添加插件配置：

```yaml
plugins:
  dir: "/path/to/plugins"
  configs:
    cmdcode-go:
      enabled: true
      priority: 1
      api_key: "YOUR_COMMAND_CODE_KEY" # 若机器已执行过 cmd login，此项可省略
```

启动或重启 CLIProxyAPI。

### 3. 使用

客户端像往常一样配置 CLIProxyAPI 地址与密钥即可。

获取模型列表：
```bash
curl -s http://127.0.0.1:8317/v1/models \
  -H "Authorization: Bearer YOUR_CLIPROXY_KEY"
```

发起对话调用：
```bash
curl -N http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer YOUR_CLIPROXY_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "cmdcode-go/deepseek/deepseek-v4-flash",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

> **提示**：调用时既可以使用完整的 `cmdcode-go/<模型ID>`（推荐），也可以直接使用简写或短名称（如 `Kimi-K3`、`deepseek/deepseek-v4-flash`），插件会自动识别路由。完整模型列表参见 [models.json](models.json)。

## 配置参数

配置项位于 `plugins.configs.cmdcode-go` 下：

| 配置项 | 说明 | 默认值 |
|---|---|---|
| `api_key` | Command Code 账号 Key（`user_...`） | 优先读取环境变量 / CLI 登录态 |
| `models` | 允许启用的模型列表（白名单） | 全部启用 |
| `disable_models` | 禁用的模型列表（黑名单） | 无 |
| `models_refresh_interval` | 远程模型列表刷新间隔 | `"6h"`（设为 `"0"` 则仅使用内置编译清单） |
| `models_file` | 本地模型清单文件路径 | 无（设置后优先于远程 URL） |
| `models_url` | 远程模型清单 JSON 地址 | 本仓库 `main` 分支的 `models.json` |

## 开发、CI 与发布

分支 push 和 pull request 会执行与 Release 相同的 Linux amd64/arm64 原生质量检查，包括格式、vet、单元测试、race、Python fixtures、工作流检查、C-shared 构建与 C ABI 重载测试。测试仅使用 fake 或本地服务，不调用真实推理。

[Release](https://github.com/megumin31/cpa-cmdcode-go/releases) 发布后，两种架构均通过检查才会上传压缩包、校验和与完成清单；稳定版和预发布版均支持。安装时保留压缩包内的 `cmdcode-go-v0.1.0.so` 文件名。详见 [CI 与本地验证](docs/CI.md) 和 [发布与重试说明](docs/RELEASE.md)。

## 开源协议

本项目采用 [AGPL-3.0](LICENSE) 协议开源。
