# CHANGELOG

## 2026-09-19 · v1.3.0：概览页历史用量日历选择

### 新增
- **运行概览页支持回看历史任意一天的用量**（丞相需求：不仅看每天，之前的天也要能选来看）。
  - 交互：页头新增日期徽标（默认「今日」），点击弹出**日历弹层**——有数据的天打蓝点、
    超出 30 天保留期的置灰并提示、未来日期禁用、顶部「今日」一键弹回；Esc/点击外部关闭。
    日历为手写纯 CSS/JS（约 150 行），延续 admin 页零外部依赖传统（原生 date 控件样式不搭
    且做不了打点/置灰，不采用）。
  - 联动范围：Tokens / 请求（含失败数）/ 实扣积分 / 平均首字 / 模型分布环形图全部切到所选日，
    卡片标题动态化（「今日 Tokens」↔「09-18 Tokens」）；「剩余积分」为实时池状态不随日期变，
    「累计 Tokens」卡继续承载累计口径；趋势图高亮列跟随所选日。
- **「立即签到」按钮已签到态 + 实时反馈**（丞相需求：签到后按钮变淡表示已签到，实时性要好）。
  - 后端：`CheckinReport` 补 `date` 字段（此前只有时刻，无法判定是否今天），新增
    `CheckedInToday()`（今天且至少一账号成功；上游幂等返回的「今天已签到」也算成功；
    全失败不视为已签到、允许重试）；`/admin/api/overview` 透出 `checkin_done`。
  - `POST /admin/api/checkin` 由 fire-and-forget 改为**同步执行**：响应返回时签到已完成，
    前端"完成即知"，不再依赖 4 秒后轮询发现（实时性从秒级轮询提为即时）。
  - 前端按钮三态：正常（可点）→ 签到中（转圈）→ **今日已签到（淡化+禁用）**；页面加载与
    5s 轮询都按 `checkin_done` 同步（跨天自动恢复可点、定时签到后其他打开的页面同步淡化）；
    全失败时恢复可点以便重试。
  - 测试：`TestCheckedInToday`（今天/昨天/全失败/旧文件无日期等 6 例）+
    `TestRunCheckinMarksDateAndDone` 集成。
- **「停止服务」自动关闭 admin 窗口**（丞相需求：停止服务后控制台窗口跟着退出，不留死页面）。
  - 浏览器硬限制：Chrome 拦截页面脚本关闭自身窗口（`window.close` 对用户打开的窗口一律
    无效，`--app` 应用窗口同样被拦，实测推翻了"app 窗口可关"的假设）。
  - 最终方案（OS 层面，不赌浏览器政策）：`launch.vbs` / `start-workbuddy.cmd` 启动 admin
    窗口时带**独立 profile**（`data/admin-profile`，不与用户日常 Chrome 合并实例）+
    `--workbuddy-admin-window` 特征标记；`stop-helper.ps1` 杀完服务后按命令行特征结束该
    专用 Chrome 实例。前端 `doStop` 保留探测关窗尝试与「服务已停止」遮罩兜底。
  - 实测：带特征的 headless Chrome 实例杀前命中 1、杀后归零，不误伤日常 Chrome；
    `stop-helper.ps1` Parser 解析 0 错误。
  - 教训：三个启停脚本是**编码敏感文件**（wscript/cmd/PS5.1 均按 ANSI 读无 BOM 文件），
    新增注释一律 ASCII（stop-helper.ps1 文件头原有 "ASCII only" 约定被本次中文注释破坏，
    已恢复；launch.vbs 新增中文注释曾致 800A03EA 编译错误，cscript 编译验证通过后修复）。
- **后端** `/admin/api/stats` 响应新增 `available_dates`（有数据的日期列表）与
  `day_details`（30 天内每天完整明细含模型分布）。本地单用户接口一次带全量换切换零延迟；
  数据底子是 v1.1.0 起按「天 × 模型」落盘的 `data/stats.json`（天桶保留 30 天，之前是存了没暴露）。

### 修复
- 概览「平均首字」卡副标题恒显示「今日暂无流式」：后端 `today` 汇总漏吐 `ttfb_n` 字段，
  前端判断 `t.ttfb_n>0` 恒 false。补齐字段（`dayDetail` 同步带上），副标题随所选日期正确显示流式次数。
- 既有时间依赖测试 bug（与本次功能无关，恰在午夜跑全量时暴露）：`TestCooldownUntilTomorrow4AMPersists` /
  `TestChatHardCreditCooldownUntilNextDay4AM` 把「次日 04:00 距今」写死 ≤24h，而 now∈[00:00,04:00)
  时相距 24~28h，每天午夜到凌晨四点之间跑必失败。断言上限修正为 28h 并注明原因。
