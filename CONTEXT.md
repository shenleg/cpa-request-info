# HTTP 执行输入记录领域上下文

本文固定当前正式精简版的领域用语与行为边界。规范见 [当前 spec](.scratch/official-http-input/spec.md)，取舍见 [ADR-0004](docs/adr/0004-official-http-execution.md)，实际平台、UI、容器及故障结果见[交付记录](docs/verification/official-http-input-2026-10-16.md)。

## 术语

| 术语 | 含义 | 不等同于 |
| --- | --- | --- |
| 客户端 API Key | 宿主认证模型调用的凭据；当前已验证身份范围仅内置 Key 环境 | 管理 Key、上游凭据、自然人 |
| 管理 Key | CPA 管理操作的认证凭据 | 客户端 API Key、匿名对象 ID |
| 已认证调用者 | 宿主已认证 principal，经官方 `caller_scope` 传递其身份范围 | 任意请求头、自报名称、上游账号 |
| 审计对象 | 插件在私有身份域中为调用者派生的稳定匿名标识 | Key 片段、配置下标、名称、旧 raw-Key 身份 |
| 已观察对象 | 实际到达允许的 HTTP Before 回调、进入有界发现目录的匿名对象 | 配置中所有 Key、当前有效 Key 清单 |
| 已选择对象 | 管理员命名并持久保存其监控配置的对象 | 第一次被发现即自动监控 |
| HTTP 模型执行 | 官方 Before 观察到的一次外部 HTTP 执行，使用官方执行 ID | 任意入站 HTTP 请求、完整会话、插件内部嵌套执行 |
| 末尾允许输入 | 回调正文末尾明确用户消息中的白名单文本与固定附件占位符 | 历史、system/developer、工具结果、回复、附件 payload |
| HTTP 执行记录 | 单次符合条件的执行输入副本及白名单元数据 | socket 接收日志、原始请求、完整会话或回复日志 |
| 观察时刻 | Before 回调取得副本的服务端时刻，字段名仍为 `received_at` | socket 接收时刻、读体前时刻、客户端自报时间 |
| 执行输入模型 | Before 正文里的模型名称，可能已被 handler 改写 | 保证原样的客户端模型名、上游最终模型 |
| 请求思考设置 | Before 正文声明的等级、模式和预算，各自保存 | 模型后缀或 CPA 配置处理后的最终生效强度、实际思考用量 |
| 请求用途 | 按已发送请求头与专用路径识别的调用类别；保存 main、turn 或 unknown | 人工原文认证、客户端提交事件 |
| 预期跳过 | 未监控、主动暂停、明确辅助/子任务、工具续跑、空输入、内部嵌套和 WebSocket 等按规则不采集 | 采集故障、已保存 |
| 采集缺口 | 插件实际观察后可以报告的提取、队列或存储等问题 | 所有入站遗漏总量、回调前失败计数 |

## 官方回调决定可见范围

固定官方 CPA `v8.0.27` / `bf9341629ef2300ac9d846a2e80fe2756f63a671`，C ABI `1` / RPC schema `6`。正式插件只声明官方 `RequestInterceptor` 与 `Management`，Before 观察、不改请求，After 返回空修改；不保留补丁/官方双运行模式。

当前只处理 Chat Completions、Responses HTTP、Anthropic Messages 三协议的 HTTP/SSE。官方元数据 `source=plugin_host_model_callback` 表示的内部嵌套明确排除，Upgrade WebSocket 明确跳过。状态为 `capture_mode=http_execution`、`responses_websocket=false`，不能把无缺口计数解释为所有客户端输入已保存。

未知模型的路由失败、读体失败等若未进入 Before，插件既看不到正文，也不能计数。Before 所见模型可能已经过 DD 名称等改写，时间也晚于最初接收；不再承诺原始模型、读体前配置快照或 WebSocket 原帧。一次官方执行按自己的 ID 记录，不按 TraceID、正文或会话标识推测合并；相同文本再次执行仍独立。

## 身份、发现与持久状态

插件只瞬时使用已认证 principal 的 `caller_scope`，以私有种子和域分隔 HMAC 派生对象 ID，不保存原 scope、原 Key 或请求头。内置 Key 环境已有身份验证依据，其他认证来源、未来版本和其他 fork 不由此获得兼容保证。

默认监控名单为空。目标 Key 先发一次已成功路由到 Before 的 HTTP 请求，首次发现只保留匿名 ID，不保存正文；管理员选择并命名后才采集后续输入。发现目录最多1024个内存 ID；未选择对象重启丢失，已选择对象恢复。页面不列 Key 片段，不要求粘贴 Key；发现不证明该 Key 现在仍有效。名称不是身份，移除监控不是删除历史，主动暂停不是存储故障。

新 `caller-scope-v1` 与旧 raw-Key 身份不等价，首次安装使用新的专用 state-dir。数据目录可以位于插件目录内：推荐动态库在扫描目录根层，数据在 `/CLIProxyAPI/plugins/request-input-audit-data`，通过整个插件目录的一条挂载持久化；专用不意味着必须使用独立挂载，数据子目录不能混放动态库或 HTML。取得 `flock` 后只为除锁文件外为空的目录创建 `caller-scope-v1.seed`，内容为 magic + 32随机字节，文件和目录均同步保存。目录 `0700`、文件 `0600`；seed 与整个目录一起持久保存、保护和备份，不能多实例共享。旧 `identity.seed`、已有数据却缺新 seed、损坏或权限异常均明确停采拒绝，保留原文件，不自动迁移、删除或轮换。

