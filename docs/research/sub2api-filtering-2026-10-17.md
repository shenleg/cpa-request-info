# sub2api：关键词拦截与客户端输入来源研究

本文只读核对官方仓库、测试、发行记录及 Claude 官方协议文档，回答关键词功能能否区分人工输入和客户端辅助内容。研究日期：2026-10-17；不修改实现或配置，不运行下载代码、不部署、不访问客户数据、不提交或推送。

## 结论

**sub2api 官方主仓库确实有入站关键词拦截，能阻止整个模型请求；但它不是“人工输入识别器”，也不能恢复客户端加工前的原文。** 对本问题所涉 Messages/Chat/Responses，它按协议提取请求末尾的 user 内容，再做不区分大小写的字面子串匹配；同一 user 内容里附带的客户端提醒也会参加关键词匹配。[S1][S2][S21][S3][S24]

**不要混淆三个能力：** 关键词拦截判断内容是否命中规则；请求用途分类判断整次请求用于主对话、子代理或辅助任务；原始输入恢复需要知道客户端加工前的文字。已核对的 sub2api 关键词入口只实现第一项。Codex 用途字段虽有实际消费者，却用于执行隔离和账号亲和性，不是关键词或审计内容选择。[S1][S10][S11][S12]

## 固定版本与搜索范围

- 官方 `Wei-Shaw/sub2api` 调查时 HEAD：`b0a02a3304a0a78cdd003ddda1dd23ba02c03084`，提交时间 `2026-10-16T04:42:02Z`，提交说明 `chore: sync VERSION to 0.2.17 [skip ci]`；版本文件为 `0.2.17`。GitHub latest 发行记录为 `v0.2.17`，发布时间 `2026-10-16T04:41:43Z`。[V1][V2][V3]
- 下载该精确 commit 的官方源码归档到独立临时目录，只读搜索 `README*`、`docs/`、`backend/internal/`、`frontend/src/`，并对用途头名称全仓搜索；重点读取下列三个核心实现及其 handler、协调器和回归测试。没有调查 fork 或穷举旧版本，因为当前主仓库已找到完整功能。
- “关键词拦截”不是由名称猜测：官方中文界面明确展示该配置，实际服务代码也读取 `blocked_keywords` 并返回 `Blocked=true`。[S0][S1] 不能把其他页面的错误关键词匹配、搜索关键词当成这个功能。

## 1. 真实入口、规则和动作

| 项目 | 源码证实的实际行为 |
| --- | --- |
| 配置入口 | 管理路由 `/risk-control/config` 提供 GET/PUT；配置包含 `blocked_keywords`、`keyword_blocking_mode`、`mode`、分组、模型、状态码、通知和自动禁用用户等字段。功能默认 `Enabled=false`，关键词列表默认空。[S4][S5][S25] |
| 启用前提 | 风控总开关、审查 Enabled、非 off、分组范围和模型范围均先检查。关键词只在 `pre_block` 中运行；`observe` 不执行关键词拦截。[S1][S6] |
| 三种策略 | `keyword_only`：命中拦截，未命中立即放行；`keyword_and_api`：命中直接拦截，未命中才继续 hash/抽样/外部审查；`api_only`：跳过关键词。关键词检查早于抽样和外部 API key 检查，因此其命中不受采样率控制，也不需要外部审核 API。[S1] |
| 规则表达能力 | 字面子串，不是正则；规则和文本都用 Go `strings.ToLower` 后按字节匹配。规则去首尾空白、按小写去重；最多 10000 项，每项最多 200 rune。此关键词配置没有逐规则动作或正则开关。[S3][S24][S5][S7][S27] |
| 作用范围 | 全部分组或配置的分组 ID；模型 all/include/exclude，模型列表按去空白后忽略大小写的完整名称比较。风控用户白名单会保留证据，但强制允许且取消本地拒绝；不是每条关键词独立选择用户。[S6][S8][S26] |
| 命中动作 | 同步返回 `keyword_block`，当前 handler 立即返回，不进入后续模型处理；没有“只删命中的几个字再继续请求”的步骤。关键词日志任务异步入队，包含命中词和提取文本摘要。[S1][S9] |
| HTTP 响应 | 默认 403，配置状态限定 400–599；使用 `content_policy_violation`。常规 Anthropic/OpenAI 旧审查错误路径把它放在 `error.type`，不能一概写成必有 `error.code`；消息来自 BlockMessage。[S27][S28][S14][S36][S16] |
| SSE / WebSocket | 普通 Anthropic 的审查发生在开启流之前；Responses 同样检查流式请求。WS 首个和后续 `response.create` 输入都审查；关键词拒绝发送 error 帧并以 PolicyViolation（1008）关闭，而非给 WS 一个 HTTP 403。Responses compact 心跳已提交响应时，错误 helper 改发 `response.failed`，不能承诺所有入口最终线上的状态都是 403。[S33][S9][S34][S35][S15][S37][S16] |
| 后续处理 | 命中记录可触发邮件和累计违规禁用用户；达到配置阈值后更新 user.Status 并清认证缓存。管理员跳过自动禁用，封触发 API key 仍是 TODO。记录任务队列满会丢任务，因此通知/处罚不是与本次同步拒绝同一事务。[S17][S29] |
| 外部审查 | 关键词未命中后可以继续基于类别阈值的外部内容审查：pre_block 同步决定是否拒绝，observe 异步；该外部审查失败时此服务放行。另一个可选 prompt 安全审计模块由协调器独立组合，不能把它当成关键词自身的语义匹配能力或真人来源判断。[S1][S18][S30] |

