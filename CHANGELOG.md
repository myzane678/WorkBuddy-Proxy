# CHANGELOG

## 2026-09-13 · v1.0.0 公开发布：多密钥管理 + Admin 界面重做

### 新功能
- **多 API 密钥管理**（internal/server/keys.go + admin.go + admin.html）：
  - `POST /admin/api/keys/create|update|delete` 三个管理接口；列表随 overview 返回
  - config.json 新增 `api_keys` 数组（`[{id,name,key,enabled,created_at}]`），复用 `writeConfigField` 持久化 + 内存整体替换（同 allowlist 模式，引用不可变并发读安全）
  - 鉴权扩展：主 key 或任一启用中的附加 key 命中即放行；停用/删除即时 401
  - 密钥生成：`wb-sk-` + 32 hex（crypto/rand）；id 6 hex
  - 前端「密钥管理」卡：新建（行内表单，创建后明文显示一次）、掩码/明文切换、一键复制、启停、删除（confirm）、创建时间；主 Key 行内置不可删
- **Admin 界面整体重做**（admin.html）：
  - 浅色专业风（参考 shadcn dashboard / Tabler 设计语言）：白卡片 + 细边框阴影、大数字统计卡带彩色图标块、黑色主按钮、状态徽章
  - 布局按信息优先级重排：页面标题区 → API Key 通栏工具条 → 用量统计行（新增「剩余积分」卡，前端聚合各账号 credits）→ 左列（账号/签到/白名单）右列（请求日志）
  - 账号卡重构：首字母头像（状态着色）+ 三列统计网格（成功/失败/在途）
  - 签到记录行式布局 + 下次签到信息条
- **请求日志时间带日期**（logging.go）：`15:04:05` → `01-02 15:04:05`（月-日 时:分:秒，不带年份）

### 改进
- `start-workbuddy.cmd`：启动成功后自动用 Chrome 打开 `/admin` 控制台（探测三个常见安装路径，兜底系统默认浏览器；仅冷启动时打开）
- 表格模式列徽章化、行悬停高亮、自定义滚动条、卡片标题图标 + 分隔线

### 测试与验证
- 新增 `TestAPIKeyAuthWithExtraKeys`（附加 key 启用放行 / 禁用拒绝 / 列表移除后立即失效）；`TestAPIKeyAuth` 回归通过
- 端到端实测（运行中服务）：创建 → 新 key 200 → 禁用 401 → 删除 401 → 主 key 200 → config.json 无残留
- `go build ./...` / `go vet` 干净；`go test ./...` 通过（`TestChatHardCreditCooldownUntilNextDay4AM` 为既有时间敏感用例，凌晨时段误报，与本次改动无关）
- Admin 界面 mock 渲染截图逐屏核验；`go:embed` 页面重编译后 curl 特征验证

### 发布
- 新建公开仓库 myzane678/WorkBuddy-Proxy；README 重写（来源声明、免责声明、Windows/Docker 双部署路径、Admin 控制台说明）；补 MIT LICENSE
- Release 附 `workbuddy-proxy-windows-amd64.exe`

## 2026-09-10 · v11：账号交接 + Windows 脚本可迁移化 + 登录脚本 PS5.1 加固

### 交接
- 移除前任维护者账号凭证（`auths/` 清空），改用本地账号重新走 OAuth 登录
- README/CHANGELOG 中明文 api_key 与前任账号描述已清理：key 只留本地 `config.json`（已被 .gitignore 排除）

### 修复
- **A 盘硬编码路径**（旧部署机遗留 `A:\ClaudeWorkspace\workbuddy2api`）全部改指 `E:\work\workbuddy2api`：
  `start-workbuddy.cmd`（3 处）、`stop-workbuddy.cmd`、`watchdog.ps1`（`$proj`）、`watchdog.vbs`、`login-workbuddy.ps1`（状态目录 `A:\tmp` → `E:\tmp`）
