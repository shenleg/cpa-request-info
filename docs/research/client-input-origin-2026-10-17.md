# 客户端人工输入与辅助请求来源研究

本文记录官方 CPA RequestInterceptor 的可见字段、Claude Code / Codex 已发送的来源标记，以及这些标记能解决什么问题。研究日期：2026-10-17。只研究和新增本文，不修改实现、客户端配置或部署，不提交。

## 结论

**不是官方插件不能过滤辅助请求，而是目前本项目没有利用客户端已经发送的请求用途标记。不过，服务端过滤辅助请求与恢复用户提交时的原始文字是两个不同目标。**

1. **排除独立辅助模型请求可以明显改进，不应继续只增加正文模板。** CPA `v8.0.27` Before 能读取入站 HTTP 请求头的副本、执行正文以及宿主生成的 `request_path`。Claude Code 有正式的请求分类头；Codex 已核对的发布版有用途元数据和子代理头，旧版还有独立压缩路径。这些信息不需要修改 CPA 宿主才能读取。[P1][P2][P3][C1][D1][D2][D5]
2. **Claude Code 连到 CPA 这样的自定义 base URL 时，请求用途提示头默认关闭。** `v2.1.273+` 支持 `CLAUDE_CODE_GATEWAY_HINT_HEADERS=1`，`x-claude-code-request-class` 的官方枚举为 `main / subagent / workflow / compaction / auxiliary`；`auxiliary` 明确包含会话标题、分类器和摘要。直连 Anthropic API 才默认发送，不可把该默认值套用到 CPA。已启用的客户可以零改配置直接使用；未启用的客户需要配置变更，但不用自写 hook。[C1][C2]
3. **零客户改动仍有可做的部分。** Claude Code `v2.1.139+` 的子代理请求带 `x-claude-code-agent-id`；Codex 的 `x-openai-subagent`、已核对新版本的 `x-codex-turn-metadata.request_kind`，以及旧版使用的 `/responses/compact`，均能识别部分非人工主请求。缺少这些字段不能反过来证明请求来自真人。[C1][C3][D1][D2][D5]
4. **即使 `request-class=main` 或 `request_kind=turn`，也不能把正文称为纯人工原文。** 这些值表示执行用途，不标出消息中每段文字的作者；一个 prompt 还可以产生多次模型调用及子代理调用。对同一个主请求混入的提醒、skill、IDE 上下文，来源头没有逐段原始输入映射。由此推论：它们能改善“排除独立辅助请求”，不能单独完成“恢复客户端加工前的输入”。[C1][D1][D3]

**建议顺序：先在现有 Before 入口使用已确认的路径和结构化用途字段；未知来源保持未知；正文模板只处理没有用途标记的已知格式。需要提高 Claude Code 辅助请求识别率时，再由用户决定是否统一启用官方一个环境变量，而不是默认要求逐客户安装 hook。** 这是根据下述接口与客户端证据形成的方案建议，不是本轮已实施结果。

## 版本与证据范围