- `internal/scheduler` 测试污染：`persistState` 落盘为 cwd 相对路径，包内测试会把
  `data/checkin-state.json` 写进包目录（2026-09-11 起既存），新测试经 `New→loadState` 读到后误判。
  新测试开头显式清理该残留文件。

### 测试
- 新增 `TestAdminStatsHistoryDayDetails`：断言 `available_dates` 排序正确、`day_details` 含
  模型明细与首字均值（今天/昨天两天种子数据）。
- `go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿。

## 2026-09-18 · v1.2.1：请求体 8 MiB 静默截断修复 + Pool 生命周期补全

### 背景
- 另一工作区副本（DSH 会话）排查出根因并已实测修复，本仓库同步同款修复。
  实测数据均出自该会话（2026-09-18），非本仓库重新测量。

### 修复 1：请求体 8 MiB 静默截断（根因）
- 旧代码 `io.ReadAll(io.LimitReader(r.Body, 8<<20))` 对超限请求体**静默截断**（不报错）：
  JSON 变成半截 → `json.Unmarshal` 失败 → model 字段读不到 → 被白名单门禁误报成
  `403 model_not_allowed` / `400 model is required`，把「请求体过大」伪装成「账号或白名单问题」。
- 触发条件：agent 场景把截图以 base64 data URL 内联进请求体（实测约 3.7 MB/张，1080x2400 截图）。
  观察到的失败样本 13 张图 base64 后约 8.2 MiB，恰好越过 8 MiB 上限——纯文本会话永远碰不到
  该边界，这正是「换个会话就能用」的原因。
- 修复（`internal/server/handler.go`）：
  - 上限抬高为 `maxBodyBytes = 32<<20`（实测依据：上游接受 ≥96 MiB；本代理内存随 body 线性增长
    8 MiB→105 MB / 32 MiB→266 MB / 64 MiB→454 MB；32 MiB 覆盖「十几张高清截图」并留 4 倍余量）；
  - `io.LimitReader` → `http.MaxBytesReader`：超限返回 **413 `request_too_large`** + stderr 一行日志；
  - 413 分支补 `chatStat` 记账，超限请求与正常路径一样落 `/admin` 请求日志（否则排查时完全不可见）；
    body 已截断无法解析 model，以空 body 构造（model 列记 `-`）。

### 修复 2：`TestAutoFlush` 偶发 flake（实测 2/5 轮命中）+ Pool 生命周期
- 本仓库 `go test ./internal/pool/ -count=20` 循环复现出偶发 FAIL：`auto flush not persisted: Credits=0`。
- 排查证据（插桩实测）：失败瞬间 `state.json` 存在且内容完整（credits=77）、目录无 `.tmp`、
  `load()` 的 ReadFile 报 not exist 且 3ms 内重试仍失败、数 ms 后自愈、`-race` 无报告。
- 结论：Windows 上高频 create/rename 的 TempDir 存在**毫秒级「Stat 可见但 ReadFile not exist」
  瞬态窗口**，Stat 成功即断言会撞进窗口误报。产品侧 `saveLocked`（tmp+Rename 原子落盘）无问题。
- 修复：
  - `TestAutoFlush` 改为轮询「重载读出 credits=77」或超时，断言强度不变（不再依赖 Stat 与 ReadFile
    之间的时序假设）；修复后 8 轮 `-count=20/40`（160+ 次迭代）全绿；
  - `internal/pool` 新增 `Pool.Close()`（停止后台 flusher + 同步落盘，幂等）：此前 `New()` 启动的
    flusher goroutine 无任何回收手段，测试中泄漏且会在 `t.TempDir()` 清理后仍向目录写盘
    （另一副本实测 `TempDir RemoveAll cleanup` 竞争即此根源）；`cmd/server` 退出路径改为 `defer p.Close()`。

### 测试
- 新增 `TestChatBodyOverLimitReturns413`：超限必须 413 `request_too_large`，绝不退化成 model 相关报错，
  且必须出现在 `recentRequests()`（可观测性回归点）。
- 新增 `TestChatBodyAtLimitPasses`：恰好等于上限的请求体正常放行（严格大于才拒，不误伤边界）。
- `go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿；pool 包 `-count=20/40` 多轮全绿。

### 文档同步
- `config.example.json` 补 `model_allowlist` 键（`config.go` 已定义、admin 写回已使用、
  真实 `config.json` 已存在，样例此前遗漏），README 配置样例同步。