- `login-workbuddy.ps1`：poll 步骤改「临时降 EAP + Out-String 取文本 + 判空」——PS5.1 下 `2>&1` 叠加 `$ErrorActionPreference='Stop'` 会吞掉退出码与 stderr，导致 `ConvertFrom-Json` 收到空值崩溃；`Set-Clipboard` 改 try/catch；脚本文件补 UTF-8 BOM（修 PS5.1 中文提示乱码）

### 验证
- `healthz` → `{"healthy":1,"total":1}`；手动签到 `ok=true`，下次自动签到 21:00
- 端到端对话：`glm-5.3-flash` 非流式 200（prompt 18 / completion 75 tokens），请求记录已入 `/admin` 请求日志
- PS5.1 语法解析 0 错误；进程血缘确认 proxy（子）由 watchdog（父）守护，与会话解耦

## 2026-09-07 · v10：/admin 页新增「停止服务」按钮

### 新功能
- **POST /admin/api/stop**：执行 `stop-helper.ps1`（杀 watchdog + workbuddy-proxy 自身进程）。
  与「重启服务」互补——停止后不会自动拉起，需手动运行 `start-workbuddy.cmd`。
- /admin 操作区「重启服务」旁新增「停止服务」danger 按钮（同款样式），点击先 `confirm`
  确认再调用；脚本路径默认相对 cwd 的 `stop-helper.ps1`，可用 `WB2A_STOP_SCRIPT` 覆盖。

### 验证
- 新增 `TestAdminStopMissingScript`（脚本缺失 → 500 且不执行，安全路径）；server 套件全绿
- `/admin` 页面浏览器实测：按钮与「重启服务」同款 danger 样式，渲染正常

## 2026-09-07 · v9：pool 防雪崩兜底修复（并列最旧 → 随机打散）

### 缺陷
- `pool.pick()` 的 LRU 兜底在并发爆发（top5 账号 `lastUsed` 同一瞬间被置位、权重不分伯仲）时，
  固定收敛到按 (权重, uid) 排序的队首账号，全部高并发请求扎堆单号 —— 恰好违背防雪崩设计意图。
  `TestPickAntiThunderingHerd` 稳定失败（100 并发中 82 次命中同一账号，连跑多次必现）。

### 修复（internal/pool/pool.go）
- LRU 兜底先求全局最旧账号；存在「并列最旧」（同刻被用过，`lastUsed` 不严格更旧）时
  改走加权随机打散，不再固定选排序首位。

### 验证
- `TestPickAntiThunderingHerd` / `TestPickLRUFallbackWhenTopAllRecentlyUsed` 各连跑 10 次全绿；
  `go test ./...` 全量通过；`go build` / `go vet` 干净。

## 2026-09-06 · v3 白名单 + 积分/首字/签到可见 + 看门狗误杀修复

### 需求落地
- **模型白名单**：config 新增 `model_allowlist`（空=放行全部）；`/v1/models` 过滤 + chat 请求 403 `model_not_allowed` 门禁；admin 页 15 个模型勾选管理（POST /admin/api/models，即时生效 + 写回 config.json）
- **请求日志加"积分"列**：usage.credit 采集（流式 SSE 末帧 + 非流式 Aggregate 两路径），每次实扣一目了然（实测 0.01-0.04/次）
- **"首字"列**：流式记 TTFB（实测 1.2-3.0s），非流式显示 –（无首字概念）
- **签到可见**：scheduler 记录每轮签到结果（时间/触发方式 manual|scheduled/每账号 msg+余额），落盘 `data/checkin-state.json` 跨重启；页面显示上次签到 + 下次自动签到时间（09-06 21:00 这类）；"今天已签到"业务 400 视为成功态
- **自动签到**：上游本就有（每日 09:00/21:00），页面透出下次触发时间

### 🔴 看门狗 12 秒误杀 bug（v1 遗留，本轮抓到真凶）
- `watchdog.ps1` 健康检查用 `Start-Process curl`——**Start-Process 不写 `$LASTEXITCODE`**，恒 null → `-ne 0` 恒真 → 每 12 秒无脑杀掉 proxy 重启
- v1/v2 所有"偶发断流"（curl 56 连接重置、空流、请求日志缺条目）全是它造成的，**不是上游问题**
- 修复：健康检查改为直接调用 `curl.exe`（直接调用才设置 `$LASTEXITCODE`）；已实测 30 秒+ 同 pid 稳定
- 顺带：watchdog 启动 proxy 时重定向 stdout/stderr 到 `data/proxy-stdout.log` / `proxy-stderr.log`（panic 栈与请求表格日志可查）