| 对象 | 本轮核对版本 | 证据范围 |
| --- | --- | --- |
| 本项目宿主 | `v8.0.27` / `bf9341629ef2300ac9d846a2e80fe2756f63a671` | 与 `host/manifest.json:3-7` 一致；读取固定官方源码，无补丁、未构建。 |
| CPA 当前正式发行版 | 查询时 GitHub latest 为 `v8.0.31`，tag 指向 `355fd10e76b82e03ba6625acc583b900286e4bf2` | 对比请求类型、模型执行和 Before 组装代码；本文所依赖的 Headers / Body / Metadata 来源未改变。其他变化不在本文结论范围。[P7] |
| CPA 默认分支 | 查询时 HEAD 为 `e7003d2a3e2cee2d45f9950c067bf8e02a2ac0f8` | 只记录查询结果，没有用未逐项检查的 HEAD 代码替代固定版证据。 |
| Claude Code 正式文档与 changelog | 固定 changelog commit `03b5892ee77de289bcd5dfa39f541525ade74aff`；当前官方 gateway compatibility guide | 证明请求头名称、值、版本和默认开关；官网文档是动态文档，访问日期为本文日期。[C1][C2][C3] |
| Claude Code 历史官方发行包 | npm `@anthropic-ai/claude-code@2.1.89` | 只读 `cli.js`，追踪旧版 `querySource` / HTTP headers / body metadata。不能据此宣称当前版本没有用途头。[C4] |
| Claude Code 当前发行形态 | npm latest 查询为 `2.1.301` | 官方包分发原生平台二进制；本轮未逆向或执行下载二进制。当前用途头语义采用官方文档，不用旧 JS 猜测。主会话只读确认本机 CLI 为 `2.1.293`，这不是客户版本清单。 |
| Codex 已发布版本 | `rust-v0.155.1` / `be2951ea34f0d295ed0becf97079f92fa5f6950e` | 确认用途字段及 HTTP/body 发送路径；不声称客户已部署此版本。[D1][D2][D3] |
| Codex 当前源码 | 查询时 HEAD `fa46ef3af641f21d0facc69bd11e562fce6a3f9a` | 同类用途字段仍存在；本轮以已发布版本为主要可用性依据。[D4] |
| Codex 历史发布版 | `rust-v0.75.0` / `64bce090e5a5002952dcffb425196552854bb619` | 确认专用 `responses/compact` 和旧子代理头；不做全部中间版本的首次引入考证。[D5][D6] |

Context7 已先查询官方 CPA 文档、Claude Code、Codex。CPA 文档给出插件契约；Claude 查询未给出用途头完整表，转官方 gateway 文档；Codex 查询给出子代理头，继续沿官方固定源码确认用途元数据的实际发送位置。第三方文章与镜像只用于发现官方页面，不作为结论依据。

## 1. CPA 插件收到的字段究竟来自哪里

### 1.1 Headers：常规 HTTP Before 能看见入站头

固定版的实际调用顺序是：

```text
Gin HTTP Request.Header
  → headersFromContext(): Request.Header.Clone()
  → modelExecutionHeaders()
  → coreexecutor.Options.Headers
  → applyRequestInterceptorsBeforeAuth(): cloneHeader(opts.Headers)
  → RequestInterceptRequest.Headers
```

`headersFromContext` 在 `handlers_context.go:152-160` 明确复制 HTTP 头；`modelExecutionHeaders` 优先使用显式执行头，否则读取上述 context；常规 HTTP 执行构造 `Options.Headers` 后调用 Before。Before 在 `handlers_interceptors.go:487-496` 把这些头和正文、元数据交给插件。[P1][P2]

**因此，客户端已经发到 CPA 的 `x-claude-code-*`、`x-openai-subagent`、`x-codex-turn-metadata` 可以直接用于 Before 分类。** 不需要把上游响应头透传开关打开。API 类型的注释虽然称 `Headers` 为“current upstream request headers”，但常规 HTTP Before 的具体来源是上述入站副本，不能只看这句注释推断其已经是 provider 最终发送的头。[P1][P2][P4]

这是固定官方代码的静态结论，不等于本轮已抓到真实客户这些头。前置代理、客户端自定义头、其他排在前面的插件仍可能影响实际内容；宿主会按顺序应用前一个 interceptor 的返回修改。[P6]

### 1.2 Metadata：宿主执行上下文，不是客户端 JSON metadata 的别名

`requestExecutionMetadata` 新建 map，写入可用的 `Idempotency-Key`、`request_path`、认证/执行上下文及 `caller_scope`；后续补充 requested model、reasoning effort、service tier 等执行信息。它没有把整个客户端 body.metadata 或所有 HTTP headers 复制进 map。[P2][P3]

