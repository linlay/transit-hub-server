# AGENTS.md

## 项目定位

Transit Hub 是一个 Go 编写的 LLM Chat API 中转网关。它对外提供 OpenAI 兼容的 `POST /v1/chat/completions` 和 Anthropic 兼容的 `POST /v1/messages`，对内根据配置文件把公开模型名路由到上游 provider、模型、账号池和账号。

项目核心目标：

- 统一客户端入口和客户端 API Key 管理。
- 支持多上游 provider、多模型映射、多账号池和权重轮询。
- 记录请求用量和请求日志，支持请求数、token、金额固定窗口限流和过期控制。
- 在上游异常时用简单熔断机制临时摘除不健康账号。
- 通过 Admin API 动态管理客户端 Key、查看 provider 状态、重载 provider 配置、覆盖模型路由池。

## 架构结构

```text
cmd/transit-hub/
  main.go                 服务入口：加载环境、打开 SQLite、加载 provider 配置、启动 HTTP server。

internal/config/
  config.go               环境变量和 provider YAML 的加载、校验。

internal/gateway/
  server.go               HTTP 路由定义。
  proxy.go                客户端鉴权、请求解析、模型改写、转发上游、响应透传、用量记录。
  admin.go                Admin API：API Key、provider 状态、配置重载、路由池覆盖。
  json.go                 JSON 响应和错误响应工具。

internal/provider/
  registry.go             provider/route/pool/account 注册表，模型解析，权重选号。
  circuit.go              账号级熔断器。

internal/store/
  store.go                SQLite schema、API Key、请求日志、路由覆盖。

internal/usage/
  usage.go                usage 字段提取和缺失 usage 时的 token 估算。

configs/
  providers/             provider 配置目录。
  issuer/                JWT grant issuer 配置和密钥目录。
```

## 运行时流程

1. `main.go` 调用 `config.LoadEnv()` 读取 `.env` 和系统环境变量。
2. `store.Open()` 打开 SQLite，自动创建 `api_keys`、`request_logs`、`route_overrides` 表。
3. `config.LoadProviderConfigs()` 读取 `CONFIG_DIR/providers` 下非 `*.example.yaml` 的 YAML 文件。
4. `provider.NewRegistry()` 根据 provider 配置构建公开模型到上游模型、账号池的路由表。
5. `gateway.New(...).Handler()` 注册公开代理接口和 `/admin` 管理接口。
6. 公开请求先校验客户端 API Key，再解析 `model`，查找路由，应用数据库中的 pool override，选择健康账号，改写请求体中的 `model` 后转发上游。
7. 响应头和响应体会尽量透传；SSE 请求会 flush。请求完成后写入用量和日志。

## 配置定义

### 环境变量

- `ADDR`：监听地址，默认 `:8080`。
- `DB_PATH`：SQLite 路径，默认 `data/transit-hub.db`。
- `CONFIG_DIR`：配置根目录，默认 `configs`；provider 配置位于 `CONFIG_DIR/providers`。
- `ADMIN_TOKEN`：Admin API 令牌，必填。
- `LOG_LEVEL`：预留日志级别，默认 `info`。
- `UPSTREAM_TIMEOUT`：上游 HTTP 超时，默认 `5m`。
- `CIRCUIT_FAILURE_THRESHOLD`：账号连续失败多少次后打开熔断，默认 `3`，必须大于等于 1。
- `CIRCUIT_COOLDOWN`：熔断冷却时间，默认 `30s`，必须为正 duration。

### Provider YAML

运行时只加载 `CONFIG_DIR/providers` 下的 `.yaml` 或 `.yml`，并跳过文件名包含 `.example.` 的示例文件。真实配置通常从 `configs/providers/*.example.yaml` 复制得到，且被 `.gitignore` 忽略。

主要字段：

- `name`：provider 唯一名称。
- `protocol`：`openai` 或 `anthropic`。
- `base_url`：上游基础 URL，必须包含 scheme 和 host。
- `default_pool`：默认账号池；为空时使用第一个 pool。
- `headers`：provider 级固定请求头。
- `endpoints`：可选路径覆盖，例如 `openai_chat_completions: /v1/chat/completions`。
- `models`：公开模型到上游模型的映射。
- `pools`：账号池列表，每个 pool 至少一个 account。
- `accounts[].api_key`：上游账号密钥，只放在真实配置里。
- `accounts[].weight`：权重；`0` 会在运行时按 `1` 处理，负数非法。
- `accounts[].auth_header` / `auth_scheme`：覆盖上游鉴权头。OpenAI 默认 `Authorization: Bearer ...`，Anthropic 默认 `x-api-key: ...`。

## 数据模型

- `api_keys`：保存客户端 API Key 的哈希、状态、过期时间、强制过期标记、生命周期配额、固定窗口限流、已用量。
- `request_logs`：保存每次请求的 key、协议、模型、provider、pool、account、状态码、延迟、token、错误类型。
- `route_overrides`：保存公开模型到 pool 的临时覆盖，用于快速切换账号池。

配额规则：

- `request_quota = 0` 表示请求数不限。
- `token_quota = 0` 表示 token 不限。
- `rate_limits` 支持 `1h`、`5h`、`1d`、`7d`、`30d` 固定窗口，窗口内的请求数、token 和金额额度为 `0` 时表示不限。
- token 优先读取上游响应里的 `usage`；缺失时按请求和响应字节数粗略估算。

## 注意事项

- 不要提交 `.env`、真实 `configs/providers/*.yaml`、真实 `configs/issuer/*`、SQLite 数据库或上游密钥。
- `*.example.yaml` 是模板，不会被运行时加载。修改真实 provider 配置后，需要重启服务或调用 `POST /admin/providers/reload`。
- Admin API 支持 `Authorization: Bearer $ADMIN_TOKEN` 或 `x-admin-token: $ADMIN_TOKEN`。
- 公开代理接口支持 `Authorization: Bearer <client-key>` 或 `x-api-key: <client-key>`。
- 创建客户端 API Key 时，明文 key 只会在创建接口响应里返回一次。
- 当前路由覆盖以公开模型名为 key；同名公开模型跨协议使用时需要谨慎，因为覆盖存储没有协议维度。
- 上游返回 `429` 或 `5xx` 会记为不健康，可能触发账号熔断；冷却后进入 half-open 再试。
- 添加公开接口或协议时，要同步更新 `internal/gateway/server.go` 路由、`provider.EndpointPath` 的 endpoint key 约定、README 操作示例和相关测试。
- 修改配置校验或路由逻辑时，至少运行 `go test ./...`。

## 开发约定

- 保持依赖简单，优先使用标准库和项目已有模式。
- HTTP handler 返回 JSON，错误格式应通过 `writeError` 保持一致。
- 代理路径上的改动要注意流式响应、hop-by-hop header、鉴权头剥离和用量记录。
- Store 层负责数据库 schema 和持久化规则，Gateway 层不要直接拼接 SQL。
- Provider registry 是内存态，重载时整体替换；不要在请求路径中长期持有会被替换的全局可变状态。
