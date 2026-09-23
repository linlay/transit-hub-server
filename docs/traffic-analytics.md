# Traffic 分析接口

管理端 `GET /admin/traffic/analytics` 返回筛选后的完整日志统计，使用现有 Admin 鉴权。与请求日志共用筛选条件，不受日志分页上限影响，不修改计费余额。

## 查询参数

| 参数 | 含义 |
| --- | --- |
| `from` / `to` | RFC3339 时间范围；`from` 必须早于 `to` |
| `exclusive_end=true` | 使用 `[from, to)`；Traffic UI 始终传入。未传时保留旧日志接口的包含结束时间行为 |
| `bucket` | `hour`、`day`、`month`，默认 `day` |
| `timezone_offset` | 归桶时相对 UTC 的分钟偏移，范围 -720 至 840，默认 0 |
| `api_key_id` | 兼容旧接口的单个 Key ID |
| `api_key_ids` | JSON 字符串数组，最多 100 项；数组内为 OR |
| `models` | 公开模型名的 JSON 字符串数组，最多 100 项；数组内为 OR |
| `provider` | Provider 名称精确匹配 |
| `status` | `success` 或 `failed`，省略表示全部 |

不同维度之间按 AND 组合。数组通过 URL 编码传递，模型名中含逗号也不会拆分。空数组表示不限，数组中的空字符串可以匹配历史未标注的 Key 或模型。

失败定义沿用现有统计：`status_code >= 400 OR error_type <> ''`。即使 HTTP 状态为 200，流式错误也计入失败。

## 响应

- `items`：逐时间桶的 `TrafficBucket`，包括按模型的请求数和词元数。
- `summary`：整个时间范围的聚合。活跃 Key 使用区间去重数量，平均耗时按每次请求平均，不能直接累加或平均每日统计。
- `models` / `keys`：全部匹配模型和 Key 的排行数据，每行含 `id`、`name`、`requests`、`total_tokens`、`cost_micro`。前端选择排序指标。
- `options.keys` / `options.models` / `options.providers`：来自保留日志的历史筛选选项，不随当前筛选收缩，因此历史已删除的 Key 和模型仍可分析。

`GET /admin/traffic` 同步支持这些过滤参数。`GET /admin/logs` 支持同样过滤参数，并继续使用 `limit`、`offset` 分页，返回匹配条件的 `total`。

## Website 行为

Traffic 默认最近 7 个自然日（含今天）、按天、北京时间 UTC+8；支持 UTC、自定义起止日期、小时和月粒度。自定义结束日期包含当天，由前端换算为下一天零点的排他边界。统计和日志时间统一使用所选时区。

图表包括调用趋势、活跃度和花费、模型排行、API Key 排行、词元与缓存、请求质量。选中单个 Key 后隐藏 Key 排行。所有筛选条件保存在页面 URL。

日志在独立弹框中每页显示 50 条，弹框打开期间固定当前统计时间范围；关闭后恢复近期窗口每 30 秒刷新。图表的 Credits 由 `cost_micro / 10000` 换算，汇总显示四舍五入后的整数，不附加单位后缀；日志保留精确小数。

统计基于保留的 telemetry 日志，时间为请求完成时间。日志清理会影响历史统计，不能用于替代 usage 库中的生命周期余额。

## 验证

- `go test ./...`
- Website：`npm run build`
- Website：`node scripts/traffic-filters.test.mjs`

新增用例覆盖组合筛选、模型名安全匹配、超过 100 条的完整统计、去重 Key、分页总数、失败口径、时区跨月与排他结束边界，以及 HTTP 鉴权和图表/日志过滤一致性。