`Metadata.source=plugin_host_model_callback` 的来源非常明确：`ExecuteModel` / `ExecuteModelStream` 设置 `InternalSource=true`，`addModelExecutionSourceMetadata` 才写入该值。它表示 **CPA 插件通过宿主发起的嵌套模型执行**，不是“客户端自动生成内容”。Claude Code 或 Codex 从进程外发来的标题、建议、摘要仍是普通外部 HTTP 执行，不会因此自动获得这个 source。[P5]

因此必须分开三层：

| 信息 | 谁产生 | 含义 |
| --- | --- | --- |
| CPA `RequestInterceptRequest.Metadata.source` | CPA 执行层 | 此处已确认值 `plugin_host_model_callback` 表示宿主内部嵌套执行。[P5] |
| 客户端 HTTP headers / body.client_metadata | Claude Code、Codex 客户端 | 请求类别、子代理、会话或用途，按具体客户端和版本解释。[C1][D1][D2] |
| 模型正文 `messages[].role=user` / Responses 的 user message | 客户端组装的模型上下文 | 对话角色；并不认证为键盘输入者。旧 Claude Code 自动权限判断也构造 `role:"user"`。[C4] |

### 1.3 Body：执行入口正文，仍可读取客户端结构化字段

Before 的 `Body` 是 `coreexecutor.Request.Payload`，正常执行路径以 handler 传入的 `rawJSON` 赋值，然后交给 Before；不是已经统一归一化成“人工文本”的内容。[P1][P2]

Responses handler 读体后可能先运行 Codex 多代理工具准备、orphan delegation 兼容处理，再进入执行。Compact handler 还会删除 `stream` 字段。这说明 **不能承诺 Before 是原始 socket 字节**。但 Before 组装自身没有只取 `input`、重建正文白名单或把 `client_metadata` 改成宿主 `Metadata` 的步骤；如果该字段保留在执行正文里，插件可直接解析。最直接、最少依赖正文处理的 Codex 用途证据仍是实际发送的 HTTP `x-codex-turn-metadata` 头。[P1][P2][P8][D2]

未真实验证所有前置插件、model router、可选 Codex 兼容配置对 `body.client_metadata` 的影响。本文不把“通常可读”提升为所有配置下的原字节保留保证。

## 2. 客户端已有标记对照

下表中的“CPA 可见”指请求确实以常规 HTTP 到达固定版 Before，且该字段未被前置组件删除；不是本轮真实线上抓包结果。

