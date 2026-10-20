# TTS 原生透传

Transit Hub 提供两个固定的 provider 命名空间入口，首期支持同步、非流式语音合成：

| POST 入口 | Hub protocol / endpoint key | 上游默认路径 |
| --- | --- | --- |
| `/bailian/v1/services/aigc/multimodal-generation/generation` | `dashscope_tts` | `/api/v1/services/aigc/multimodal-generation/generation` |
| `/minimax/v1/t2a_v2` | `minimax_tts` | `/v1/t2a_v2` |

这些命名空间表示上游厂商，和可自定义的 provider 配置 `name` 独立。同一原生协议可配置多个账号池或 provider 实例；模型仍按 `(protocol, public model)` 唯一。不同协议之间不回退路由。未开放通用任意路径代理，也不注册无命名空间的原生接口。

## 启用

复制 `configs/providers/bailian-tts.example.yaml`、`minimax-tts.example.yaml` 为同目录下不含 `.example.` 的 YAML，注入各自的 `BAILIAN_TTS_API_KEY`、`MINIMAX_TTS_API_KEY`，然后重启或调用 `POST /admin/providers/reload`。示例不会自动加载。

百炼必须使用标准 DashScope 服务与 API Key，不能直接复用现有百炼 Token Plan compatible-mode 地址。两个协议都要求模型 `type: tts`，也只接受 TTS 模型。上游端点可通过 `endpoints.<protocol>` 覆盖；默认路径对基础 URL 已含 `/v1`（MiniMax）或 `/api/v1`（DashScope）的情况自动去除重复前缀。

公开模型名为 `th-dashscope-qwen3-tts-flash` 和 `th-minimax-speech-2_8-hd`。客户端使用 Transit Hub 客户端 Key，并须有对应模型权限。账号池选择、并发限制、请求额度、金额额度、固定窗口和模型禁用沿用现有机制。

## 请求与响应

设置 `TRANSIT_HUB_URL` 为入口根地址（部署在子路径时包含子路径），`CLIENT_API_KEY` 为网关客户端 Key：

```bash
curl -sS "$TRANSIT_HUB_URL/bailian/v1/services/aigc/multimodal-generation/generation" \
  -H "Authorization: Bearer $CLIENT_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"th-dashscope-qwen3-tts-flash","input":{"text":"你好。","voice":"Cherry"}}'

curl -sS "$TRANSIT_HUB_URL/minimax/v1/t2a_v2" \
  -H "Authorization: Bearer $CLIENT_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"th-minimax-speech-2_8-hd","text":"你好。","stream":false,"output_format":"hex","voice_setting":{"voice_id":"male-qn-qingse"},"audio_setting":{"format":"mp3","sample_rate":32000}}'
```

只改写顶层 `model` 和上游鉴权；未知字段、大整数和厂商参数保留。HTTP 状态、响应体及音频字段原样透传。百炼的临时 URL 由客户端下载，MiniMax hex / 百炼 base64 由客户端解码；Hub 不下载、不持久化音频。`stream:true`、`X-DashScope-SSE: enable` 或请求 SSE 输出会在调用上游前被拒绝。

用量采集复用旁路解压，保留原始响应字节。增量 JSON 采集器跳过大字符串，最多保留 256 KiB 元数据和 4 KiB 单字符串；不会受通用 JSON 8 MiB 采样上限影响。元数据过大、响应不完整、不可解码或字段类型不合法时，用量 unavailable，不估算字符或 token。

上游响应头请求 ID 按现有 `requestIdHeader` 规则归一为 `X-Request-Id`。原生 body 中的 `request_id`（百炼）、`trace_id`（MiniMax）保持不变；上游无响应头 ID 时，用其补充日志 ID，不缓冲整个响应以补写响应头。

## 字符计费

`billing.mode: characters` 使用上游实际报告的计费字符：百炼 `usage.characters`、MiniMax `extra_info.usage_characters`。显式 0 与字段缺失分别处理；不按文本长度推算，不占用 token 额度。

价格 API 的 `unit` 仍是 `CREDITS`，字符费率位于现有 billing JSON 中，金额为整数字符串：

```json
{
  "protocol": "minimax_tts",
  "public_model": "th-minimax-speech-2_8-hd",
  "unit": "CREDITS",
  "input_microcredits_per_1m_tokens": "0",
  "output_microcredits_per_1m_tokens": "0",
  "billing": {
    "mode": "characters",
    "microcredits_per_10k_characters": "350000"
  }
}
```

上述 350000 仅为费率格式示例，即 0.35 Credits / 万计费字符，不代表官方价格或 CNY 换算。部署时在 Pricing 配置实际 Credits 售价；本次不自动写入价格。

费用 = characters × microcredits_per_10k_characters / 10000，使用大整数运算、最终一次四舍五入到 micro-Credits，溢出饱和。字符模式要求正费率，不接受 token / 图片价格；免费必须显式配置 `free`。TTS 只接受 characters/free 价格。缺少价格的金额受限 Key 会在调用上游前拒绝，无金额限制 Key 可继续调用并标记 unpriced。