## 2. 究竟匹配哪些内容

**不是匹配原始 JSON，不扫描整个历史，也不是无条件向前找“最后一条 user”。** `gjson` 读取指定字段的字符串，抽取块后以换行拼接，最后将空白序列压成单个空格；因此 JSON 转义后的文本参与匹配，而不是转义字节本身。[S2][S21][S23]

| 协议/字段 | 精确提取边界 |
| --- | --- |
| Anthropic Messages | 从 messages 末端仅跳过 `role=system` 的连续项，然后该项必须是 user；不会穿过 assistant/tool 回找历史 user。提取此项的 text/input_text/message，忽略 tool_result 等未知类型。顶层 system 不参与；末尾 user 中的历史引用、IDE 上下文、辅助说明仍属于待查文本。[S2] |
| OpenAI Chat Completions | messages 数组**最后一项必须为 user**；否则为空，不回查历史。提取该项 content 的已知文本类型；system、developer、assistant、tool 等其他角色不进入这次关键词文本。[S2] |
| OpenAI Responses | input 是字符串就取字符串；是数组只看末项；末项需为 user，或 role 缺省且有已知文本/图像内容。不是恢复 previous_response_id 对应的历史；function_call_output.output 不被此提取器读取。[S2][S19] |
| Gemini / 图像入口 | Gemini 只看 contents 末项，role 为 user 或空，读取 parts.text；图像入口读取 prompt。图像 URL/base64 收集到 Images，不会被关键词 matcher 做 OCR；未找到该关键词路径解码任意附件、PDF、文件或任意编码载荷的实现。[S21][S22][S24] |

**最关键的区别：关键词检查刻意保留 `<system-reminder>`；外部语义审查则从审查副本排除含该标签的整个文本块，不是只去掉标签内片段，也没有据此删改发给模型的正文。** 两者共享末尾 user 边界，但 `extractContentModerationKeywordText(...false)` 关闭提醒过滤。测试覆盖标签前、后、独立块、标签内部命中，以及 tool/assistant 末项不取历史。[S21][S23][S19]

因此它能避免一部分“历史和工具返回包含禁词导致误伤”，但不能保证“只查本轮新增真人文字”：客户端放进末项 user 的辅助内容仍会被查，同一输入重发也没有人工新增性证明。[S2][S19] **这里描述的是提取器收到的正文，不是原始网络字节**：Responses handler 在审查前还有 automation/delegation bootstrap 正规化，代码明确标注 `call_output_to_user_message`；不能把提取器忽略 function_call_output 夸大成所有入口都不会审查工具生成文字。[S9]