| 客户端 / 版本 | HTTP 或 body 字段 | 能区分什么 | 默认与 CPA Before 可见性 | 局限 |
| --- | --- | --- | --- | --- |
| Claude Code `≥2.1.273` | header `x-claude-code-request-class` | `main` 主对话；`subagent` 子代理；`workflow` workflow 内 agent；`compaction` 压缩摘要；`auxiliary` 标题、分类器、摘要等旁路请求。[C1] | 直连 Anthropic 默认 on；custom base URL 默认 off；`CLAUDE_CODE_GATEWAY_HINT_HEADERS=1` 才发。发到 CPA 后可读 Headers。[C1][P1] | 最合适的独立辅助请求分类字段；不是作者证明。官方文档没有逐项列出本项目所有建议/动作描述模板对应类别。 |
| Claude Code `≥2.1.273` | header `x-claude-code-agent-type` | `Explore / Plan / general-purpose / custom / teammate / fork` 等 agent 类型。[C1] | 与 hint 开关一致；只在子代理自身 turn 上出现。[C1] | 子代理自己的 compaction / side requests 保留 agent ID，但没有 agent-type；不能用缺少 agent-type 判定主请求。 |
| Claude Code `≥2.1.273` | header `x-claude-code-compaction` | `auto / manual / reactive`；idle compaction 也是 auto。[C1] | hint 开启后，压缩请求带此头。[C1] | `x-claude-code-context-compacted` 是压缩后的第一次主请求，不是压缩请求，不能一并排除。 |
| Claude Code `≥2.1.283` | header `x-claude-code-prompt-id` | 将服务同一用户 prompt 的多次请求及其子代理请求分组。[C1][C2] | hint 开启且请求归属于 prompt 时出现。[C1] | 分组不是“这一请求含新人工输入”；不能只按 prompt-id 首见/重复就决定留存。 |
| Claude Code `≥2.1.139` | header `x-claude-code-agent-id`；嵌套 agent 另有 `x-claude-code-parent-agent-id` | 来自会话内被 spawn 的子代理；agent ID 不是人或设备 ID。[C1][C3] | 官方普通 request headers 表列出，不依赖 hint 表；已有客户无需开启 hint 也可利用实际存在的 agent ID。[C1] | 不覆盖主会话的所有辅助调用。没有此头不能证明真人。 |
| Claude Code | header `x-claude-code-session-id`，旧包 body `metadata.user_id` 内的 session/device/account 信息 | 会话/客户端身份。[C1][C4] | 默认请求头/旧包正文可见。[C1][C4][P1] | 不是请求用途；同会话可含辅助调用。 |
| Claude Code `2.1.89` 内部参数 | `querySource=prompt_suggestion / generate_session_title / auto_mode / repl_main_thread` 等 | 客户端内部知道用途。[C4] | 核对的旧版发送路径没有把 querySource 作为 HTTP header 或 body metadata 发出。`source` 在 fetch wrapper 只是调试日志。[C4] | 不能把内部变量当 CPA 可读字段；不能把旧版事实外推为当前版缺少官方用途头。 |
| Codex `rust-v0.155.1`，及所查当前 HEAD | header `x-codex-turn-metadata` 的 JSON 字段 `request_kind`；body `client_metadata["x-codex-turn-metadata"]` 为同类 JSON 字符串 | `turn / prewarm / compaction / memory`。[D1][D2][D4] | 已核对的 HTTP 请求组选项主动附加 compatibility headers；body 也写入 client_metadata；这些发送路径没有另一个“为 CPA 开启用途头”开关。[D2][D3] | 只证明这些版本和路径；不保证全部客户已升级。`turn` 不等于真人原文，仍有子代理/自动执行及混合上下文。 |
| Codex 所查新旧版本 | header `x-openai-subagent` | 当前所查源码映射 `review / compact / memory_consolidation / collab_spawn / Other(label)`；旧版本也有 SubAgent source 转 HTTP 的实现。[D4][D6] | Responses HTTP 请求真正插入此头；CPA Headers 可读。[D2][D4][P1] | 表示会话/子任务来源，不是所有辅助调用的完整标签；`Other(label)` 为开放值。不能假定用户主动启动 review 时其中每段文本都是自动内容，也不能用缺失证明真人。 |
| Codex `rust-v0.75.0` 的 remote compaction | POST `responses/compact`，相对于 provider base URL | 专用压缩调用。[D5] | CPA 注册对应 `/v1/responses/compact` 与 `/backend-api/codex/responses/compact` 时，`Metadata.request_path` 可用于排除。[P3][P8] | 新版压缩也可走 `/responses` 并带 request_kind=compaction，故只看 `/compact` 不完整。[D1][D3] |

`User-Agent`、`x-app`、`originator`、session ID 适合选择“按哪个客户端版本解释字段”，不适合直接作为人工/自动分类。Claude 的 `anthropic-version` / `anthropic-beta` 表示协议及能力，不是作者来源；`x-anthropic-billing-header` 在所查旧包中实际是 system prompt 的文本块，不可因名字像 header 就当成 HTTP 用途头。[C1][C4]

## 3. 两个客户端的关键发送证据

### Claude Code：应优先采用正式用途头，而不是内部 querySource

官方 gateway compatibility guide 对 custom base URL 关闭 hint 的原因写得很具体：避免代理拒绝未知请求头。管理员可以通过 managed settings 的 `env` 统一开启。这比编写各客户端 hook 简单，但仍是客户端配置变更；本轮没有授权或执行该变更。[C1]

