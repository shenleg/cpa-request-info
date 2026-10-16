# CPA HTTP 执行输入记录

本仓库提供安装到**官方 CLIProxyAPI `v8.0.27`** 的原生插件，无需修改、重编或替换用户正在运行的 CPA。正式精简版 `0.2.0` 已通过 macOS arm64、Linux amd64 原生系统与浏览器验收，以及固定官方 Docker 镜像内的非 root 加载、采集和重启验证；尚未部署生产。实际范围和证据见[本轮交付记录](docs/verification/official-http-input-2026-10-16.md)。

当前规范为 [HTTP 执行输入 spec](.scratch/official-http-input/spec.md) 和 [ADR-0004](docs/adr/0004-official-http-execution.md)。安装步骤见[本地交付说明](docs/local-delivery.md)。

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
- 只截断审计副本，不改模型输入。`truncated` 与 `extraction_incomplete` 独立展示，可以同时为 true。
- **普通文本里的秘密、Key、URL、Base64 不自动脱敏；应用层明文保存。** 纯文本展示不代表正文不敏感。

记录字段限于 `id`、`object_id`、`received_at`、`protocol`、`model`、`text`、`truncated` 和可选的 `extraction_incomplete`。名称保存在对象配置中，不逐条复制。

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
| POST | `/monitor`、`/rename` | `{"object_id":"64hex","name":"名称"}`；选择启用或仅改名，名称最多256 UTF-8字节 |
| POST | `/remove` | `{"object_id":"64hex"}`；停止后续采集，保留历史 |
| POST | `/pause` | `{"paused":true}` 或 `false`；主动暂停/恢复 |
| GET / POST | `/settings` | 读取/修改 `max_text_bytes`、`retention_days`、`budget_bytes`；缩短另需 `confirm_retention_shortening:true` |
| POST | `/records/search` | **列表和搜索均使用 JSON 正文**，包含筛选条件、`q`、`offset`、`limit` |
| GET | `/detail?id=<recordID>` | 单条已保存正文和白名单元数据 |
| GET | `/status` | `capture_mode=http_execution`，三种 `supported_protocols`，`responses_websocket=false`，实际采集诊断 |
| POST | `/gaps/acknowledge` | 确认页面实际展示的 `generation` / `revision`；不解除故障或删除历史，并发变化返回409 |

搜索 JSON 使用顶层字段，例如 `{"object_id":"64hex","model":"执行入口模型名","q":"正文关键词","offset":0,"limit":20}`，可另加 `from` / `to`（RFC3339Nano，包含端点）。筛选取交集后计数、分页；`offset` 为0–10000000，`limit` 为1–100、默认20。`model` 精确匹配 Before 正文中的名称；`q` 只搜索已保存全文的字面子串，仅 ASCII 英文忽略大小写，不解释正则、不 trim、不搜索截断尾部或附件。模型最多1024 UTF-8字节，关键词最多65536字节。

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
