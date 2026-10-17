# CPA HTTP 执行输入记录

本仓库提供安装到**官方 CLIProxyAPI `v8.0.27`** 的原生插件，无需修改、重编或替换用户正在运行的 CPA。正式精简版 `0.2.0` 已通过 macOS arm64、Linux amd64 原生系统与浏览器验收，以及固定官方 Docker 镜像内的非 root 加载、采集和重启验证；尚未部署生产。实际范围和证据见[本轮交付记录](docs/verification/official-http-input-2026-10-16.md)。

基础规范为 [HTTP 执行输入 spec](.scratch/official-http-input/spec.md) 和 [ADR-0004](docs/adr/0004-official-http-execution.md)。请求用途过滤增量见[规范](.scratch/request-purpose-filter/spec.md)与[本轮双平台验收](docs/verification/request-purpose-filter-2026-10-17.md)，不以旧版本报告替代。安装步骤见[本地交付说明](docs/local-delivery.md)。

思考设置、模型下拉和详情复制的增量规范见[记录增强 spec](.scratch/record-metadata-and-actions/spec.md)，本轮实际验证结果见[验收记录](docs/verification/record-metadata-and-actions-2026-10-17.md)。

## 支持条件与记录含义

- 固定官方 commit `bf9341629ef2300ac9d846a2e80fe2756f63a671`，C ABI `1` / RPC schema `6`。正式能力仅为官方 `RequestInterceptor` 和 `Management`，不依赖私有宿主补丁，不承诺未来版本或其他 fork 兼容。
- 在官方 `Before` 回调中观察一次外部 HTTP 模型执行；`After` 返回空修改，不改请求或回复、不等待模型结果。支持 Chat Completions、Responses HTTP、Anthropic Messages 三协议及其 SSE 请求。
- 官方元数据 `source=plugin_host_model_callback` 的插件内部嵌套执行明确跳过；WebSocket Upgrade 请求明确跳过，Responses WebSocket 不采集。
- 记录身份使用官方执行 ID，不按 `TraceID`、正文或客户端会话字段合并。同文重交不去重，内部重试不通过正文猜测次数。
- `received_at` 是 **Before 所见时刻**，不是 socket 接收时刻；`model` 是回调正文中的模型，宿主可能已做 DD 名称等改写，不保证是客户端原始模型名。
- 未知模型导致的路由失败、读体失败等发生在回调之前时，插件不可见。状态只报告实际收到回调后的采集情况，不能统计未收到回调的遗漏，更不能用零故障证明全部请求已记录。

## 默认不监控：先发现，再选择

`caller_scope` 来自宿主已认证 principal，不由插件从任意请求头推断。当前认证范围仅限已验收的内置客户端 Key 环境；其他认证来源不在已验证范围。插件用私有随机种子和域分隔 HMAC 派生匿名对象 ID，不保存原 scope、原 Key、请求头，也不显示 Key 片段。

1. 让目标 Key 通过现有客户端先发一次**已成功路由、到达 Before 的 HTTP 模型请求**。首次发现不保存正文，不会在启用后补录。
2. 在 CPA 管理页登录，进入可能为兼容保留名称的“输入审计”菜单；页面明确展示的是 **HTTP 执行记录**。
3. 从已观察匿名 ID 中选择对象、命名并启用，之后才采集后续符合条件的执行。不要向页面粘贴 Key，也不要把对象 ID 当作登录凭据。

观察目录最多保留 1024 个内存匿名 ID，**不是配置中所有 Key 的列表**，也不能证明 Key 现在仍有效。未选择对象重启后丢失、需重新发现；已选择对象及名称、启停配置持久恢复。改名不改变身份，移除监控不删除历史；主动暂停与存储故障分别显示。

### 新身份必须使用新的 state-dir

