# cmdcode-go

[English](README.md) | 中文

将 **Command Code Go 套餐接入 CLIProxyAPI** 的非官方插件。安装后，已有的 OpenAI 兼容客户端可以通过 CLIProxyAPI 调用 Go 套餐模型，无需单独运行 Command Code CLI。

支持流式与非流式聊天、工具调用、推理内容，以及模型支持的图片输入。插件以一个 `.so` 动态库运行在 CLIProxyAPI 中，不需要额外启动服务。

## 安装

以下步骤面向 Linux，需要：

- 支持插件 ABI 的 CLIProxyAPI v7；本项目使用的 SDK 版本见 [go.mod](go/go.mod)。
- Go 1.26+、C 编译器及 libc 开发头文件。构建环境须与宿主的操作系统、CPU 架构和 libc 兼容。
- 可用的 Command Code Go 账号及账号密钥。

### 1. 编译插件

```bash
git clone https://github.com/megumin31/cmdcode-go.git
cd cmdcode-go
mkdir -p build
(cd go && CGO_ENABLED=1 go build -buildmode=c-shared -o ../build/cmdcode-go.so .)
```

产物为 `build/cmdcode-go.so`。同时生成的 `.h` 文件无需安装。

### 2. 配置并安装

在 CLIProxyAPI 配置中加入以下内容；已有 `plugins` 配置时，合并字段即可：

```yaml
plugins:
  dir: "/absolute/path/to/plugins"
  configs:
    cmdcode-go:
      enabled: true
      priority: 1
      api_key: "YOUR_COMMAND_CODE_KEY"
```

将目录和密钥替换为实际值。停止 CLIProxyAPI 后，在仓库根目录执行：

```bash
install -Dm755 build/cmdcode-go.so /absolute/path/to/plugins/cmdcode-go.so
```

随后按你原来的方式启动 CLIProxyAPI。

**复用已有登录：** 如果运行 CLIProxyAPI 的账号已经登录过 Command Code，可省略 `api_key`，插件会读取 CLI 登录文件。也可以向宿主进程提供环境变量 `COMMANDCODE_KEY`。使用 systemd 或容器时，需要在相应服务或容器中配置该变量。

## 使用

客户端的 API 地址和密钥仍然填写 **CLIProxyAPI 的地址及客户端密钥**。Command Code 账号密钥只用于插件连接上游。

先查询宿主提供的模型：

```bash
export CLIPROXY_URL="http://127.0.0.1:8317"
export CLIPROXY_API_KEY="YOUR_CLIPROXYAPI_KEY"

curl --fail-with-body -sS "$CLIPROXY_URL/v1/models" \
  -H "Authorization: Bearer $CLIPROXY_API_KEY"
```

从返回结果中选择一个属于本插件的模型 ID，替换下面的 `MODEL_ID`，发起一次流式聊天：

```bash
curl --fail-with-body -N "$CLIPROXY_URL/v1/chat/completions" \
  -H "Authorization: Bearer $CLIPROXY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MODEL_ID",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": true
  }'
```

可用模型见 [models.json](models.json)。宿主可能为模型设置别名或前缀，客户端应使用 `/v1/models` 实际返回的 ID。模型出现在列表中表示注册成功；聊天请求成功才验证了上游账号可用。

## 配置

以下字段均位于 `plugins.configs.cmdcode-go` 下；`enabled` 和 `priority` 由宿主管理。

| 字段 | 用途 | 默认行为 |
|---|---|---|
| `api_key` | Command Code 账号密钥 | 未配置时尝试环境变量和 CLI 登录文件 |
| `models` | 只启用指定模型，YAML 列表 | 空列表启用全部名单模型 |
| `disable_models` | 排除指定模型，YAML 列表 | 不排除；优先于 `models` |
| `models_file` | 使用本地模型 JSON | 未设置；设置后优先于远程地址 |
| `models_url` | 远程模型 JSON 地址 | 本仓库 `main` 分支的 `models.json` |
| `models_refresh_interval` | 远程名单刷新间隔 | `"6h"`；`"0"` 关闭远程和本地刷新，使用编译内置名单 |

模型筛选支持完整 ID、唯一短名和 `cmdcode-go/` 前缀。建议从名单复制完整 ID，避免歧义。

<details>
<summary>高级配置与密钥查找顺序</summary>

