# WorkBuddy Proxy

> WorkBuddy CN（CodeBuddy / copilot.tencent.com）的 OpenAI 兼容反向代理，支持 OAuth 登录、多账号轮转、多密钥管理、工具调用与流式响应。

> ⚠️ **本项目仅供学习和技术交流使用，请勿用于任何商业用途。** 使用本项目需自行遵守 WorkBuddy / CodeBuddy 的服务条款，一切使用风险由使用者自行承担。

## 项目来源

本项目基于 [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api)（MIT License）修改而来，感谢原作者的工作。相对上游的主要改动（多密钥管理、Admin 控制台、Windows 部署脚本等）详见 [CHANGELOG.md](CHANGELOG.md)。

## 功能特性

- 🔐 **OAuth 登录** — 通过 `/v2/plugin/auth/state` 设备授权流程获取凭证，支持 token 自动刷新
- 🔄 **多账号轮转** — 三因子加权随机选号（credits ×闲置×成功率），防热点 + 防惊群（100ms 窗口）
- 🔑 **多密钥管理** — `/admin` 页面创建/启停/删除附加 API Key，掩码显示一键复制，即时生效、跨重启保留
- 🖥 **Admin 控制台** — 左侧边栏多页面布局（运行概览/账号池/密钥管理/模型白名单/请求日志），点击栏目即跳转，窄屏自动收起为抽屉
- 🛠 **工具调用** — 完整支持 OpenAI tools/tool_choice，流式 `tool_calls` 按 index 合并
- 📡 **流式 + 非流式** — 上游 SSE 透传；非流式本地聚合（上游拒绝非流式请求）
- ⏰ **定时签到** — 每日 09:00 / 21:00 自动签到 + 积分查询，积分耗尽账号次日 04:00 自动恢复
- 📊 **积分监控** — `/admin` 控制台一键查询全部账号剩余/总量/百分比，剩余积分随每笔请求准实时扣减（签到时对账校正）
- 📈 **用量统计** — 今日/累计 Tokens、请求、实扣积分持久化统计（跨重启保留）+ 7 天趋势图 + 模型分布环形图；页头日历可回看最近 30 天任一天用量（明细保留 30 天）
- 🚀 **一键启动** — `launch.vbs` 桌面快捷方式：无黑窗自动拉起服务，Chrome 应用窗口打开控制台
- 🔑 **登录工具** — `login-workbuddy.ps1` 交互式登录（Windows 原生），落盘即生效
- 🏗 **Docker 部署** — 一键 `docker compose up`，healthcheck 常驻
- 📈 **请求级日志** — `/admin` 请求记录表（seq/时间/模型/首字/耗时/uid/tokens/积分）+ 500 条环形持久化 + 模型/账号/模式/状态筛选与 CSV 导出
- 🏥 **健康检查** — `/healthz` 无健康账号时返回 503，可接负载均衡器
- 📉 **状态汇总** — `/status` 返回 total/healthy/cooling/disabled 计数 + 每账号完整画像

## 快速开始（Windows 本地部署）

### 方式一：下载编译好的 exe

1. 从 [Releases](../../releases) 下载 `workbuddy-proxy-windows-amd64.exe`，放到项目目录
2. `cp config.example.json config.json`，编辑 `api_key`（调用方鉴权用）
3. 运行 `login-workbuddy.ps1` 完成 WorkBuddy 账号 OAuth 登录（落盘 `auths/`）
4. 双击 `start-workbuddy.cmd` —— 启动成功会自动用 Chrome 打开 `/admin` 控制台
5. （可选）右键 `launch.vbs` 发送到桌面快捷方式，以后双击图标即可：服务未运行时自动拉起（约 3~15 秒），并以无地址栏的 Chrome 应用窗口打开控制台

### 方式二：Docker

```bash
git clone https://github.com/myzane678/WorkBuddy-Proxy.git
cd WorkBuddy-Proxy
cp config.example.json config.json
# 编辑 config.json，设置 api_key
./login.sh        # 上游自带登录脚本（Windows 用户建议用 login-workbuddy.ps1）
docker compose up -d --build
```

### 验证

```bash
# 模型列表
curl -s http://localhost:7863/v1/models -H "Authorization: Bearer your-api-key"

# 账号状态（汇总 + 每账号详情）
curl -s http://localhost:7863/status -H "Authorization: Bearer your-api-key"

# 健康检查（无健康账号时 503）
curl -s http://localhost:7863/healthz

# 聊天补全（流式）
curl -sN http://localhost:7863/v1/chat/completions \
  -H "Authorization: Bearer your-api-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true}'
```

## Admin 控制台

浏览器打开 `http://127.0.0.1:8091/admin`（端口以 config.json 的 `listen` 为准），左侧边栏点击栏目切换页面：