正式 changelog 在 `2.1.273` 加入 request-class 等五个 hint，在 `2.1.283` 加入 prompt-id；`2.1.139` 加入子代理 agent-id / parent-agent-id。[C2][C3]

对旧官方 npm `2.1.89` 的只读追踪补充说明“内部 source 不等于 HTTP source”：

- `cli.js:453` 的 `WL` 生成默认 `x-app`、User-Agent、`X-Claude-Code-Session-Id` 等；`te9` 只把 `source` 插入调试日志文字，再调用 fetch，没有把它写入 HTTP header。
- `cli.js:7943` 的 `Sq6` 构造 `metadata.user_id`，编码的是 `device_id / account_uuid / session_id` 及用户显式配置的 extra metadata，不自动包含 querySource。
- `cli.js:1662` 建立 `querySource:"prompt_suggestion"`；`6213` 建立 `generate_session_title`；`2020` 的自动权限判断会把生成内容放入 `role:"user"`；`7948` 的 API 参数组装不能由这些内部标记直接推导为 wire 字段。[C4]

旧发行包的这些观察不用于否定 `2.1.273+` 的正式协议。`isSynthetic` 等未在本轮证明实际发往 HTTP 的内部属性，也不列为可用服务端信号。

### Codex：用途字段不只存在于内部 Rust 结构

已发布的 `rust-v0.155.1`：

1. `core/src/responses_metadata.rs:152-165` 定义 `CodexResponsesRequestKind` 和实际 JSON 值。
2. 同文件 `306-340` 写入 body client_metadata；`345-373` 把用途 JSON 写入 `x-codex-turn-metadata` HTTP compatibility header。
3. `core/src/client.rs:700-714` 构造这些头；`1213-1240` 的 HTTP transport options 将它们放进 `extra_headers`。
4. `codex-api/src/endpoint/responses.rs` 在发出 HTTP Responses 请求时使用这些 extra headers，并按 SessionSource 插入 `x-openai-subagent`；`core/src/client.rs:886` 同时把 client_metadata 放进 Responses request body。[D1][D2][D3]

因此这里是可用的 HTTP/正文标记，不是把客户端内部枚举误当成网络协议。

旧 `rust-v0.75.0` 的 `CompactClient.path()` 返回 `responses/compact`，`compact()` 真正用 POST 发出，`core/src/client.rs:339-367` 调用该客户端。新版本的 compaction metadata 注释则明确覆盖通过 `/responses` 发送的压缩调用。因此可以用专用路径排除旧压缩，也必须接受新版结构化用途分类，不能只依赖路径。[D5][D1][D3]

## 4. 本项目最小改进方向与代价

以下本地结论由主会话调查提供，本研究没有重复读取业务实现：

- `internal/native/capture.go:64-75` 目前只排除 CPA `Metadata.source=plugin_host_model_callback`。
- `capture.go:76-82` 解码 Headers 只用于 WebSocket 判断，`84` 已取得 request_path，`93` 已取得 Body；`91-98` 交给 collector 的字段只有 ID/scope/protocol/body，没有请求用途。
- `capture.go:124-127` 目前把两个 `/responses/compact` 路径也归为 Responses 采集。
- `internal/audit/client_input.go:12-14,55-84` 是建议、动作、标题、转录正文模板；`222-330` 处理提醒、IDE、skill。`CONTEXT.md:43` 已明确识别依赖已知格式，不是真人来源证明。
- `internal/native/app.go:100-104` 的 Before 调用 capture、After 返回空结果；可在现有 Before 获取字段后、交 collector 前直接分类，不需要宿主补丁。README 和 UI 已称为 HTTP 执行输入记录，不建议重复改名。

建议只规划下述改动，**本轮未实施**：

