# Transit Hub

Transit Hub 是一个 Go 1.26 LLM Chat API 中转网关。它对外提供 OpenAI 兼容和 Anthropic 兼容的 Chat 接口，对内按配置把公开模型名路由到上游 provider、上游模型、账号池和账号。

## 功能

- OpenAI 兼容接口：`POST /v1/chat/completions`
- Anthropic 兼容接口：`POST /v1/messages`
- 客户端 API Key 创建、禁用、过期、配额和用量累计
- 内部管理员账号登录、网页登录态和管理后台 API
- API Key 软删除、请求日志、流量报表、在线 device/session 统计
- 按模型维护价格表并记录估算成本
- 多 provider、多模型映射、多账号池、账号权重
- 上游账号熔断和冷却恢复
- Provider 配置热重载
- 按公开模型临时覆盖路由账号池

## 快速启动

### 1. 准备环境

```bash
cp .env.example .env
```

编辑 `.env`，至少修改：

```dotenv
ADMIN_TOKEN=replace-with-a-long-random-token
```

可选环境变量：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `ADDR` | `:8080` | 服务监听地址 |
| `DB_PATH` | `data/transit-hub.db` | SQLite 数据库路径 |
| `CONFIG_DIR` | `configs` | 配置根目录；provider 配置位于 `$CONFIG_DIR/providers` |
| `ISSUER_CONFIG_PATH` | `$CONFIG_DIR/issuer/config.yaml` | JWT grant 签发配置路径 |
| `ADMIN_TOKEN` | 无 | Admin API 令牌，必填 |
| `ADMIN_USERNAME` | `admin` | 首个内部管理员用户名；仅在 `ADMIN_PASSWORD` 设置时用于初始化 |
| `ADMIN_PASSWORD` | 无 | 首个内部管理员密码；为空时不会自动创建登录账号 |
| `ADMIN_SESSION_TTL` | `24h` | 管理站登录 Cookie 有效期 |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:5173` | 允许携带 Cookie 调用 Admin API 的前端 Origin，逗号分隔 |
| `COOKIE_SECURE` | `false` | 管理站 Cookie 是否仅允许 HTTPS |
| `SESSION_ACTIVE_WINDOW` | `5m` | 统计当前在线 device 的活跃窗口 |
| `LOG_LEVEL` | `info` | 日志级别预留 |
| `UPSTREAM_TIMEOUT` | `5m` | 上游请求超时 |
| `CIRCUIT_FAILURE_THRESHOLD` | `3` | 连续失败多少次后熔断账号 |
| `CIRCUIT_COOLDOWN` | `30s` | 熔断冷却时间 |

### 2. 配置上游 Provider

从示例复制真实配置：

```bash
cp configs/providers/deepseek.example.yaml configs/providers/deepseek.yaml
```

编辑 `configs/providers/deepseek.yaml`，填入真实上游 key：

```yaml
name: deepseek
protocol: openai
base_url: https://api.deepseek.com
default_pool: primary
headers: {}
models:
  - public: deepseek-chat
    upstream: deepseek-chat
    pool: primary
pools:
  - name: primary
    accounts:
      - name: deepseek-main
        api_key: sk-your-upstream-key
        weight: 1