## 正文、查询与存储边界

只判断末尾明确用户输入，不回找或拼接历史；Responses 字符串 input 视为本次文本，结构化列表只看末尾明确 user。工具调用/结果整体排除，不递归提取嵌套内容。附件仅保留固定占位符，不保存 URL、文件名、引用或 Base64 载荷；system/developer、历史和模型回复不保存。未知块能安全提取部分时标记 `extraction_incomplete`，不能安全提取时报告固定原因，不退回保存完整请求。`truncated` 与提取不完整独立，正常空输入不是缺口。普通文本秘密不自动脱敏，应用明文保存。

先按请求头与专用路径识别用途：Responses compact、明确子智能体/workflow、辅助、压缩、预热和记忆调用正常跳过正文，但仍返回匿名身份以保持对象发现。首版不读取 body.client_metadata；无标记、未知、损坏或互斥用途仍按原规则提取，保存 purpose=unknown。Claude main 保存为 main，Codex turn 在没有子任务来源时保存为 turn；两者都不是人工原文证明。只持久有限用途值，不保存用途头、子代理 ID；旧记录缺字段保持未记录用途，不追溯改写。旧二进制严格拒绝新字段，不支持同一升级后 state 原地降级；详见[用途规范](.scratch/request-purpose-filter/spec.md)。

Anthropic Messages 仅对用途未知请求，在截断前按已确认模板跳过建议、标题、动作、权限转录、返回摘要及起始独立 `<conversation>` 行后的非空会话包装；明确 main/turn 不被整请求模板反判。保留请求均清理独立技能、已确认 `/run` 重新调用说明、`Web page content:` 分隔包装、成对提醒和 IDE 上下文，保留其他独立正文、附件占位、代码围栏和原始空白。Chat Completions 仅对 unknown 末尾纯文本整消息精确等于 `follow system prompt` 正常跳过，不 trim、不忽略大小写、不匹配子串；其他 Chat 文本与 Responses 不套用 Anthropic 客户端规则。跨连续 user 查看转录仅用于未知请求判定，绝不拼接历史正文。完整同形的手工粘贴也会被过滤；规则只影响新记录，不改写历史或模型请求。完整规则表见 [README](README.md#识别与过滤规则)，契约见[过滤规范](.scratch/client-input-filter/spec.md)。

新记录的 `request_reasoning` 仅保存协议白名单中的 `effort`、`type`、`budget_tokens`。Chat、Responses 和 Messages 分别读取各自思考声明；模式、等级、预算不互相推导，显式0不等于缺失。新记录空对象表示“未指定”，旧记录无字段表示“未记录”，不改写历史。不保存完整配置对象，不从模型后缀或宿主配置推断最终值；旧二进制严格拒绝新字段，不能用写入后的原 state 直接降级。

管理沿用 CPA 端口及管理 Key，菜单可能保留“输入审计”，页面明确是 HTTP 执行记录。公开静态 shell 没有数据，所有动态接口受管理鉴权保护。列表与搜索统一为 POST `/v0/management/plugins/request-input-audit/records/search`，筛选、`q`、`offset`、`limit` 在 JSON 顶层；单条详情保留 GET `/detail?id=...`。关键词不能进 URL，也不能由代理或正文日志另存。筛选按匿名对象、观察时刻、执行输入模型和已保存正文取交集；详情不是完整会话，截断尾部和附件不参与搜索。模型下拉通过受管理鉴权保护的 GET `/models` 枚举全部已同步未到期历史中的非空名称，不受当前筛选或分页影响，仍按原字符串精确匹配。详情复制只使用已保存 `text`，不加元数据或清理空白；浏览器复制失败明确提示，不改变历史。

入队不是保存，只有已同步记录可查询。默认上限64KiB、保留7天、总预算1GiB；管理范围分别为1–65536字节、1–30整天、4MiB–1GiB。上限只影响后续副本；保留期从 Before 时刻计算，到期即不可查，后台回收不受暂停、移除或停机影响。缩短须确认，调长不延长旧历史。预算统计整个专用目录；满额停采，不提前覆盖未到期历史。容量原因可在满足空间条件后恢复，真实 I/O 或完整性故障不能靠提高预算消除。

缺口历史、当前活动故障、已阅和已保存数量分别解释；不同原因次数不能相加为独立丢失数，观察时间不是损失起止，运行计数重启归零不代表历史完整。异常退出不重放内存副本；常规错误不改变模型调用不等于 native 崩溃隔离。插件同进程，进程级崩溃风险未隔离；到期和空间回收不等于安全擦除，外部快照和备份由管理员保护。

## 历史资料

[旧 spec](.scratch/request-input-audit/spec.md)、[ADR-0001](docs/adr/0001-minimal-audit-loop.md)、[ADR-0002](docs/adr/0002-admission-snapshot.md)、[ADR-0003](docs/adr/0003-websocket-submissions.md)及旧 macOS/Linux/票13验收记录保留原事实。其中私有宿主观察契约、入站快照和 WebSocket 保证已被 ADR-0004 替代，不能作为当前正式版能力或本轮通过证据。官方接口探针用于说明决策依据；正式三协议、浏览器与容器的实际结果须另行验收记录。