| 栏目 | 说明 |
|---|---|
| 运行概览 | 今日/累计 Tokens、今日请求、实扣积分、剩余积分、平均首字统计卡 + 签到状态（支持手动触发）+ 最近 7 天 Token 趋势图 + 模型分布环形图（今日/累计切换） |
| 账号池 | 每账号健康状态、积分、成功/失败/在途计数 |
| 密钥管理 | 主 Key 查看/复制/修改；附加密钥创建/启停/删除，掩码显示、即时生效 |
| 模型白名单 | 多列网格勾选放行的模型（不勾 = 放行全部），全选/清空快捷操作，即时生效 |
| 请求日志 | 最近请求（模型/状态/首字/TTFB/tokens/积分/耗时），模型/账号/模式/状态筛选 + CSV 导出，5s 自动刷新，环形缓冲 500 条跨重启保留 |

统计卡与图表数据来自 `/admin/api/stats` 持久化统计（按「天 × 模型」聚合落盘 `data/stats.json`，天桶保留 30 天，累计总量不受修剪影响），自 v1.1.0 启用后开始累积。

## 配置说明

> **权威字段定义见 [`config.example.json`](config.example.json)**：它是当前 schema 的唯一权威，下方样例与之保持一致。`cp config.example.json config.json` 即可得到完整默认配置。

```json
{
  "listen": ":7863",
  "api_key": "your-api-key-here",
  "auth_dir": "./auths",
  "state_file": "./data/state.json",
  "region": "cn",
  "model_allowlist": [],
  "cooldown": {
    "soft_rate": "60s"
  },
  "schedule": {
    "checkin_hours": [9, 21],
    "keepalive_hours": [22]
  },
  "upstream": {
    "timeout_seconds": 120
  },
  "features": {
    "sanitize_blacklist_fingerprints": true
  },
  "upstash": {
    "url": "",
    "token": ""
  },
  "pool": {
    "max_in_flight": 3,
    "breaker_threshold": 3,
    "breaker_cooldown": "30m",
    "breaker_cooldown_max": "6h",
    "idle_weight_per_hour": 0.5,
    "idle_weight_max": 5.0
  },
  "session_sticky": {
    "enabled": true,
    "ttl": "30m",
    "gc_interval": "5m"
  }
}
```

**注意**：`cooldown.hard_credit` / `cooldown.err_threshold` / `cooldown.err_cooldown` 三个历史键已退役。硬冷却固定为**次日 04:00**（本地时区，`CooldownUntilTomorrow4AM`），连续错误语义并入熔断器（`pool.breaker_threshold` 触发指数退避）。旧配置中的这些键因 JSON 未知字段被自然忽略，不报错。

## 账号轮换与冷却策略

### 状态机

```
Healthy → Cooling → (签到恢复) → Healthy
   ↓           ↑
Disabled ←────┘ (session 死亡，永久)
```

### 错误分类

| 错误类型 | 冷却策略 | 恢复方式 |
|---|---|---|
| **402 + 余额关键词** | 冷却到**次日 04:00** | 签到任务（09:00/21:00）自动恢复 |
| **429 限流** | 60s 短冷却 | 到期自动恢复 |
| **401 + session 死亡** | **永久禁用** | 人工重新登录 |
| **404 上游偶发** | 60s 短冷却（不累计错误计数） | 到期自动恢复 |
| **5xx 上游故障** | 喂熔断计数（`pool.breaker_threshold` 触发指数退避熔断） | 熔断到期自动恢复 / 成功清零 |
| **网络抖动** | **不计失败**，立即换号重试 | 即时 |

### 挑选策略

1. **状态过滤**：Disabled / Cooling / 熔断 / 在途占满 不选
2. **Top-5 候选**：按三因子权重降序取前 5（credits 只是权重的一个因子，闲置补偿与成功率同样决定谁进短名单）
3. **三因子加权随机**：权重 = credits 比例 ×10 + 闲置补偿 + 成功率 ×3（credits 全 0 仍按闲置+成功率加权）
4. **防惊群**：跳过 100ms 内刚被选中的账号（除非 top5 全部刚被用过，退回 LRU）

## 账号池 v3

在 v2 基础上吸收外部项目成熟设计，引入四块能力：

- **熔断器（指数退避）**：连续 `pool.breaker_threshold` 次失败熔断，退避 `breaker_cooldown × 2^retryCount` 封顶 `breaker_cooldown_max`；成功清零。单一连续失败计数器 `fails`，签到解冻只清冷却（余额恢复）不动熔断——熔断作为"连续 5xx"信号要到退避到期或下次 chat 成功才恢复。
- **三因子加权选取**：`credits 比例 ×10 + idleWeight + successRate ×3`。闲置补偿每小时 `+idle_weight_per_hour`（封顶 `idle_weight_max`），成功率无记录给中性 1.5。
- **在途租约**：单账号并发上限 `pool.max_in_flight`（0 = 不限），`Pick` 跳过占满账号。
- **会话粘性路由**：同一 `metadata.conversation_id`/`conversation_id`/`metadata.user_id` 尽量绑定同一账号，TTL 滚动续期；请求失败自动解绑回落轮换，请求成功后会话绑定**跟随最终成功号**。
- **全冷却兜底**：无 healthy 账号时从冷却账号选最早到期者顶班（禁用与余额耗尽号永不参与）。

