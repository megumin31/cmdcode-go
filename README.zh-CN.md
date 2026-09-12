# cmdcode-go

[English](README.md) | 中文

用于 Command Code Go 账号的非官方 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 插件。将 OpenAI chat 请求转换到 CLI 的 `/alpha/generate` 网关，支持流式响应、工具调用、推理内容和图片输入。

本项目与 Command Code / Langbase 无关。上游协议未公开，兼容性可能独立于模型名单发生变化。

## 快速开始

以下命令面向 **Linux**。构建产物必须匹配宿主的操作系统、架构和 libc；在宿主本机编译最简单，也可以使用兼容的构建环境。不要把 macOS 动态库复制到 Linux。

需要：与 [go/go.mod](go/go.mod) 中 SDK 版本兼容的 CLIProxyAPI v7、该文件要求的 Go 工具链、C 编译器及 libc 开发头文件，以及 Command Code 账号密钥。直接提供密钥时，不需要安装官方 CLI。

### 1. 编译

```bash
git clone https://github.com/megumin31/cmdcode-go.git
cd cmdcode-go
mkdir -p build
(cd go && CGO_ENABLED=1 go build -buildmode=c-shared -o ../build/cmdcode-go.so .)
file build/cmdcode-go.so
```

已有仓库时，在干净的 `main` 分支执行 `git pull --ff-only origin main`，不用重新克隆。确认 `file` 显示 Linux 动态库，且架构与宿主一致。

### 2. 配置宿主

将以下配置合并到宿主配置中，保留其他插件：

```yaml
plugins:
  dir: "plugins"
  configs:
    cmdcode-go:
      enabled: true
      priority: 1
      # api_key: "user_..."  # 或向宿主进程提供 COMMANDCODE_KEY。
```

密钥必须对**运行中的宿主进程**可见，不能只在交互终端中设置。服务进程可通过服务环境配置、插件配置或服务账号可读取的 CLI 登录文件获取密钥。

优先级：宿主 auth 属性 → 插件 `api_key` → `COMMANDCODE_KEY` / `COMMANDCODE_API_KEY` / `COMMAND_CODE_API_KEY` → CLI 登录文件。文件依次查找 `$COMMANDCODE_CONFIG_DIR/auth.json`、`~/.commandcode/auth.json`、`~/.config/commandcode/auth.json`。

### 3. 安装并重启

在仓库根目录执行，先填写**实际插件目录**。示例假设使用名为 `cliproxyapi.service` 的用户级 systemd 服务；其他部署方式请使用对应的服务管理命令。

```bash
PLUGIN_DIR=/absolute/path/to/cliproxyapi/plugins
(
  set -e
  mkdir -p "$PLUGIN_DIR"
  systemctl --user stop cliproxyapi.service
  if [ -f "$PLUGIN_DIR/cmdcode-go.so" ]; then
    cp -p "$PLUGIN_DIR/cmdcode-go.so" "$PLUGIN_DIR/cmdcode-go.so.bak"
  fi
  install -m 755 build/cmdcode-go.so "$PLUGIN_DIR/cmdcode-go.so.new"
  mv "$PLUGIN_DIR/cmdcode-go.so.new" "$PLUGIN_DIR/cmdcode-go.so"
  systemctl --user start cliproxyapi.service
)
```

先停止宿主，再替换已加载的动态库。条件备份也适用于首次安装。运行时只需要动态库，不需要安装生成的 C 头文件。

### 4. 验证

针对上面的 systemd 示例：

```bash
systemctl --user is-active cliproxyapi.service
journalctl --user -u cliproxyapi.service --since "5 minutes ago" --no-pager
```

确认宿主加载并注册插件，且没有配置或动态库加载错误。成功刷新名单**不要求出现某条成功日志**；使用编译名单兜底也是正常状态。

用宿主的客户端 API key 查询名单，**不是** Command Code 账号密钥：

```bash
CLIPROXY_URL=http://127.0.0.1:8317  # 替换为宿主地址。
curl --fail-with-body -sS "$CLIPROXY_URL/v1/models" \
  -H "Authorization: Bearer $CLIPROXY_API_KEY"
```

执行前将 `CLIPROXY_API_KEY` 设置为宿主客户端密钥。将返回的 ID 与 [models.json](models.json) 对照；宿主可能配置前缀或别名，不要要求所有 ID 都以 `cmdcode-go/` 开头。名单注册只证明加载成功，不代表上游账号可用。验证推理时，从返回名单选择一个 ID 发起简短聊天；这会实际调用上游。

## 配置参考

插件选项都放在 `plugins.configs.cmdcode-go` 下。