## 3. 请求用途信号：识别了什么，拿来做什么

| 信号 | 当前固定源码结果 |
| --- | --- |
| `x-claude-code-request-class`、`CLAUDE_CODE_GATEWAY_HINT_HEADERS`、`x-claude-code-agent-id` | 对固定归档全仓精确/忽略大小写搜索无命中。已读审查 Request 构造、legacy adapter 和关键词提取器也没有此类头；未证实用于用途分类、关键词豁免或审计日志选择。这里是所查版本与路径的搜索结论，不外推成全部历史或所有 fork 都没有。[S10] |
| `x-codex-turn-metadata.request_kind` | 确实解析；turn/prewarm/compaction 共用 WS 执行作用域，memory/其他用途分开，避免后台请求抢占用户执行。也读取 body.client_metadata 内同名 JSON；这不是免审或日志排除规则。[S11] |
| `x-openai-subagent` | 确实读取 HTTP 头及 body.client_metadata：参与 WS 执行作用域；guardian/review 配合 `codex-auto-review` 与 parent_thread_id，复用父线程账号亲和性。没有据此调整关键词提取内容。[S11][S12][S10] |
| `/responses/compact` | 有明确路径检测及专用请求正规化；Responses handler 之后仍调用审查。是协议/兼容处理，不是“压缩请求一律忽略”。[S20][S9] |
| Claude agent/compact/权限分类器 | ClaudeCodeValidator 用 UA、必需头、system 模板相似度、计费块字段、探测 max_tokens 等接受 CLI 流量；模板包含 Agent SDK、Explore、Compact，安全监视器另查固定前缀、长度和多个标记。该函数返回 CLI 身份验证结果，不是人工/辅助分类；关键词审查没有调用这些模板来豁免请求。已查相关路径未发现标题/建议专用关键词免审规则。[S13][S32][S10][S31][S1] |

Claude 官方另有真正的用途头：`2.1.273+` 的 request-class 为 `main/subagent/workflow/compaction/auxiliary`，auxiliary 明确包括标题、分类器、摘要；custom base URL 默认关闭，需客户端 `CLAUDE_CODE_GATEWAY_HINT_HEADERS=1`，直连 Anthropic 才默认开启。该开关未发送的字段不能由 sub2api 恢复；本次搜索没有找到 sub2api 提供此环境变量的客户端集成。缺头不证明人工，main/turn 也不证明正文每段都是手写。[C1][C2][S10]

## 4. 对五个实际场景的回答

1. **只拦人工新输入，别误伤历史/system/工具返回：只能部分满足。** 协议末项与类型过滤能排除许多历史/工具内容，但不能区别同一 user 块中的真人和客户端文字。[S2][S19]
2. **只忽略标题/建议/权限分类，主请求放行：未证实有现成开关。** 配关键词去拒绝辅助模板会真的让客户端该调用失败，不是仅不记日志；已有 Codex 用途消费者没有提供此种审计筛选。[S1][S10][S11]
3. **用户正常讨论过滤模板会否误判：会。** 只要待查文本包含配置的词就命中，不理解引用、否定或讨论意图；关键词路径也不会因 `<system-reminder>` 而跳过。[S1][S2][S3]
4. **跨文本块、Unicode、改写能否影响结果：有明确字面边界。** 文本块之间插入空白并整体压缩空白；规则只 TrimSpace，不同样压缩规则内部空白。实现对规则和文本只调用 Go ToLower，没有额外的完整 case-fold、NFC/NFKC、同形字或语义改写处理。不存在“任何拆块/编码/同义改写都能识别”的保证。[S21][S23][S3][S24][S7]
5. **是否影响客户端运行：会。** 它拒绝实际请求，WS 还会关闭连接；辅助调用命中同样如此，累计违规还能禁用用户。具体客户端如何显示、重试或损失哪些功能，本轮未真实运行验证。[S9][S15][S17]

## 5. 与当前 CPA 官方插件的具体比较