```

说明：

- 运行时加载 `configs/providers/*.yaml` 和 `configs/providers/*.yml`。
- 文件名包含 `.example.` 的配置不会被加载。
- 真实配置里会包含上游密钥，已被 `.gitignore` 忽略。
- `protocol` 只能是 `openai` 或 `anthropic`。
- `endpoints.openai_chat_completions` 和 `endpoints.anthropic_messages` 可用于覆盖上游路径。

### 3. 配置 JWT Grant 签发密钥

如果需要开放自动发放桌面端 API Key 的接口，准备 issuer 配置和 RSA 密钥：

```bash
mkdir -p configs/issuer
openssl genrsa -out configs/issuer/private.pem 2048
openssl rsa -in configs/issuer/private.pem -pubout -out configs/issuer/public.pem
cp configs/issuer/config.example.yaml configs/issuer/config.yaml
```

`configs/issuer/config.yaml` 示例：

```yaml
private_key_path: private.pem
public_key_path: public.pem
issuer: transit-hub
audience: api-key-grant
default_jwt_ttl: 720h
default_api_key_request_quota: 50
default_api_key_token_quota: 100000
```

`private_key_path` 和 `public_key_path` 支持相对 issuer config 文件所在目录的路径。未配置 issuer 时服务仍会启动，但 `/api/apply-apikey` 和 `/admin/jwt-grants` 创建接口会返回 `503`。

### 4. 启动服务

本地启动：

```bash
make run
```

健康检查：

```bash
curl -sS http://localhost:8080/healthz
```

容器化部署：

```bash
docker network create transit-hub-net
docker compose up -d --build
```

容器启动前同样需要先准备 `.env`，并在 `configs/providers/` 下放置真实的 provider 配置。容器会固定监听 `:8080`，加入 external Docker network `transit-hub-net`，挂载 `./data` 保存 SQLite 数据库，并以只读方式挂载 `./configs` 读取 provider 和 issuer 配置。

生产部署时后端不映射宿主机端口，对外入口由 `transit-hub-website` 提供。website 容器会在同一个 `transit-hub-net` 网络内通过服务名 `transit-hub:8080` 访问后端。

## 创建客户端 API Key

Admin API 需要携带 `Authorization: Bearer $ADMIN_TOKEN` 或 `x-admin-token: $ADMIN_TOKEN`。
管理站登录后也会通过 HttpOnly Cookie 调用同一组 `/admin` 接口。

```bash
curl -sS -X POST http://localhost:8080/admin/api-keys \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"demo","request_quota":1000,"token_quota":100000,"allowed_models":["deepseek-chat"]}'
```

响应里的 `key` 只会返回这一次，请妥善保存。配额字段为 `0` 表示不限额。`allowed_models` 是该 key 可调用的公开模型名白名单；省略或传空数组表示不允许调用任何模型。

常用管理命令：

```bash
# 列出客户端 API Key
curl -sS http://localhost:8080/admin/api-keys \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# 查看单个客户端 API Key
curl -sS http://localhost:8080/admin/api-keys/key_xxx \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# 禁用客户端 API Key
curl -sS -X PATCH http://localhost:8080/admin/api-keys/key_xxx \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"status":"disabled"}'

# 强制过期
curl -sS -X PATCH http://localhost:8080/admin/api-keys/key_xxx \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"forced_expired":true}'

# 修改可调用模型白名单
curl -sS -X PATCH http://localhost:8080/admin/api-keys/key_xxx \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"allowed_models":["deepseek-chat"]}'

# 软删除客户端 API Key（历史日志保留）
curl -sS -X DELETE http://localhost:8080/admin/api-keys/key_xxx \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

## 创建 JWT Grant 并自动申请 dk API Key

管理员可以创建一个 JWT grant，设置该 JWT 最多能发放多少个 API Key。响应里的 `jwt` 只会返回这一次：

```bash
curl -sS -X POST http://localhost:8080/admin/jwt-grants \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"desktop rollout","issue_quota":100,"request_quota":50,"token_quota":100000,"allowed_models":[]}'
```

客户端拿到 JWT 后调用公开申请接口：

```bash
curl -sS -X POST http://localhost:8080/api/apply-apikey \
  -H "Authorization: Bearer $JWT_GRANT" \
  -H "Content-Type: application/json" \
  -d '{"name":"my desktop"}'
```

申请成功会返回一次性明文 `dk_...` API Key，并通过 `issuer_jti` 追溯到签发它的 JWT Grant。`issue_quota` 控制该 grant 最多发放多少个 API Key，`request_quota`、`token_quota`、`allowed_models` 控制每个新发出 API Key 的初始限制；额度字段为 `0` 表示不限额，`allowed_models: []` 表示不限制模型。

管理 grant：

```bash
# 列出 JWT grants（不会返回 jwt 明文）
curl -sS http://localhost:8080/admin/jwt-grants \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# 修改颁发额度或禁用 grant
curl -sS -X PATCH http://localhost:8080/admin/jwt-grants/jti_xxx \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"issue_quota":200,"request_quota":100,"token_quota":200000,"allowed_models":[],"status":"active"}'
```

## 管理网站

前端项目位于 `/Users/linlay/Project/zenmind-around/transit-hub-website`，使用 React + Vite。

```bash
cd /Users/linlay/Project/zenmind-around/transit-hub-website
npm install
npm run dev
```

默认开发地址为 `http://localhost:5173`，后端默认允许该 Origin 携带 Cookie。
生产环境推荐由 website 同源反代 `/admin` 到后端；此时浏览器不需要直接访问后端端口。若前端独立部署到其他域名，需要设置：

```dotenv
CORS_ALLOWED_ORIGINS=https://your-admin-site.example.com
COOKIE_SECURE=true
```

首次启用网页登录时，在后端 `.env` 设置 `ADMIN_USERNAME` 和 `ADMIN_PASSWORD`。
服务启动时如果该用户名不存在，会自动创建一个内部管理员账号；之后可在管理站 Users 页面增删账号或重置状态。

管理站包含：

- Dashboard：总请求、token、成本、错误率、活跃 device 和近期流量。
- API Keys：创建、编辑、软删除 key，查看剩余额度、日志和连接设备。
- Sessions：按 API key、device ID、source 查看当前连接。
- Traffic：全局流量和请求日志。
- Pricing：维护模型 input/output 每百万 token 单价，供成本估算使用。
- Providers：查看 provider、模型路由、pool 和账号熔断状态。
- Users：维护内部登录账号。

客户端可在代理请求中携带：

```http
x-device-id: macbook-pro
x-source: codex
```

带有 `x-device-id` 的请求会被纳入 session 统计；缺少这些 header 的旧客户端仍可正常调用代理接口。

## 调用代理接口

公开接口使用客户端 API Key，不使用 Admin Token。支持 `Authorization: Bearer <client-key>` 或 `x-api-key: <client-key>`。

OpenAI 兼容请求：

```bash
curl -sS http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $CLIENT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "deepseek-chat",
    "messages": [
      {"role": "user", "content": "hello"}
    ]
  }'
```

Anthropic 兼容请求：

```bash
curl -sS http://localhost:8080/v1/messages \
  -H "x-api-key: $CLIENT_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "your-anthropic-public-model",
    "max_tokens": 256,
    "messages": [
      {"role": "user", "content": "hello"}
    ]
  }'
```

## Provider 运维

查看当前 provider、模型、pool、账号熔断状态：

```bash
curl -sS http://localhost:8080/admin/providers \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

修改 `configs/providers/*.yaml` 后热重载：

```bash
curl -sS -X POST http://localhost:8080/admin/providers/reload \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

把某个公开模型临时切到指定 pool：

```bash
curl -sS -X PUT http://localhost:8080/admin/routes/deepseek-chat/pool \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"pool":"primary"}'
```

清除路由 pool 覆盖：

```bash
curl -sS -X DELETE http://localhost:8080/admin/routes/deepseek-chat/pool \
  -H "Authorization: Bearer $ADMIN_TOKEN"
```

## 开发命令

```bash
# 启动服务
make run

# 运行测试
make test

# 编译
make build

# 整理依赖
make tidy
```

## 安全和数据

- `.env`、真实 `configs/providers/*.yaml`、真实 `configs/issuer/*`、`data/`、SQLite 数据库文件不会提交到 Git。
- Admin Token 只用于管理接口，客户端请求必须使用创建出来的客户端 API Key。
- 上游密钥只应放在真实 provider YAML 中，不要放入示例文件。
- API Key 明文不入库，数据库保存哈希；但 SQLite 数据库仍包含用量和请求日志，应按敏感数据处理。
# transit-hub-server