| 方案 | 客户零改动 | 能解决什么 | 代价 / 不能解决什么 |
| --- | --- | --- | --- |
| 在当前 Before 排除明确的 `/responses/compact` | 是 | 已使用专用路径的压缩请求；比正文模板更直接。[D5][P3] | 不覆盖新 `/responses` 压缩，不恢复原始输入。 |
| 按已存在的 Claude request-class、agent-id；Codex request_kind、subagent 头分类 | 是，仅使用实际已收到的字段 | 明确用途的独立辅助调用，不依赖提示词原文。[C1][D1][D2] | 需要按客户端/版本理解，决定是否保留用户主动委派的子任务；字段缺失或未知时不能认定人工。 |
| 在缺少 hint 的 Claude Code `≥2.1.273` 统一启用官方环境变量 | 否；需要用户批准客户端配置变更 | request-class 可区分 compaction / auxiliary / 主对话 / 子代理等。[C1] | 不用自写 hook；若客户没有统一管理渠道，仍有部署成本。不能默认替每个客户开启。 |
| 未知来源仍按当前提取规则处理并清楚保留“未知”，或不采未知 | 是 | 前者保留覆盖率；后者提高已分类集合的确定性 | 前者会继续混入未识别自动内容；后者必然漏掉没有标记的人工输入。这里是产品选择，不是假装规则能消除信息缺失。 |
| 继续维护有限正文规则 | 是 | 清理已知客户端加工块，照顾旧版/无标记客户 | 有误删与漏识别，需要维护，不能成为原始人工输入证明。 |
| 客户端提交事件另传原始输入及结构化分段 | 否 | 若未来确实要求“用户提交前原文”，这是取得该信息的直接位置 | 需要新的客户端集成，不属于本轮零改动方案；服务端用途分类不能替代它。 |

分类发生在审计副本的采集判断，不应据此拦截模型调用本身。缺少用途字段、字段损坏、未知枚举和 header/body 不一致，应明确作为不同状态处理；本文不指定静默猜测优先级，也不建议把无标记归入“人工”。这些是根据证据提出的设计取舍，尚无实现或验收结果。

## 5. 插件能力边界与尚未确认项

### 已确认的边界

- 官方 RequestInterceptor 是执行前后观察/修改接口，不是用户 UI 的提交事件。固定版没有 `is_human`、原始键盘提交文本或每个内容块的作者字段。能使用已有客户端标记，不会凭空生成未上网的信息。[P4][P1][C1][D1]
- CPA 内部 source、客户端 SessionSource/querySource、模型 user role 不能混用。只有确实写进 HTTP/header/body 的信息才是此处的直接输入。[P5][C4][D2]
- 客户端自报用途可以改善正常客户端的来源分类，但不构成不可伪造的人类身份认证；CPA 拿到的是请求内容，不是独立签名的用户提交证明。此项为依据接口输入形成的推论。[P1][C1][D2]
- RequestInterceptor 类型与实际组装在 `v8.0.31` 仍未增加人工来源承诺；仅升级宿主不等于获得“人工原文识别”。[P7]

### 本轮明确未确认

1. **客户实际请求里的字段。** 没有访问用户凭据、真实审计数据或抓真实线上请求。主会话确认工具进程未设置 `CLAUDE_CODE_GATEWAY_HINT_HEADERS`，只说明该进程环境，不代表所有客户。
2. **具体模板到 Claude request-class 的逐项映射。** 官方文档确认 auxiliary 包含 titles/classifiers/summaries，但没有逐项说明本项目全部 prompt suggestion、动作描述、权限转录模板。应使用无敏感文本的合成客户端请求核对用途字段，不能仅凭头名承诺全部样例覆盖。
3. **所有 Codex 版本和后端。** 已证明 `0.155.1` 和所查 HEAD 的 HTTP 发送，不证明任意旧版、第三方 fork、编辑器包装或后端都带同样字段。旧 `0.75.0` 的发送结构不同；没有进行全历史首次引入考证。
4. **所有前置处理对 body.client_metadata 的保留。** 重点已证明 Headers 的入站复制；Body 是执行正文，不能保证经历任意路由、可选兼容处理和前置插件仍逐字保留。
5. **人工来源识别准确率。** 没有真实客户端矩阵、标注数据集、误删率或漏采率实验。不能把几个结构标记或已知模板合成测试当成所有客户端可靠识别。
6. **主请求的原始文字恢复。** 本轮没有找到正式的、跨上述客户端通用的“每段文字原作者/提交前原文”协议；这不是断言任何客户端永远不存在相关新字段。