- README 测试命令段据实修正：`gofmt -l .` 在 CRLF 工作区（`core.autocrlf=true`）会标记全部
  `.go` 文件，属行尾差异非格式缺陷，删去「应为空」的错误声明。
- README「稳定性设计」补「请求体上限 32 MiB」条目，写明**勿改回 `io.LimitReader`** 的告警。

### 待办：图片降采样（暂缓，先记录）
- 现状：32 MiB 上限下大截图（1080x2400，2.8 MB/张）约 8 张，典型截图（412 KB）约 60 张；
  超限现在明确报 413，看到即知需少贴图。
- 实测依据：上游按固定 tile 预算计费，同一张截图 120 KB 与 2.8 MB 都是 ~960 token——
  卡住的是**字节**不是 token；sharp 重编码 JPEG q80 长边 1568 可缩 95% 且识别质量不变。
- 触发条件：再次出现「图片过多导致 413」时启用；首选落点为**客户端侧**压缩（对所有下游生效，
  属改官方行为需授权），代理侧不推荐（每请求解压重编码增延迟与内存，且需引入图片处理依赖）。

## 2026-09-18 · v1.2.0：剩余积分准实时扣减

### 新增

- **剩余积分准实时更新**：每次对话请求成功后，从池内余额本地扣减本次实扣积分（`usage.credit`），
  admin 页「剩余积分」卡随每笔请求即时下降，不再冻结到下一次签到（此前最坏延迟近 12 小时）。
  - 流式（SSE 末帧）与非流式（Aggregate）两条路径全覆盖，接入点在 `handler.go` 两个成功出口。
  - 零头累加器：池内 `credits` 为整数而单次实扣为小数（约 0.01~0.04），小数进运行态字段
    `creditFrac` 凑满 1 扣 1，漂移 ≤1 积分。
  - 对账机制：签到（定时 09:00/21:00 或手动）回写上游 `UserResource` 精确值，自动校正漂移。
  - 上游零额外请求（对比轮询/每请求回查方案，无风控风险）。

### 修复

- 扣减后余额为负时钳 0（与 `UserResource` 语义一致）；usage 缺失（credit<0）不扣减。

### 测试

- 新增 `TestDeductCreditLocal`（pool_test.go）：零头累加、凑满扣减、大额钳 0、未知 uid 忽略、
  签到回写对账、usage 缺失不扣，六个场景全覆盖；`go test ./...` 全量通过。

## 2026-09-13 · v1.1.0：Admin 管理台侧边栏重构 + 持久化用量统计 + 一键启动器

### Admin 管理台（Breaking-free UI 重构）

- **左侧边栏多页面布局**：单页长滚动重构为侧边栏 + 5 个子页面（运行概览/账号池/密钥管理/模型白名单/
  请求日志），hash 路由（`#/overview` 等）点击即跳、浏览器前进/后退可用；窄屏（≤960px）侧边栏
  自动收起为抽屉 + 汉堡按钮。签到并入运行概览页（卡片 + 右上角「立即签到」），独立栏目移除。
- **运行概览升级**：
  - 六张统计卡：今日 Tokens（输入/输出拆分）、累计 Tokens（含起始日）、今日请求（失败红字）、
    今日实扣积分、剩余积分、平均首字
  - 最近 7 天 Token 使用趋势柱状图（输入/输出双色堆叠，悬停明细，今日高亮）
  - 模型分布环形图 + 图例（请求数/tokens/占比），支持「今日 / 累计」切换
  - 图表全部纯 CSS/conic-gradient 手写，零外部依赖
- **请求日志筛选**：模型/账号/模式（流式/同步）/状态（成功/4xx/5xx）四维下拉筛选（纯前端即时过滤，
  选项从日志数据动态提取，5s 自动刷新不冲掉选中条件），配「重置」与「导出 CSV」（带 BOM，Excel 中文不乱码）。
- **模型白名单重排**：勾选项改自适应多列网格，新增「已选 n/m」计数与「全选 / 清空（放行全部）」快捷操作。
- **favicon**：项目 logo 以 base64 内嵌（1.7 KB，零额外请求）。

### 新增：持久化用量统计（`internal/server/stats.go`）

- 按「天 × 模型」聚合请求数/错误/输入输出 tokens/实扣积分/流式首字，挂在 `chatStat.done()`
  统一出口（流式与非流式都覆盖）；usage 缺失（-1）按 0 计入，`status>=400` 计为错误。