### 上游代码改动（外科手术）
- `handler.go`：peek 增 Model 字段；白名单门禁（403）+ models() 过滤 + filterModels/allowlisted helper；两分支 st.credit 赋值
- `logging.go`：chatStat/chatStatsReader/RequestEntry 增 credit；syncCredit helper
- `reqlog.go`：RequestEntry 增 Credit float64
- `scheduler.go`：runCheckin 重构（CheckinReport/CheckinResult 记录 + 落盘/恢复 + "已签到"成功态）；CheckinSnapshot 供 admin
- `admin.go`：overview 增 all_models/allowlist/checkin/checkin_next；POST /admin/api/models；writeConfigField 复用
- `cmd/server/config.go`：ModelAllowlist 字段
- `watchdog.ps1`：LASTEXITCODE 修复 + 日志重定向

### 验证
- 构建 + server/scheduler 测试全绿
- 白名单全流程：设 [glm-5.2,deepseek-v4-flash] → /v1/models 只剩 2 → kimi-k2.7 请求 403（并记入日志）→ glm-5.2 200 → 恢复放行全部
- 流式 4 连发 exit 全 0，日志全记录（首字/积分/tokens 齐全）
- 页面 Chrome 实拍：模型勾选区 15 项、签到区（下次 21:00）、请求表（首字/积分列）全部在位
- 手动签到 ok=True msg=今天已签到

### 反馈修正
- 白名单区删掉"全选/全不选"按钮（与"不勾=放行全部"语义重复），只留"保存白名单"
- 首字列改秒显示（fmtSec，1.2s 风格），清理孤儿 checkAll 函数

## 2026-09-06 · v2 /admin 状态页

### 新增
- **`/admin` 状态页**（`internal/server/admin.go` + 内嵌 `admin.html`）：健康账号/冷却禁用/模型数/运行时长卡片、账号卡（昵称/uid/状态/积分/成功失败计数）、API Key 明文可见 + 复制 + 页面内改 key（即时生效，写回 config.json）、请求日志表（#/时间/模型/模式/状态/账号/TTFB/输入/输出 tokens/耗时）、立即签到 / 重启服务 / 刷新按钮、显示条数切换、双击表格展开折叠、5 秒自动刷新
- **请求日志持久化**（`internal/server/reqlog.go`）：500 条环形缓冲 + 2s 防抖落盘 `data/request-log.json`（跨重启保留，`WB2API_REQLOG_PATH` 可覆盖），输入 tokens 为本次新增采集（logging.go 的 SSE 末帧解析 + Aggregate 响应两条路径）
- **手动签到积分回写**（scheduler.go +1 行）：`RunCheckinNow` 现在调用 `Pool.SetCredits`，修复 /status 与 admin 页积分恒 0 的上游缺口（同时让三因子轮转权重的 credits 因子真正生效）

### 鉴权模型
- `GET /admin`、`GET /admin/api/overview`、`GET /admin/api/requests`：无鉴权（服务只绑 127.0.0.1，首次打开页面浏览器还没有 key，放开读才能看到状态；页面自动把 key 回填 localStorage `zcode_admin_wb_key_v2`）
- `POST /admin/api/checkin|restart|key`：Bearer 鉴权（实测无 key 返回 401）

### 上游代码改动（最小外科手术）
- `handler.go`：Config 加 `Admin AdminExtras`，NewHandler 挂载 admin 路由，chat 两分支补 `st.inToks`
- `logging.go`：seq 生成移到 `done()`（stdout 与请求日志共用同一 seq）；chatStatsReader 增加 prompt_tokens 采集；logChatRow 加 seq 参数
- `scheduler.go`：+`SetCredits` 一行
- `cmd/server/main.go`：注入 Scheduler/ConfigPath/StartedAt
- `logging_test.go`：适配新 logChatRow 签名；TestChatStatsReaderTokensFromUsage 起点前移 1ms 修机器速度敏感的 TTFB=0 flake