## 6. 验证与文件改动

- 本研究下载的官方源码/发行包各放在新的空 `/tmp/` 目录；仅作数据读取，读下载内容的 Python 使用 `python3 -I`。未执行下载脚本、二进制或构建。
- 本研究只新增本报告，没有修改实现、`.env`、凭据、客户端设置或 CI/CD；没有 commit、push 或部署。
- 主会话运行 `go test ./internal/audit -run '^TestClientInput' -count=1`，macOS 通过（4.649s）；另运行 `go test ./internal/native -run '^TestHTTPExecutionDiscovery$' -count=1`，通过（0.127s）。这两项是当前已知模板和合成回调回归，不是真实客户端抓包，更不是人工来源准确性证明。
- 本轮未执行 Linux、真实客户端网络验证、容器或部署验收。

## 一手来源索引

### CPA

- [P1：固定版 Before 的实际组装](https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/api/handlers/handlers_interceptors.go#L482-L497)。
- [P2：固定版执行入口正文与 Headers 来源](https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/api/handlers/handlers_execution.go#L47-L104)；[入站头 Clone](https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/api/handlers/handlers_context.go#L152-L162)；[modelExecutionHeaders](https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/api/handlers/model_execution.go#L256-L260)。
- [P3：宿主 requestExecutionMetadata](https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/api/handlers/handlers.go#L251-L295)。
- [P4：RequestInterceptRequest 定义](https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/pluginapi/types.go#L1130-L1152)；[官方插件文档](https://help.router-for.me/cn/plugin/request-interceptor)。
- [P5：内部模型执行设置 InternalSource](https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/api/handlers/model_execution.go#L104-L167)；[写 source 的唯一辅助函数](https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/api/handlers/model_execution.go#L285-L290)。
- [P6：interceptor 顺序与副本](https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/internal/pluginhost/adapters_interceptors.go#L110-L170)。
- [P7：v8.0.31 正式发行](https://github.com/router-for-me/CLIProxyAPI/releases/tag/v8.0.31)；[当前正式版类型](https://github.com/router-for-me/CLIProxyAPI/blob/355fd10e76b82e03ba6625acc583b900286e4bf2/sdk/pluginapi/types.go)；[当前正式版执行组装](https://github.com/router-for-me/CLIProxyAPI/blob/355fd10e76b82e03ba6625acc583b900286e4bf2/sdk/api/handlers/handlers_execution.go)；[当前正式版 Before](https://github.com/router-for-me/CLIProxyAPI/blob/355fd10e76b82e03ba6625acc583b900286e4bf2/sdk/api/handlers/handlers_interceptors.go)。
- [P8：固定版 Responses / Compact handler](https://github.com/router-for-me/CLIProxyAPI/blob/bf9341629ef2300ac9d846a2e80fe2756f63a671/sdk/api/handlers/openai/openai_responses_handlers.go#L553-L688)。

### Claude Code

- [C1：官方 gateway compatibility guide 的 Request headers 与 Gateway hint headers](https://code.claude.com/docs/en/llm-gateway-protocol#gateway-hint-headers)，包含版本、custom base 默认关闭、全部枚举、agent ID 与 prompt-id 含义；[普通请求头](https://code.claude.com/docs/en/llm-gateway-protocol#request-headers)。
- [C2：固定 changelog，2.1.273 用途头](https://github.com/anthropics/claude-code/blob/03b5892ee77de289bcd5dfa39f541525ade74aff/CHANGELOG.md#L2281-L2283)；[2.1.283 prompt-id](https://github.com/anthropics/claude-code/blob/03b5892ee77de289bcd5dfa39f541525ade74aff/CHANGELOG.md#L1490-L1492)。
- [C3：固定 changelog，2.1.139 子代理 ID 请求头](https://github.com/anthropics/claude-code/blob/03b5892ee77de289bcd5dfa39f541525ade74aff/CHANGELOG.md#L5476-L5493)。
- [C4：Anthropic 官方 npm 2.1.89 固定版本发行包](https://registry.npmjs.org/@anthropic-ai/claude-code/-/claude-code-2.1.89.tgz)。`package/cli.js` 行号与函数在正文列出。压缩包 SHA-256：`680e35001b24b604f58958e3a324bb758be3c069c0a3f89585156256f17a9c87`；cli.js SHA-256：`a9950ef6407fdc750bddb673852485500387e524a99d42385cb81e7d17128e01`。这是官方版本化发行物，不是第三方反编译仓库。

### Codex

- [D1：已发布 0.155.1 用途枚举](https://github.com/openai/codex/blob/be2951ea34f0d295ed0becf97079f92fa5f6950e/codex-rs/core/src/responses_metadata.rs#L105-L170)；[用途元数据的 body 与 HTTP 输出](https://github.com/openai/codex/blob/be2951ea34f0d295ed0becf97079f92fa5f6950e/codex-rs/core/src/responses_metadata.rs#L294-L374)。
- [D2：0.155.1 HTTP 选项真正附加用途头](https://github.com/openai/codex/blob/be2951ea34f0d295ed0becf97079f92fa5f6950e/codex-rs/core/src/client.rs#L1208-L1241)；[compatibility_headers 使用](https://github.com/openai/codex/blob/be2951ea34f0d295ed0becf97079f92fa5f6950e/codex-rs/core/src/client.rs#L700-L714)；[Responses HTTP endpoint 实际发送](https://github.com/openai/codex/blob/be2951ea34f0d295ed0becf97079f92fa5f6950e/codex-rs/codex-api/src/endpoint/responses.rs)。
- [D3：0.155.1 body.client_metadata](https://github.com/openai/codex/blob/be2951ea34f0d295ed0becf97079f92fa5f6950e/codex-rs/core/src/client.rs#L865-L888)；[新版压缩构造 Compaction 用途](https://github.com/openai/codex/blob/be2951ea34f0d295ed0becf97079f92fa5f6950e/codex-rs/core/src/compact_remote_v2_attempt.rs#L76-L105)。
- [D4：当前所查 HEAD 的用途枚举](https://github.com/openai/codex/blob/fa46ef3af641f21d0facc69bd11e562fce6a3f9a/codex-rs/core/src/responses_metadata.rs#L119-L185)；[HTTP 子代理头映射](https://github.com/openai/codex/blob/fa46ef3af641f21d0facc69bd11e562fce6a3f9a/codex-rs/codex-api/src/requests/headers.rs#L16-L30)；[实际加入 HTTP](https://github.com/openai/codex/blob/fa46ef3af641f21d0facc69bd11e562fce6a3f9a/codex-rs/codex-api/src/endpoint/responses.rs#L75-L96)。
- [D5：0.75.0 压缩专用路径与 POST](https://github.com/openai/codex/blob/64bce090e5a5002952dcffb425196552854bb619/codex-rs/codex-api/src/endpoint/compact.rs#L39-L80)；[客户端实际调用](https://github.com/openai/codex/blob/64bce090e5a5002952dcffb425196552854bb619/codex-rs/core/src/client.rs#L339-L367)。
- [D6：0.75.0 SessionSource 转成子代理头](https://github.com/openai/codex/blob/64bce090e5a5002952dcffb425196552854bb619/codex-rs/codex-api/src/requests/headers.rs#L14-L26)；[Responses 请求加头](https://github.com/openai/codex/blob/64bce090e5a5002952dcffb425196552854bb619/codex-rs/codex-api/src/requests/responses.rs#L125-L142)。