| 字段 | 默认值 | 用途 |
|---|---|---|
| `base_url` | `https://api.commandcode.ai` | 上游网关地址；未配置时也可由 `COMMANDCODE_BASE_URL` 提供 |
| `cli_version` | `1.47.1` | 请求中的 CLI 版本标识；未配置时也可由 `COMMANDCODE_CLI_VERSION` 提供 |
| `project_slug` | `cliproxyapi` | 请求中的项目标识 |
| `permission_mode` | `standard` | 传给上游的权限模式 |

`cli_version` 与模型名单的来源版本是两个独立设置。名单更新不会自动修改请求协议或版本标识。

密钥按以下顺序查找，使用第一个非空值：

1. 宿主为当前请求选择的 auth 属性 `api_key`。
2. 插件配置 `api_key`。
3. 环境变量 `COMMANDCODE_KEY`、`COMMANDCODE_API_KEY`、`COMMAND_CODE_API_KEY`。
4. `$COMMANDCODE_CONFIG_DIR/auth.json`、`~/.commandcode/auth.json`、`~/.config/commandcode/auth.json`。

文件路径中的 `~` 属于运行宿主的账号。插件支持登录文件中的 `apiKey` 和 `api_key` 字段。

重新配置时，非法配置会整体拒绝，省略的字段保留旧值。清空字符串字段可用 `null`，清空模型筛选可用 `[]`。

</details>

## 模型与版本更新

**模型名单：** GitHub Actions 每六小时检查 npm 上的 Command Code 包。包内的 `models.md` 是模型名单和最低套餐要求的唯一来源，只收录 Go 套餐模型；models.dev 用于补充输出长度、模态等元数据。生成和测试通过后，结果提交到本仓库。

插件默认按需获取最新名单：宿主调用插件的模型注册或查询接口时，才检查是否需要刷新远程数据，并非每六小时主动推送到宿主。刷新失败会保留上一份有效名单；首次获取失败则使用编译内置名单。因此，名单更新通常无需重编译插件，具体何时出现在客户端取决于宿主何时重新查询插件。

需要固定名单时，将本仓库的 `models.json` 保存到本地并设置 `models_file`。本地文件按修改时间重新读取；保留默认刷新设置即可。需要完全使用编译内置名单时，设置 `models_refresh_interval: "0"`。

**插件代码：** 拉取最新代码并重新编译，停止宿主、替换 `.so` 后再启动。升级前保留旧动态库和配置，出现问题时可恢复。动态库回滚不会固定远程模型名单。

## 兼容性

本项目与 Command Code / Langbase 无关联，使用 CLI 的未公开网关协议。上游协议或账号策略变化可能需要更新插件，模型名单更新无法解决所有兼容性问题。

- 工具调用支持消息转换与结果回传，但强制选择工具尚未完整支持。
- 图片和推理能力取决于所选模型；推理文本通过 `reasoning_content` 返回。
- models.dev 的输出长度是参考元数据，不代表 Command Code 网关确认的硬上限。
- 上游未返回 token 用量时，响应使用零值，不代表实际没有消耗。

更多协议与生命周期边界见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 排查问题

| 问题 | 检查 |
|---|---|
| 插件加载失败 | 宿主日志、插件目录、文件权限，以及 `.so` 的架构和 libc 兼容性 |
| 认证失败 | 插件使用的 Command Code 密钥是否有效，是否对宿主进程可见 |
| 客户端找不到模型 | `/v1/models` 的实际 ID、模型筛选配置，以及宿主是否重新查询了插件 |
| 日志出现 `model refresh failed` | 远程地址连通性或本地 JSON 文件；插件会继续使用有效的旧名单 |
| 有推理内容但正文为空 | 是否以 `finish_reason: length` 结束，token 预算是否已被推理消耗 |

## 开发

在仓库根目录运行：

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
(cd go && go vet ./... && go test -race ./...)
```

Python 测试需要 Python 3.11+。测试使用本地数据与模拟服务器，无需上游密钥。提交代码前也应运行上面的动态库构建命令。

- [ARCHITECTURE.md](ARCHITECTURE.md)：组件职责、协议转换与并发设计。
- [models 工作流](.github/workflows/models.yml)：模型获取、生成与验证流程。
- `python3 scripts/extract-models.py --help`：生成器参数。

## 许可证

[GPL-3.0](LICENSE)。