MiniMax 非零 `base_resp.status_code`、百炼非空 `code` 是业务失败，即使 HTTP 200 也记录 `upstream_business_error`，不扣字符费用，原始 HTTP 和 body 不变。只将容量、限流、服务端错误用于熔断，参数错误不摘除账号，无自动重试。错误分类参考 [MiniMax 错误码](https://platform.minimax.io/docs/api-reference/errorcode) 和 [百炼错误码](https://help.aliyun.com/en/model-studio/error-code)。拿到上游响应仍计一次请求；调用上游前拒绝或没有上游响应只记日志。上游成功报告用量后，客户端后续下载或保存音频失败不撤销费用。

日志和 `/api/me/logs` 新增 `characters`，旧日志自动补 0；金额仍记入现有生命周期和窗口 Credits。TTS 不参与 token 估算校准。Admin 模型详情展示实际入口与上游地址，`/api/me/prices` 返回已授权 TTS 模型价格；`/v1/models` 继续仅列 OpenAI 协议模型。

Provider 管理页显式连通性测试会发起短文本语音合成、使用示例默认音色，有上游费用；不会下载音频或落盘。测试请求不计入客户端 Key 用量，和现有管理探测一致。

## Agent Platform 配置

新增 provider registry 文件，Key 使用 Transit Hub 客户端 Key：

```yaml
key: transit-hub-tts
baseUrl: https://<Transit Hub 入口根地址>
apiKey: <Transit Hub 客户端 Key>
requestIdHeader: X-Request-Id
protocols:
  DASHSCOPE_TTS:
    endpointPath: /bailian/v1/services/aigc/multimodal-generation/generation
  MINIMAX_TTS:
    endpointPath: /minimax/v1/t2a_v2
```

新增模型 registry 文件，保留原生协议：

```yaml
key: th-dashscope-qwen3-tts-flash
name: Qwen3 TTS Flash via Transit Hub
provider: transit-hub-tts
type: tts
protocol: DASHSCOPE_TTS
modelId: th-dashscope-qwen3-tts-flash
```

```yaml
key: th-minimax-speech-2_8-hd
name: MiniMax Speech 2.8 HD via Transit Hub
provider: transit-hub-tts
type: tts
protocol: MINIMAX_TTS
modelId: th-minimax-speech-2_8-hd
```

把 `tools.yml` 的 `speech-generate.profiles.*.model-key` 切换至相应新 key。Platform 的 CNY pricing 为自身费用展示，与 Hub 的 Credits 扣费独立配置；不根据 Hub 价格自动填充或换算。本次不修改正在使用的 Platform registry 或 profile。

Website 开发代理及生产 Nginx 均代理 `/bailian/` 和 `/minimax/`，支持 API base path、Ingress rewrite，关闭响应缓冲并使用长超时；自建外层反代也应保留两个命名空间。

## 验证

自动测试覆盖原生 body 透传、未知字段与大整数、鉴权替换、协议隔离、业务失败、零/缺失/非法字符数、字符计费与额度、gzip 旁路解析、超过 8 MiB 音频和单字节分块，以及上游路径覆盖。运行 `go test ./...`、`go test -race ./...`、Website `npm run build`、`sh scripts/prepare-nginx.test.sh`。

真实冒烟需在配置账号、模型授权与价格并重载后，执行上面的两条 curl（会产生上游费用），再用 Platform 的 `speech_generate` 确认音频下载、解码及保存均成功。自动测试不调用真实付费接口。

可选付费测试（需要另行授权，默认跳过）：设置 `TRANSIT_HUB_TTS_SMOKE=1`，并注入 `BAILIAN_TTS_API_KEY`、`MINIMAX_TTS_API_KEY` 后执行 `go test ./internal/gateway -run '^TestTTSProviderSmoke$' -count=1 -v`。也可通过 `TRANSIT_HUB_TTS_SMOKE_PROVIDER_DIR` 指向已有 Platform provider registry 目录，仅在明确启用测试时读取对应凭据。测试经内存中的 Gateway handler 调用真实上游，检查字符扣费、URL 下载与 WAV/MP3 文件头；音频不落盘，日志不输出凭据、签名 URL 或音频正文。

## 真实验证记录（2026-10-20）

经明确授权，使用现有 Platform provider 凭据，通过本地 Gateway handler 的两个公开命名空间入口，各合成一次“你好。”：

| 模型 | 上游计费字符 | 下载 / 解码后的音频字节 | 结果 |
| --- | ---: | ---: | --- |
| `qwen3-tts-flash` | 5 | 30764 | 临时 URL 下载、音频文件头、字符记账通过 |
| `speech-2.8-hd` | 5 | 16692 | hex 解码、音频文件头、字符记账通过 |

两次 token 均为 0，使用临时测试库费率验证扣费，未修改生产价格、账号或 Platform registry，音频仅在内存中处理。本记录验证真实厂商合成和 Gateway 处理链路；尚未部署到生产入口。