CPA `v8.0.27` 的 Before 能拿到入站 Headers 副本及执行 Body，这是官方契约能力；本项目当前 `capture.go` 仅从 Headers 判断 WS，主要按正文及模板筛选审计副本，并始终不返回请求修改。现有代码不是关键词防火墙。[P1][P2][L1][L2]

**为了“审计里只留尽可能接近人工的输入”，不应换成 sub2api 的关键词封禁。** 可借鉴其协议末项/工具类型边界，但应优先读取已存在的结构化用途头与 compact 路径；缺少用途标记时保持未知。Claude 未发送 hint 时仍需要客户端开启，不能靠网关识别出根本没有到达的信息；用途分类也不能恢复混合上下文里的真人原文。[S2][S10][C1][P1]

## 证据与未验证项

以下 sub2api 链接全部固定到本次 SHA。只做静态阅读，测试文件是已存在的回归意图，**没有执行测试**；没有验证客户版本/请求头、真实误报率、吞吐、全部前置正规化和所有协议附件组合。未查 fork、未穷举历史发行版；不声称它们不存在其他能力。已读本项目 verification；本轮只有研究文档，不适用双平台实现验收。

[V1]: https://github.com/Wei-Shaw/sub2api/commit/b0a02a3304a0a78cdd003ddda1dd23ba02c03084
[V2]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/cmd/server/VERSION#L1
[V3]: https://github.com/Wei-Shaw/sub2api/releases/tag/v0.2.17
[S0]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/frontend/src/i18n/locales/zh/admin/channels.ts#L462-L480
[S1]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L864-L1072
[S2]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation_input.go#L129-L260
[S21]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation_input.go#L20-L65
[S22]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation_input.go#L262-L325
[S23]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation_input.go#L394-L407
[S24]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation_keyword_matcher.go#L167-L196
[S25]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L2139-L2169
[S26]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L2906-L2916
[S27]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L72-L90
[S28]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L1717-L1719
[S29]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L1235-L1252
[S30]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/securityaudit/coordinator.go#L29-L125
[S31]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/securityaudit/coordinator_legacy.go#L17-L34
[S32]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/claude_code_validator.go#L232-L280
[S33]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/handler/gateway_handler.go#L232-L238
[S34]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/handler/openai_gateway_handler.go#L2578-L2581
[S35]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/handler/openai_gateway_handler.go#L3010-L3012
[S36]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/handler/gateway_handler.go#L2121-L2133
[S37]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/handler/openai_gateway_handler.go#L4040-L4058
[S3]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation_keyword_matcher.go#L32-L62
[S4]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/server/routes/admin.go#L170-L180
[S5]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L145-L177
[S6]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L824-L862
[S7]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L2814-L2848
[S8]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L2277-L2304
[S9]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/handler/openai_gateway_handler.go#L439-L549
[S10]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/handler/security_audit_helper.go#L160-L177
[S11]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/openai_ws_execution_scope.go#L60-L128
[S12]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/openai_guardian_affinity.go#L25-L99
[S13]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/claude_code_validator.go#L28-L156
[S14]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/handler/security_audit_errors.go#L17-L95
[S15]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/handler/security_audit_errors.go#L130-L173
[S16]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/handler/openai_gateway_handler.go#L3895-L3910
[S17]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L1946-L2008
[S18]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go#L1075-L1168
[S19]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation_reminder_test.go#L39-L130
[S20]: https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/openai_gateway_request_body.go#L578-L595
[C1]: https://code.claude.com/docs/en/llm-gateway-protocol#gateway-hint-headers
[C2]: https://github.com/anthropics/claude-code/blob/03b5892ee77de289bcd5dfa39f541525ade74aff/CHANGELOG.md#L2281-L2283
[P1]: https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/api/handlers/handlers_interceptors.go#L482-L497
[L1]: https://github.com/shenleg/cpa-request-info/blob/0b4afe10fb877864424b5448e24e20b42da23b5c/internal/native/capture.go#L47-L162
[P2]: https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/api/handlers/handlers_context.go#L152-L162
[L2]: https://github.com/shenleg/cpa-request-info/blob/0b4afe10fb877864424b5448e24e20b42da23b5c/internal/audit/client_input.go#L1-L84