新 `caller-scope-v1` 身份与旧 raw-Key 派生身份不等价，不要复用旧补丁版审计目录。推荐将动态库放在 `plugins-dir/request-input-audit.so`，专用数据放在 `plugins-dir/request-input-audit-data/`；现有插件目录挂载即可覆盖两者，`state-dir` 配置为 `/CLIProxyAPI/plugins/request-input-audit-data`。首次安装的数据子目录应为空，不能混放动态库或 HTML；已有精简版数据迁移步骤见[交付说明](docs/local-delivery.md#已有精简版数据改放到子目录)。

- `caller-scope-v1.seed` 由格式 magic 和 32 个随机字节组成；取得 `flock` 后，仅在空目录中初始化（除本进程锁文件外无其他内容），并同步保存文件和目录。
- 专用目录为 `0700`、seed 和其他状态文件为 `0600`。seed 必须和**整个 state-dir** 一起持久保存、保护和备份；单独复制正文不够。多个实例不能共享此目录。
- 发现旧 `identity.seed`、已有数据却缺新 seed、seed 损坏或权限不合规时明确停采拒绝。不自动迁移、删除、覆盖或轮换种子；故障遗留物保留。

## 保存什么，不保存什么

只从回调正文选择**末尾明确用户输入**中的允许文本和固定附件占位符，不回找或拼接历史。Chat Completions / Messages 只判断最后一条消息；Responses 字符串 `input` 作为本次文本，列表只判断末尾明确用户消息。连续 user 不跨消息拼接。

- 不保存历史、system/developer 指令、工具调用或工具结果、模型回复、完整请求、认证材料和附件 payload。工具结果内部不递归提取；附件仅留 `[图片]`、`[音频]`、`[视频]`、`[文件]` 等协议白名单占位符，不保存 URL、文件名、引用或 Base64 载荷。
- 工具续跑、正常空输入、非用户末尾正常跳过。未知或畸形内容不序列化为整请求：能安全提取的部分标记 `extraction_incomplete`，不能安全提取则仅报告固定原因。
- 优先使用已发送的用途头和专用 compact 路径识别辅助请求；未知用途继续提取，不要求客户改配置。随后按协议选择末尾输入、识别已知自动模板、清理独立附加块，具体条件见下方[识别与过滤规则](#识别与过滤规则)。
- 只截断审计副本，不改模型输入。`truncated` 与 `extraction_incomplete` 独立展示，可以同时为 true。
- **普通文本里的秘密、Key、URL、Base64 不自动脱敏；应用层明文保存。** 纯文本展示不代表正文不敏感。

记录字段限于 `id`、`object_id`、`received_at`、`protocol`、`model`、`purpose`、`request_reasoning`、`text`、`truncated` 和可选的 `extraction_incomplete`。`purpose=main/turn/unknown` 分别显示“主会话调用 / 对话轮次 / 用途未知”；旧记录缺该字段显示“未记录用途”。主会话与对话轮次均不是人工原文认证；不保存原用途头或子代理 ID。名称保存在对象配置中，不逐条复制。

`request_reasoning` 只保存 Before 可见的请求声明：Chat 的 `reasoning_effort`、Responses 的 `reasoning.effort`、Messages 的 `output_config.effort` 与 `thinking.type` / `thinking.budget_tokens`，对应 `effort`、`type`、`budget_tokens`。等级、模式和预算独立展示，不互相换算；预算为0与未指定不同。新记录的空对象显示“未指定”，旧记录缺字段显示“未记录”。**这些是请求值，不保证是 CPA 后缀、协议转换或配置覆盖后的最终上游强度**。不保存完整思考配置对象，不从模型名推断。

**新增字段的降级限制：** 新插件可读取原有记录，但旧插件的严格解码器会拒绝其不认识的 `purpose` 或 `request_reasoning`。不能把写入新记录后的 state-dir 直接交给旧插件降级运行；本项目不自动迁移、删除或改写数据。

### 识别与过滤规则

以下规则只决定**是否保存审计副本、保存哪些内容**，不拒绝或改写实际模型请求。处理顺序为执行来源与采集资格 → 请求用途 → 末尾消息/协议内容 → 模板排除与独立块清理 → UTF-8 长度截断 → 入队保存。正常跳过不增加采集缺口。

#### 执行来源与采集资格

| 识别条件 | 处理 |
| --- | --- |
| 官方回调 `source=plugin_host_model_callback` | 排除 CPA 插件通过宿主发起的内部嵌套执行；它不表示所有客户端自动调用 |
| WebSocket Upgrade、未支持的路径或协议格式 | 不采集；当前仅支持 Chat Completions、Responses HTTP、Anthropic Messages 的 HTTP/SSE 执行 |
| 首次发现、未监控或主动暂停 | 不保存正文；符合条件的外部 HTTP 执行仍可用于发现匿名对象 |
| Chat/Messages 末条不是明确 `user`，或 Responses 末项不是明确用户消息 | 正常跳过，不回找前面的用户原话；Responses 字符串 `input` 直接视为本次文本 |
| 工具调用、工具结果、模型回复、system/developer、正常空输入 | 不保存，不递归提取其嵌套正文；用户附件只保留固定协议占位符 |

#### 请求用途：只读取实际收到的请求头与路径

| 标记 | 值/条件 | 处理 |
| --- | --- | --- |
| `request_path` | `/v1/responses/compact`、`/backend-api/codex/responses/compact` | 跳过压缩请求 |
| `x-claude-code-agent-id`、`x-openai-subagent` | 任一有效非空值 | 跳过子任务；优先于普通对话用途，即使同时有 Codex `turn` 也跳过 |
| `x-claude-code-request-class` | `subagent`、`workflow`、`compaction`、`auxiliary` | 跳过辅助执行 |
| `x-claude-code-request-class` | `main` | 保存用途为 `main`，继续正文提取与独立块清理 |
| `x-codex-turn-metadata` JSON 中的 `request_kind` | `prewarm`、`compaction`、`memory` | 跳过预热、压缩、记忆调用 |
| `x-codex-turn-metadata` JSON 中的 `request_kind` | `turn`，且没有有效子任务标记 | 保存用途为 `turn`，继续正文提取 |
| 用途标记缺失、未知、损坏或互相矛盾 | 无法一致解释用途 | **继续采集，保存为 `unknown`（用途未知）**，再应用该协议的正文规则 |

头名不区分大小写；多个同名值仅在全部一致且非空时有效。明确 compact 路径或有效子任务标记优先。Claude `main` 与 Codex `turn` 同时存在时保存为 `main`；两端均表示跳过时跳过，对话与辅助用途矛盾时为 `unknown`。不读取 `body.client_metadata`，不根据 User-Agent、会话 ID、prompt ID 或正文语言猜测人工来源。完整用途契约见[用途规范](.scratch/request-purpose-filter/spec.md)。

#### 整条辅助请求排除

下表只对**用途为 `unknown`** 的末尾纯文本用户消息匹配，不读取附件/工具内部内容。Anthropic 明确 `main`/`turn` 继续保留整请求模板，但仍执行下一节的独立块清理。

| 协议 | 自动内容 | 识别条件与处理 |
| --- | --- | --- |
| Anthropic Messages | 输入建议 | 起始为固定 `[SUGGESTION MODE: …]`，并同时符合已确认的首段、任务、格式与结束指令；整条跳过，不只凭首行判断 |
| Anthropic Messages | 动作描述 | 起始为完整 `Describe your most recent action in 3-5 words…` 固定指令，且同时包含 `Good:` 与 `Bad (` 示例；整条跳过 |
| Anthropic Messages | 会话标题 | `<session>\n…\n</session>` 后紧接已确认的固定标题指令或语言变体；整条跳过 |
| Anthropic Messages | 权限判断转录 | `<transcript>` 内为合法连续 JSON 转录且包含 user/assistant 条目；或符合跨连续 user 的已确认起止与分类指令结构；整条跳过，不提取转录中的原话 |
| Anthropic Messages | 返回会话摘要 | 消息起始匹配固定前缀 `The user stepped away and is coming back. Recap in under 40 words. 1-2 plain sentences, no markdown.`；整条跳过 |
| Anthropic Messages | 会话内容包装 | 整条纯文本以独立 `<conversation>` 行开头，其后有非空内容；整条跳过，不绑定内部标题，也不要求未确认的闭合标签 |
| Chat Completions | 通用辅助指令 | 解码后的整条纯文本**精确等于** `follow system prompt`；整条跳过。不去除首尾空白、不忽略大小写、不匹配子串；混合附件/工具时不按此规则排除 |

字符串和纯 text 块可用于整消息判定，连续 text 块直接组合，不额外插入空白；不跨历史消息组合正文。Responses 不应用上述客户端正文模板，也不应用下一节 Anthropic 专用块清理。

#### Anthropic 独立附加块清理

保留的 Anthropic 请求（`main`、`turn`、`unknown`）均执行下列清理，再保存剩余正文及附件占位符。**独立块指单独的协议 text 块，字符串 content 则整体视为一个块**；不从同一自动块内猜测哪一段可能是人工文字。

| 内容 | 匹配条件 | 删除范围 |
| --- | --- | --- |
| 技能加载说明 | 块首 `Base directory for this skill: ` 后有非空路径及空行 | 删除该独立块 |
| `/run` 技能重新调用 | 块的第一行精确为 `(Re-invocation of /run — the skill instructions were previously loaded; the arguments or dynamic output below are new.)` | 删除该独立块及块内动态输出；不泛化到任意技能名或中断提示 |
| 网页内容包装 | 块首为 `Web page content:`，后接可选空白、`---`、至少一个空白及非空网页内容 | 删除该独立块；网页正文不必有 Markdown 标题 |
| 客户端提醒 | 行边界或块边界上的完整成对 `<system-reminder>`，可有16–32位小写十六进制 `id` | 删除成对范围；嵌套必须完整，结束标签必须匹配同一标识 |
| IDE 上下文 | 行边界或块边界上的完整成对 `<ide_opened_file>`、`<ide_selection>` | 删除成对范围 |

其他独立正文与原始空白保留；工具不保存，附件按协议保留固定占位符。代码围栏中的上述附加块保留，包括跨 text/工具块的围栏。普通讨论、正文中引用标记、缺少配对的提醒/IDE 标签不会只因包含关键词而删除；`<conversation>` 是单独的整请求起始规则，不适用提醒标签的配对要求。

**适用边界：**这些是有限的已知格式识别，不是人工来源证明。完整同形的手工粘贴也会被过滤；未识别的自动内容仍可能被记录。用途 `main`/`turn` 也不代表每段内容都由人工输入。所有分类与清理均早于长度截断；未知内容继续按原规则报告 `extraction_incomplete`，与 `truncated` 独立。规则只影响安装新版本后的新记录，**不会自动删除或重新过滤已有历史**。实现规范见[客户端过滤 spec](.scratch/client-input-filter/spec.md)。

## 存储与管理

| 设置 | 默认值 | 管理页/API 可调范围 |
| --- | --- | --- |
| 单条文本与占位符合计上限 | 64 KiB（65536 字节） | 1–65536 字节，UTF-8 安全前缀 |
| 保留期 | 7 个 24 小时 | 1–30 整天 |
| 整个专用目录预算 | 1 GiB（1073741824 字节） | 4 MiB–1 GiB |

上限只影响后续回调取得的副本。保留期从 Before 观察时刻计算，到期退出列表、搜索和详情；启动及周期维护回收空间，暂停、移除、停机不冻结期限。缩短保留期须确认；调长不延长旧记录或使过期记录重新出现。预算包括 seed、配置、诊断和辅助文件，不仅是正文；满额停止新采集，不提前覆盖未到期历史。容量恢复不等于真实 I/O 或完整性故障已修复。

使用 64 条 / 8 MiB 有界副本队列及分段 JSONL，只有写入并同步成功的记录可查；入队不等于保存。缺口历史、当前活动故障与已阅状态分别显示，观察时刻不冒充损失起止，不将不同原因的次数相加当成独立丢失请求数。不恢复异常退出前的内存副本，不承诺 exactly-once、零丢失、零延迟或任意流量完整保存七天。

### 管理 API

插件不新开端口，沿用 CPA 管理鉴权。全部动态接口由**管理 Key** 保护；匿名、普通客户端 Key 和匿名对象 ID 都不能读取。公开静态 shell `/v0/resource/plugins/request-input-audit/index.html` 不含对象、记录或凭据。正文和名称只按纯文本展示，不执行 HTML/Markdown 或自动加载外部资源。

下表路径统一加前缀 `/v0/management/plugins/request-input-audit`。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/objects` | 有界已观察对象与持久对象，不是全量 Key 配置目录 |
| GET | `/models` | 全部已同步且未到期历史的非空执行输入模型，去重排序，不受筛选或分页影响 |
| POST | `/monitor`、`/rename` | `{"object_id":"64hex","name":"名称"}`；选择启用或仅改名，名称最多256 UTF-8字节 |
| POST | `/remove` | `{"object_id":"64hex"}`；停止后续采集，保留历史 |
| POST | `/pause` | `{"paused":true}` 或 `false`；主动暂停/恢复 |
| GET / POST | `/settings` | 读取/修改 `max_text_bytes`、`retention_days`、`budget_bytes`；缩短另需 `confirm_retention_shortening:true` |
| POST | `/records/search` | **列表和搜索均使用 JSON 正文**，包含筛选条件、`q`、`offset`、`limit` |
| GET | `/detail?id=<recordID>` | 单条已保存正文和白名单元数据 |
| GET | `/status` | `capture_mode=http_execution`，三种 `supported_protocols`，`responses_websocket=false`，实际采集诊断 |
| POST | `/gaps/acknowledge` | 确认页面实际展示的 `generation` / `revision`；不解除故障或删除历史，并发变化返回409 |

搜索 JSON 使用顶层字段，例如 `{"object_id":"64hex","model":"执行入口模型名","q":"正文关键词","offset":0,"limit":20}`，可另加 `from` / `to`（RFC3339Nano，包含端点）。筛选取交集后计数、分页；`offset` 为0–10000000，`limit` 为1–100、默认20。`model` 精确匹配 Before 正文中的名称；`q` 只搜索已保存全文的字面子串，仅 ASCII 英文忽略大小写，不解释正则、不 trim、不搜索截断尾部或附件。模型最多1024 UTF-8字节，关键词最多65536字节。

管理页的执行输入模型下拉选项来自 `/models`，不是当前页或宿主模型配置；选择后仍按原名称精确匹配。详情的“复制纯文本”仅复制已保存正文，保留原换行和空白，不包含元数据或截断尾部。复制仅在详情成功加载后可用；普通 HTTP 页面在 Clipboard API 缺失时使用选区复制，浏览器拒绝或复制失败会明确提示手动复制，不改变已保存内容。

**不要使用旧 GET `/records?...&q=...`，不要把关键词放进 URL。** POST 搜索避免关键词出现在官方 URL 访问日志中；仍须确保部署中的代理、请求正文日志或调试工具不另存搜索词，不能宣称整个宿主日志已脱敏。

## 构建与验收入口

需要 Go ≥ 1.26、CGO、目标平台 C 编译器；完整开发构建另需 Git、Python 3、tar。浏览器验收使用固定 Node/Playwright 依赖。

```bash
make build-plugin      # 只构建正式插件，不获取或重编宿主
make build             # 精确 Git 对象全新导出官方宿主并构建插件，不应用 patch
make deps              # 明确准备 Go/Node 依赖，不执行 npm 生命周期脚本
make browser-install   # 下载固定 Playwright 对应 Chromium
make check             # 格式、vet、测试编译，不启动宿主
make check-staged      # 手动检查真实暂存区的隔离快照
make verify            # 开发验收入口，不等于已经通过
```

插件产物为 `.build/plugins/request-input-audit.dylib`（macOS）或 `.build/plugins/request-input-audit.so`（Linux）。`.build/bin/cliproxyapi` 仅供开发验收，**安装插件不需要替换用户 CPA**。Linux 动态库须与宿主架构及 libc 兼容，不能把 macOS 动态库拷到 Linux；交叉编译成功不代表目标平台已加载运行。

插件前端内嵌于动态库，不需要单独部署 HTML；现有 CPA 主管理页面可用时直接沿用。本轮主管理前端以 `v1.26.2` 验收，两端各 8 个真实浏览器场景各两轮通过；如需固定该版本，可通过 `MANAGEMENT_STATIC_PATH` 指定其静态目录并禁止自动更新。镜像 `eceasy/cli-proxy-api:v8.0.27` 已在 Linux amd64 验证挂载正式 `.so`、非 root 采集与重启；当前推荐的单目录挂载结构见[交付说明](docs/local-delivery.md)，不自建 CPA 镜像。

自主双端验收、固定基点工作区审查、有限证据和独立提交授权规则见[验证与交付约定](docs/agents/verification.md)。测试只使用隔离合成配置与可控上游，不复制合成 Key 到生产；本轮实际结果须由验收记录给出，旧报告不代替本轮运行。

## 风险与历史

native 插件与宿主同进程，未隔离非法内存访问、进程退出或 native 崩溃。常规解析、队列、存储错误不应改变模型调用，不代表所有故障都能隔离。主机高权限、卷、快照和备份读取者仍可读取明文；逻辑到期和空间回收不等于安全擦除，应用不能控制外部副本保留。目录祖先不能是符号链接，macOS 使用真实路径。官方宿主仍可能有既有后台网络请求，`--local-model` 不等于完全离线。

[旧完整入站审计 spec](.scratch/request-input-audit/spec.md)、[ADR-0001](docs/adr/0001-minimal-audit-loop.md)、[ADR-0002](docs/adr/0002-admission-snapshot.md)、[ADR-0003](docs/adr/0003-websocket-submissions.md) 与[票13交付报告](docs/verification/delivery-platform-validation-2026-10-15.md)保留其历史事实。其中补丁宿主、读体前快照、原始模型和 WebSocket 保证**不是当前正式版承诺**。[官方接口探针记录](docs/verification/official-plugin-probe-2026-10-16.md)是决策依据，也不是正式精简版本轮交付通过证明。