- 落盘 `data/stats.json`（`WB2API_STATS_PATH` 可覆盖），dirty + 2s 防抖异步写，与请求日志同款模式，
  跨重启保留；天桶只保留最近 30 天，`Total` 累计独立存储不受修剪影响。
- 新端点 `GET /admin/api/stats`（与既有 GET 类 API 一致不鉴权）：返回今日汇总（含首字均值）、
  累计汇总、最近 7 天趋势、模型分布（今日 + 累计，按 tokens 降序）。
- 统计自 v1.1.0 启用后开始累积，历史数据无法追溯。

### 新增：一键启动器（Windows）

- `launch.vbs`：无黑窗静默启动入口——curl 探测 `/healthz`，服务未运行则拉起看门狗并轮询等待
  （最多 15 秒），最后以 Chrome `--app` 模式打开 `/admin`（无地址栏独立窗口，类桌面应用）；
  无 Chrome 回退默认浏览器。桌面快捷方式图标为项目同款渐变 logo。
- `scripts/genico/main.go`：纯 Go 标准库手绘 admin 页同款 logo（渐变圆角方块 + 心电折线，
  SDF 描边 + 4x 超采样抗锯齿），生成 16/32/48/256 四档 `assets/icon.ico` 与 favicon PNG。
- `scripts/make-shortcut.ps1`：创建桌面快捷方式（自动适配 OneDrive 重定向桌面）。

### 测试与验证

- 新增 3 个统计回归用例（聚合正确性/落盘恢复/30 天修剪）；`go vet` 干净，`go test ./... -count=1` 全绿。
- 真机验证：冷启动场景（服务停止 → 双击快捷方式 → 自动拉起 → healthz 恢复 → 应用窗口弹出）通过。

### 发布

- Release 附 `workbuddy-proxy-windows-amd64.exe`（v1.1.0 编译产物）

## 2026-09-13 · v1.0.1：修复上游 11128 风控拦截（UA / 角色归一化 / 指纹脱敏三线同步上游）

### 背景

- 2026-09-12 起腾讯 CodeBuddy 上游风控升级，代理发出的 chat 请求被按「非官方渠道」拦截：
  HTTP 400 `code=11128 "Illegal API invocation from an unapproved channel"`，单号池轮空后客户端表现为
  503 `no_healthy_account`。拦截为**逐字精确匹配**（非语义审核、非限流），重试无效。
- 已知触发面（上游 Sliverkiss/workbuddy2api issue #25/#36/#39/#54）：旧版 UA 黑名单、
  `role:"developer"` 不在白名单、system prompt 固定模板句指纹、请求体含裸数字 11128 的反探测、
  工具调用 arguments 净化盲区。

### 修复（对照上游 master 同步）

- **headers.go**：出站 UA 对齐官方 WorkBuddy Desktop 三段式
  `WorkBuddy/5.5.4 WorkBuddy/5.5.4 CLI/2.137.1`（旧值 `CLI/2.63.2 CodeBuddy/2.63.2` 已被风控拉黑）。
- **payload.go**：新增 `normalizeRoles`——`developer` role 归一为 `system`（协议兼容层，
  不受 `sanitize` 开关影响；大小写/空白容错，仅动 developer 一个值）。
- **sanitize.go** 指纹脱敏全量同步：
  - Codex instructions 首段指纹改写（首句插一词 `tool` 破坏逐字匹配，语义不变）
  - Claude 身份句匹配串去结尾标点，覆盖桌面版「…for Claude, running within the Claude Agent SDK」变体
  - 反馈句 `give→provide` 一词改写（整句含 anthropics 仓库链接才触发拦截）
  - 裸数字 11128 反探测：请求体出现 `11128` 即整单拦截，改写为 `11-128`
  - 新增 `sanitizeToolCalls`：净化 `tool_calls[].function.arguments`（content 为 null 的工具调用轮
    旧版整条跳过，历史工具参数里的被拦字符串原样漏出）

### 测试与验证

- 新增 10 个回归用例：桌面版身份句、反馈句、11128 反探测、tool_calls 盲区、Codex 改写/预检/变体不动、
  Codex wire body 集成、`normalizeRoles` 表驱动（含 messages 缺失不 panic）；`go test ./... -count=1` 全绿
- 真机端到端：携带全套已知触发指纹（Codex 三句开头 + Claude 反馈句 + 裸 11128）的请求经代理打真实上游返回 200

### 发布

- Release 附 `workbuddy-proxy-windows-amd64.exe`（v1.0.1 编译产物）

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