### Redis（Upstash）镜像

- 配置 `upstash.url/token`（空 = 纯内存模式，一切功能照常，只打一条启动警告）。
- Redis 仅做异步镜像（粘性会话映射防重启丢失 + 池状态快照恢复备份），**不在请求热路径同步调用**。
- 池状态快照：每次本地 `state.json` 落盘同步镜像一份到 Redis（带 `saved_at`）；启动时**择新恢复**——Redis 快照比本地新才采用，否则本地优先。
- `/status` 透出 `redis_mode`（`upstash`/`noop`）与池级 `sticky_sessions`。

### 请求级日志

每个 `/v1/chat/completions` 请求结束后打一行表格日志到 stdout：

```
| #001 | 18:31:31 | deepseek-v4 | stream | 200 | uid=0851ce35 | TTFB=801ms | tok=60 | 23.5tok/s | total=2.6s |
```

字段说明：
- `#001`：请求序号（进程级 atomic counter）
- `TTFB`：首 token 到达时间（stream 模式）
- `tok`：输出 token 数（从上游 usage.completion_tokens 精确读取，非估算）
- `uid`：账号 UID 前 8 位

## 工具脚本（Windows 本地）

| 脚本 | 用途 |
|---|---|
| `start-workbuddy.cmd` | 启动（watchdog 守护，无窗口） |
| `stop-workbuddy.cmd` | 停止（watchdog + proxy 双杀） |
| `login-workbuddy.ps1` | OAuth 登录，落盘 auths/（替代上游 login.sh，Windows 原生） |
| `/admin` 控制台 | 积分查询 / 批量签到 / 白名单 / 请求日志（已内置，替代 signin.sh、credit.sh） |

> 上游自带的 `login.sh` / `signin.sh` / `credit.sh` 为 bash 脚本，Windows 本地部署已移除，功能由上表覆盖。

## API 端点

| 端点 | 鉴权 | 说明 |
|---|---|---|
| `POST /v1/chat/completions` | Bearer | OpenAI 兼容聊天补全（流式/非流式） |
| `GET /v1/models` | Bearer | 模型列表（动态拉取 + 静态兜底） |
| `GET /status` | Bearer | 账号状态汇总（total/healthy/cooling/disabled + 每账号详情） |
| `GET /healthz` | 无 | 健康检查（无健康账号时 503） |

## 稳定性设计

- **防雪崩**：上游 4xx/5xx 轮转重试（不直接返回），404 短冷却 60s 不累计失败
- **错误分流**：网络层错误不计失败（避免抖动连坐）；HTTP 5xx 喂单一连续失败计数器，达 `breaker_threshold`（默认 3）触发指数退避熔断
- **请求体上限**：`maxBodyBytes` = 32 MiB，超限由 `http.MaxBytesReader` 返回 **413 `request_too_large`**
  并落 stderr 日志。**勿改回 `io.LimitReader`**——它会静默截断超限请求体，使 JSON 变半截、
  被下游误报成「model 缺失 / 白名单错误」（贴多张大图的会话必踩，排查代价极高）
- **请求日志**：表格日志（seq/TTFB/uid/tokens/latency）便于排查慢请求
- **连接池**：`MaxIdleConnsPerHost=20` 减少 TLS 握手
- **凭证续期**：token 临近过期自动 refresh，失败禁用账号
- **状态持久化**：`data/state.json` dirty flag + 5s 周期异步落盘，进程退出 `Pool.Close()` 停 flusher 并补一次落盘
- **防惊群**：100ms 窗口内不重复选中同一账号（高并发时打散热点）

## 开发

### 测试

```bash
go build ./...
go test ./... -count=20  # 20 次全绿（pool 已修 TestAutoFlush 偶发 flake，高频重复验证过）
go vet ./...
```

> **关于 `gofmt -l .`**：本仓库文件为 CRLF 行尾（`core.autocrlf=true` 的 Windows 工作区），
> `gofmt -l .` 会把全部 `.go` 文件标记为需重写——这是行尾格式差异、**非代码格式缺陷**：
> 将任一文件转为 LF 后 `gofmt -l` 即为空。如需本地校验格式，先把文件行尾转成 LF。

### 代码结构

```
cmd/
  server/     # 主服务入口
  login/      # OAuth 登录工具
  credit/     # 积分查询工具
  signin/     # 批量签到工具
internal/
  auth/       # auth 文件解析 + token 刷新
  pool/       # 账号池（状态机 + 冷却 + 持久化）
  scheduler/  # 定时签到 + 积分查询
  server/     # HTTP handler + 请求日志
  upstream/   # 上游 API 封装（chat/billing/auth）
```

## 免责声明

本项目仅供学习和技术交流使用，请勿用于任何商业用途。使用者需遵守 WorkBuddy / CodeBuddy 的服务条款，自行承担使用风险。作者不对任何因使用本项目产生的直接或间接损失负责。本项目与 WorkBuddy / CodeBuddy 官方无关。

## 更新日志

查看 [CHANGELOG.md](CHANGELOG.md)。

## License

[MIT](LICENSE)