| 选项 | 默认值 / 行为 |
|---|---|
| `api_key` | 可选；密钥优先级见上文 |
| `base_url` | `https://api.commandcode.ai`；可用 `COMMANDCODE_BASE_URL` 覆盖默认值 |
| `cli_version` | 优先 `COMMANDCODE_CLI_VERSION`，否则 `1.47.1`；协议指纹，与名单版本分开 |
| `project_slug` | `cliproxyapi` |
| `permission_mode` | `standard` |
| `models` | 空表示整个名单；支持 YAML 列表 |
| `disable_models` | 空表示不排除；优先于 `models` |
| `models_url` | raw.githubusercontent.com 上本仓库的 `main/models.json` |
| `models_file` | 本地 JSON 文件，优先于 `models_url` |
| `models_refresh_interval` | `"6h"`；`"0"` 使用编译名单并关闭 URL/文件刷新 |

模型名称支持完整 ID、唯一短名和 `cmdcode-go/` 前缀。非法配置整体拒绝；重新配置时省略的字段保留旧值，使用 `null` 或空列表清空相应值。

不要自动把 `cli_version` 设置成模型名单版本：名单更新并不证明新版协议指纹兼容。

## 模型更新

[models 工作流](.github/workflows/models.yml) 每六小时运行一次，锁定 npm 精确版本，读取包内 `models.md`，只收录文档标为 Go 的模型。[models.dev](https://github.com/anomalyco/models.dev) 只补元数据，不改变成员关系。验证通过后同时更新 JSON 和编译兜底名单。

运行时采用**按需刷新**：宿主调用模型注册/列表接口时检查六小时 TTL，没有独立的后台计时器。本地文件按修改时间检查。失败保留上一份有效名单，冷却五分钟，不会用空名单覆盖；抓取超时八秒。

仅更新名单通常不需要重新编译。重启宿主会重置内存刷新状态；只修改未变更配置文件的时间戳，不能保证刷新。需要固定版本或离线名单时使用 `models_file`；校验与格式细节见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 升级与回滚

代码升级：更新干净的仓库，依次执行上面的**编译 → 安装并重启 → 验证**。确认成功前保留备份。

恢复旧动态库时，先按上文设置 `PLUGIN_DIR`：

```bash
test -f "$PLUGIN_DIR/cmdcode-go.so.bak" &&
systemctl --user stop cliproxyapi.service &&
cp -p "$PLUGIN_DIR/cmdcode-go.so.bak" "$PLUGIN_DIR/cmdcode-go.so.restore" &&
mv "$PLUGIN_DIR/cmdcode-go.so.restore" "$PLUGIN_DIR/cmdcode-go.so" &&
systemctl --user start cliproxyapi.service
```

回滚后再次验证。这只恢复动态库；如改过配置，需单独恢复。远程名单仍跟随 `main`，除非固定本地文件或关闭刷新。

## 常见问题

| 现象 | 检查方向 |
|---|---|
| `invalid ELF header`、架构错误、缺少 libc 符号 | 构建系统、架构、libc 兼容性和 CGO 工具链 |
| 宿主看不到插件 | 实际插件目录、启用配置、文件权限、宿主日志 |
| 缺少密钥 / 认证失败 | 服务账号的环境和登录文件；区分宿主客户端密钥与上游账号密钥 |
| 名单没有变化 | 按需刷新触发、TTL、文件修改时间及五分钟失败冷却 |
| `model refresh failed` | URL、网络或 JSON 校验；旧名单/编译名单仍可用 |
| 上游拒绝版本 | 配置的协议指纹与实际错误响应，不要由名单版本猜测 |
| 空回答且 `finish_reason: length` | 推理可能耗尽预算；检查 reasoning 内容与请求 token 预算 |
| 关闭耗时 / 流被截断 | 查看日志；shutdown 等待回调结束，缺少 finish 事件会报错 |

## 限制与开发

上游未提供用量时返回零值，不代表实际零消耗。fallback 输出预算只用于默认请求；显式预算只受已确认的 `gateway_output_limit` 限制。强制选择工具尚未完整支持。客户端断开通过宿主 emit 失败感知；静默上游可能持续到请求超时。详见 [架构与协议边界](ARCHITECTURE.md)。

在仓库根目录执行：

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
(cd go && go vet ./... && go test -race ./...)
```

Python 测试需要 Python 3.11+。Go 测试使用本地服务器和注入回调，不需要上游密钥；首次下载构建/测试依赖需要网络。CI 还会构建 c-shared 动态库。这些检查不代表已验证当前线上网关兼容性。

生成器参数见 `python3 scripts/extract-models.py --help`，完整下载与生成流程见工作流文件。五组件职责和源码分工见 [ARCHITECTURE.md](ARCHITECTURE.md)。