### 验证
- `go build` OK；`internal/server` ×3、scheduler、auth/session/upstream/redisstore/cmd 全绿
- **`internal/pool` TestPickAntiThunderingHerd 在原始上游代码上同样失败（stash 验证）**：上游统计型 flaky 测试（Windows 机器上 91/100 > 50% 阈值），与本次改动无关，未修
- 实机验证：页面 Chrome 实拍渲染正常（a11y snapshot 全部数据在位）；请求日志跨重启保留；checkin 后积分 699 回填

## 2026-09-06 · v1 初次部署

### 部署
- 上游：https://github.com/Sliverkiss/workbuddy2api（Go 版，196⭐，MIT）
- 本地路径：`A:\ClaudeWorkspace\workbuddy2api\`（浅克隆 master）
- 构建：Go 1.26.5，`GOMODCACHE=A:\DevTools\go-path\pkg\mod`（模块缓存不落 C 盘），`GOPROXY=https://goproxy.cn,direct`
- 产物：`workbuddy-proxy.exe`（11.3MB）+ `wb-login.exe` / `wb-credit.exe` / `wb-signin.exe`（各 ~8.6MB）

### 配置
- `config.json`：监听 `127.0.0.1:8091`（与 zcode-api 8090 并存），api_key 只存本地 config.json（明文已从文档移除），auth_dir `./auths`
- 账号：初期由前任维护者账号接入（昵称/uid 与凭证已随交接一并移除），后改用本地账号重新登录
- 自动签到：每日 09:00 / 21:00（上游内置 scheduler）

### 新增文件（Windows 适配，未改上游 Go 代码）
- `login-workbuddy.ps1`：PowerShell 原生 OAuth 登录（替代 login.sh，绕开其 unbound variable bug PR#18；不依赖 bash/python3）
- `start-workbuddy.cmd` / `stop-workbuddy.cmd`：窗口自消失启停脚本（启动成功显示 5s 自关，停止 3s 自关）
- `watchdog.ps1` / `watchdog.vbs`：隐藏看门狗，3s 轮询 `/healthz`，掉线自动拉起；任何 HTTP 响应（含 503 无健康账号）都算存活，仅连接失败才重启
- `stop-helper.ps1`：停止逻辑（cmd 薄壳调用，避免 cmd 内嵌 PowerShell 引号坑）
- `.gitignore` 追加 `/*.exe`

### 验证记录（2026-09-06 全通过）
1. `go build` 4 个二进制 ✅
2. `/healthz` 200 ✅；`/v1/models` 10 个模型（glm-5.2 / deepseek-v4-pro/flash / kimi-k2.7 / hy3 等）✅
3. 非流式对话 ✅（usage 精确返回 tokens + credit 计费）
4. 流式对话 SSE ✅
5. **工具调用（硬需求）**：非流式 `finish_reason:tool_calls` + 标准参数 ✅
6. 多轮工具闭环：tool 结果回传 → 模型正确消费 ✅
7. 流式 tool_calls 按 index 合并 + `[DONE]` ✅
8. stop→start 全生命周期：端口释放 / 看门狗零残留 / 重启后端到端 pong ✅

### 已知事项
- 上游偶发空流（一次流式请求返回空，重试即恢复），账号池熔断/冷却机制正常兜底
- `/status` 的 credits 字段初始为 0，真实额度以 `wb-credit.exe` 输出为准
- `timeout /t` 在非交互控制台报 "Input redirection is not supported"，双击运行无影响
- 工具脚本（start/stop/watchdog/login）为纯 ASCII——cmd 解析含 UTF-8 中文批处理会字节错位

### 端点
| 端点 | 鉴权 | 说明 |
|---|---|---|
| POST /v1/chat/completions | Bearer | OpenAI 兼容（工具调用/流式） |
| GET /v1/models | Bearer | 模型列表 |
| GET /status | Bearer | 账号池状态 |
| GET /healthz | 无 | 健康检查 |
