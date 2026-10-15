# CPA 请求输入审计：最小安全闭环

本仓库提供 CLIProxyAPI `v8.0.27` 的最小宿主补丁及真实 C ABI 原生插件。当前覆盖 [ticket 01](.scratch/request-input-audit/issues/01-minimal-safe-audit-loop.md)、[ticket 02](.scratch/request-input-audit/issues/02-monitoring-lifecycle.md)、[ticket 03](.scratch/request-input-audit/issues/03-chat-input-boundaries.md)、[ticket 04](.scratch/request-input-audit/issues/04-responses-http.md)、[ticket 05](.scratch/request-input-audit/issues/05-anthropic-messages.md) 、[ticket 06](.scratch/request-input-audit/issues/06-http-request-once.md) 、[ticket 07](.scratch/request-input-audit/issues/07-responses-websocket.md) 、[ticket 08](.scratch/request-input-audit/issues/08-search-and-browse.md) 、[ticket 09](.scratch/request-input-audit/issues/09-retention-and-settings.md) 与 [ticket 10](.scratch/request-input-audit/issues/10-storage-budget.md) 的本地验收范围，**仅供隔离合成数据验收，不是完整第一版或生产审计交付**。

## 已实现范围

- 固定官方 commit `bf9341629ef2300ac9d846a2e80fe2756f63a671`，保持 C ABI 1 / RPC schema 6，独立只读观察契约升级为 3，要求三种 HTTP 与 Responses WebSocket 接入齐备；旧契约不静默降级。
- 宿主在最终内置客户端 Key 鉴权后、handler 读体前同步冻结本次提交的无秘密身份与采集决策；读体后、业务改写及模型路由前只读观察，不重新按名单或主动暂停决定资格。不是 usage queue 或执行请求拦截器。
- 宿主随机 seed + HMAC-SHA-256 产生稳定无秘密对象 ID；不同 Key 分开，目录和认证采用同一算法，不依赖配置下标/名称。标识不能登录。
- 默认监控名单为空。管理员选择 ID 并命名、仅改名、移除或重新加入；名称仅在对象目录保存，旧/新记录显示当前名称。同 Key 重返名单沿用历史，不同 Key 即使同名也独立。不粘贴原客户端 Key，不改变宿主 Key 配置。
- 全局主动暂停/恢复单独持久化，不解除存储故障、不把预期跳过记为缺口；后续变更不取消已接收/入队副本。对象名称、停用及暂停状态正常重启后保留。
- 当前有效 Key、已移除但有历史及 Key 已无效的对象具有独立状态；管理员可按对象筛选历史，查询不要求原 Key 仍有效。已知对象总数最多1024（包含停用对象），名称最多256 UTF-8字节。
- 仅判断 Chat Completions 最后一条显式消息；连续 `user` 只取最后一条，不跨消息拼接。末尾 `assistant` / `tool` / `function` / `system` / `developer` 正常跳过，不回找旧用户发言。
- 选中 `user.content` 支持字符串或内容数组：`text` 原样保存；`image_url`、`input_audio`、`video_url`、`file` 的对应非 null 对象载荷仅生成 `[图片]`、`[音频]`、`[视频]`、`[文件]`。按原顺序直接组合，不保存附件载荷、文件名、URL、Base64 或引用标识。`tool_call` / `tool_use` / `tool_result` 整块排除，不递归提取嵌套内容。
- 未知/畸形块不序列化：安全部分保存并标记 `extraction_incomplete`，每次提交增加一次 `unsupported_content` 缺口；完全无法安全提取则只报缺口。缺失字段、重复键和大小写别名不静默回退为完整输入，也不保存整请求兜底。空字符串、显式 `null`、空数组、仅合法空文本或工具块正常跳过，空白文本不 trim。
- 文本与占位符默认合计最多 64 KiB UTF-8 安全前缀，可在受保护设置/页面改为1–65536字节，只影响后续提交快照；历史和已接收副本不追溯重截断，不含元数据；恰好上限不误报，首次字符越界后不再拼后续短块。只截断审计副本，列表和详情分别显示截断/提取不完整，两种状态可同时存在。请求模型缺失/非字符串时记为未知，不猜测。普通文本里的 URL、秘密、Base64 **不会自动脱敏**。
- Responses HTTP 覆盖标准、compact 与 Codex direct 别名 POST 入口；GET WebSocket 握手不观察。字符串 `input` 是本次用户文本，结构化列表只取末尾明确 `user` 消息（`type` 省略或为 `message`），不回找/拼接历史。`input_text` 原样提取；`input_image`、`input_file`、固定宿主支持的 `input_audio` 和 `input_video` 只留占位符。已知工具/模型生成项整体排除，未知项与无法判断的 `item_reference` 报解析缺口，不解析引用。
- `previous_response_id` 不参与历史回查或持久化，显式重发独立记录；客户端请求模型先于路由及上游别名映射提取，不从回复补猜。宿主 capabilities 必须显式声明三种 HTTP 协议及 `responses-websocket`，契约版本不是3或缺少任一接入点均拒绝采集，不静默显示全覆盖。
- Anthropic Messages 在 `/v1/messages` 认证后、模型 DD 名称改写/协议转换/路由之前观察原始消息。末尾连续 `user` 只取最后一条；纯 `tool_result` 正常跳过，混合消息只提取新 `text` 和固定附件占位符，不提取工具结果内嵌文本、图片、文件或引用。`image` 只留 `[图片]`，`document`（包括 text/content 来源）只留 `[文件]`；未知类型/来源报告提取不完整或解析缺口，不猜测其他协议的内容类型。
- 64 条 / 8 MiB 有界副本队列，约 1 MiB 分段 JSONL，写入并同步成功后才允许管理查询；入队不等于保存。
- 默认总预算 1 GiB，合法范围 4 MiB–1 GiB，可通过管理页面/接口调整并持久化；保守核算整个专用目录及辅助文件，预算内预留 1 MiB 配置更新空间及 1 MiB + 4 KiB 维护空间。满额停采，不覆盖或提前删除未到期历史；正常清理或调高预算释放足够余量后自动恢复，主动暂停不受影响。容量基础告警保留到自身到期，不因恢复/重启清除，未恢复容量故障持续显示。
- 输入默认从宿主接收提交起保留7个24小时，管理页面/API可调整为1–30整天；限制最短一天避免零期限，最长30天限制敏感数据寿命并使历史期限规则有界。到期即退出列表、组合搜索、详情和对象历史标记，不等待文件回收；启动及每分钟清理，暂停、移除和停机不冻结期限。
- 缩短需明确确认历史提前到期的影响；未确认返回409且不部分修改配置。调长只适用于生效时刻之后的提交，不延长旧历史或使记录重新可查；慢体和排队副本从原接收时间算起。没有清空对象、全量清空或提前删除未到期历史的管理功能。
- 宿主插件菜单、无数据静态 shell、受保护对象/选择/状态/列表/详情 API。正文和名称只按纯文本展示，不渲染 HTML/Markdown，不自动加载外部资源。
- 对象、服务端接收时间、客户端请求模型和正文关键词取交集后计数/分页；只搜索已保存全文，英文忽略大小写、中文与搜索语法字符按字面，不搜索未保存历史、截断尾部或附件载荷。列表最多100条、预览最多512 UTF-8字节；详情独立显示单次提交及两个内容状态，不拼会话。
- 最近查询页最多保留 100 条记录的分段/偏移位置提示，详情只读取对应已同步行并重新校验；不缓存正文、不建立全量 ID 索引、不新增持久化文件。筛选采用有界逐条扫描，未承诺大历史搜索延迟。
- 输入审计管理路径的宿主访问日志不记录查询字符串，避免额外保存搜索词或被误填作关键词的认证 Key；方法、路径、状态及耗时等原日志仍保留，其他路径不变。未命中时仍可扫描查询。

## 未完成与风险

尚无上述白名单以外的内容类型、三协议完整 SSE/重试/断开回归矩阵、异常退出恢复或完整缺口历史。Linux amd64 已在 Debian 13 原生构建并完成现有系统、工具及七个浏览器场景各两轮回归，本票另以隔离64MiB tmpfs验证真实ENOSPC和只读重启故障；Linux arm64、其他块设备/文件系统及运行中热切只读仍未验证，详见[本票双平台验收记录](docs/verification/storage-budget-2026-10-15.md)和[上一票Linux记录](docs/verification/linux-amd64-2026-10-15.md)。当前已增加统一到期隐藏、周期回收与保留设置；暂停、移除不会改变接收时间或提前删除未到期历史。容量自动恢复不代表真实 I/O/完整性故障可自动恢复，正常重启也不等于异常恢复 ticket 11 已完成。

应用层不加密。专用目录为 `0700`、文件为 `0600`，仍需保护主机、磁盘、快照和备份。逻辑到期和文件空间回收不等于磁盘、数据库空闲页、文件系统、快照或备份安全擦除；应用不控制外部副本的保留。没有安全擦除或 native 进程崩溃隔离保证；常规解析/队列/存储错误 fail-open，不代表零丢失、零延迟或吞吐承诺。诊断包含有界运行期原因码/已知计数，以及固定大小的持久容量告警摘要；运行期计数正常重启后归零，不能解释为历史没有缺口，也不是完整可恢复缺口历史。

宿主 `identity.seed` 必须与数据目录一起保护和备份；插件不读取/修改它的内容。已有数据但 seed 缺失、损坏或权限不合规时停采，不静默轮换全部对象。目录祖先不能是符号链接，macOS 使用真实路径，例如 `/private/var/...` 而非 `/var/...`。

官方宿主启动仍可能进行 npm/版本等**既有后台网络请求**；`--local-model` 不表示完全离线。本项目模型验收流量仅去本地可控上游，不使用真实提供商或凭据。

## 本地构建

需要 Go ≥ 1.26、启用 CGO、C 编译器、Git、Python 3、`tar` 和 `patch`。已在 macOS arm64 和 Debian 13 / Linux amd64 上使用 Go 1.27.2 原生构建并运行验收；仅交叉编译不算目标平台运行通过。Linux 浏览器还需 Chromium 系统运行库，具体环境和未验证项见[Linux amd64 验收记录](docs/verification/linux-amd64-2026-10-15.md)。

```bash
make deps              # 明确准备 Go/Node 依赖；不执行 npm 生命周期脚本
make browser-install   # 明确下载固定 Playwright 所需 Chromium
make check             # 快速格式、vet 与编译检查，不启动宿主
make hooks-install     # 只给本仓库安装暂存快照检查；遇已有 hook 冲突即停止
make test-one TEST=TestSavedLatestInput
make verify            # 全部系统/工具/浏览器验收；正常停止进程，保留证据
```

快速检查离线执行，缺少依赖会失败，需先 `make deps`。pre-commit 通过 `git write-tree` 导出真实 index 的独立快照，避免部分暂存时工作树的未暂存修复掩盖坏代码；它不自动格式化、不修改 index、不下载依赖、不跑压力或磁盘故障测试。导出快照保留在 `.test-data/`。可显式运行 `make check-staged`。

本项目执行本机macOS及 `ssh linux-service` 隔离Linux验收，不配置或触发远端CI。统一入口为 `make verify`；每张ticket以同一份最新运行源码在两端完成本票要求的构建、系统、工具和浏览器验收后才标记完成，具体规则见[验证与交付约定](docs/agents/verification.md)。单端通过不代表另一平台已验证。

构建脚本核实 Git commit 和补丁摘要，从精确 Git 对象导出**全新隔离目录**，应用无 fuzz 补丁，避免把旧工作树改动混入构建。不执行下载的安装脚本，不自动删除目录。

产物：

```text
.build/bin/cliproxyapi
.build/plugins/request-input-audit.dylib  # macOS；Linux amd64 为 .so，已完成 Debian 13 原生运行验收
```

`go test ./...` 的系统测试要求先构建宿主及插件，不因产物缺失静默跳过。测试仅创建合成配置和受控 HTTP 上游，数据保留在 `.test-data/`；正常停止本测试创建的进程，不自动删除数据。

已补充实际队列满、真实 `ENOSPC` 和真实只读卷 `EROFS` 的专项测试，见下节。断电、运行中热切只读、完整恢复等仍未全面验证，不能将这些专项概括成全部故障已验收。

## 补充故障与压力验收

```bash
go test ./tests/system -run TestQueueFullPreservesUpstreamAndDrains -count=1 -v
go test ./tests/system -run 'TestRealENOSPCFailOpen|TestRealReadOnlyVolumeFailOpen' -count=1 -v
```

压力夹具使用宿主已有的 `MANAGEMENT_PASSWORD` 环境鉴权入口，仅设置本次合成密码，避免几百次详情查询重复做 bcrypt 淹没队列验收；匿名、客户端 Key 和错误管理密码仍必须被拒绝。普通权限测试保持配置 bcrypt 路径。没有关闭鉴权，也没有给生产代码增加测试后门。

- **队列满**：真实宿主、128 并发、每批 1,024 个 64 KiB 合成提交，最多 4,096 次；必须从受保护管理状态实际看到 `queue_full`，否则测试失败。确认所有模型请求正常到可控上游、输入完整，排空后 `saved + queue_full = 提交数`，查询只展示已保存结果。公开 `queued` 计数包括正在写入的 1 条，因此峰值可为 65，并不表示 64 容量 channel 无界。
- **ENOSPC**：macOS `hdiutil` 创建全新的 64 MiB HFS+ 镜像，先校验独立设备与容量，再仅在该卷的审计目录之外填充，必须实测得到 `ENOSPC`。随后真实模型调用仍应为 `200`，采集停下且出现 `storage_error`，失败副本不能冒充保存，原已保存记录保留。
- **只读卷**：先在可写镜像保存输入，停止本测试宿主后正常卸载，原镜像以 `-readonly` 重挂；外部创建文件必须实际得到 `EROFS`。重新启动宿主后观察/存储初始化明确不可用，实际模型调用仍为 `200`。这是**只读环境下重启的 fail-open 验证**，不冒称运行中写入者经历热切只读，也不保证只读状态下管理页能查询历史。

镜像测试不使用 `sudo`、不格式化物理磁盘、不强制卸载、不修改全局磁盘设置。只卸载本次新镜像，不删除镜像、填充文件或验收目录。macOS 镜像单项失败会明确报错；其他平台此镜像夹具明确跳过，不能以skip宣称通过。本票另在原SSH Linux环境用私有64MiB tmpfs补测实际ENOSPC及停止写入者后的只读重启，结果与范围见[双平台验收记录](docs/verification/storage-budget-2026-10-15.md)。

## 合成浏览器验收

```bash
bash scripts/fetch-management-ui.sh
go build -o .build/bin/audit-demo ./cmd/audit-demo
.build/bin/audit-demo
```

打开命令输出的 `management.html` 地址。合成管理 Key 为 `synthetic-admin-audit-test-only`；客户端 Key 为 `synthetic-client-audit-a-only` 和 `synthetic-client-audit-b-only`；仅此本地实例使用，**不要配置真实 Key**。模型名 `audit-test-model` 只转发到命令创建的本地合成上游。

1. 宿主管理页登录，进入“输入审计”菜单。
2. 优先复用宿主同源已有登录状态；未勾“记住密码”而无法取得时，按用户确认允许临时输入同一个管理 Key。只存审计页内存，输入框立即清空，刷新后消失；不另写 localStorage/cookie 或服务端凭据。
3. 选择无秘密对象 ID 并命名，提交合成 Chat 请求。
4. 刷新已保存列表，查看单条纯文本和截断/采集状态。

固定官方管理前端为 `v1.26.2`，摘要见 `host/management-ui.json`。复用的 `cli-proxy-auth` 状态格式是此版本的兼容契约，不声称官方有临时 token 桥或未来前端自动兼容。插件不新增凭据持久化；宿主管理前端自身的“记住密码”行为不由插件改变。

自动验收只需 `make browser`：构建固定宿主/插件及 demo、核对管理页面、启动真实实例、运行六个场景各两轮并停止进程，不依赖 MCP。已有构建产物时可用 `make browser-run`。启动 JSON 只有健康检查通过后才发 `ready`，失败/超时有明确退出；runner 不保存子进程 stderr 原文或 Playwright 可能含凭据的调用日志，只报告固定阶段/原因和本项目场景行号。

场景位于 `tests/browser/smoke.js`、`tests/browser/truncation.js`、`tests/browser/lifecycle.js`、`tests/browser/responses-websocket.js` 、`tests/browser/search-browse.js` 、`tests/browser/retention-settings.js` 与 `tests/browser/storage-budget.js`。预算场景验证三项原子设置、精确字节转换、非法值不发请求、刷新/重载、容量/超额暂停、未到期历史保留、调高自动恢复及主动暂停独立。保留设置场景验证缩短影响确认、取消无写入、合法范围、原子保存、页面重载/刷新保持设置，每轮恢复默认值。搜索场景验证四条件交集、23条匹配结果的翻页与重新筛选、双状态详情、恶意正文/名称/模型纯文本以及没有外部请求。其余场景分别验证菜单与纯文本安全展示、65,535字节截断标签/提取不完整/双状态及可见缺口、Responses 协议与请求模型/未知模型展示、Anthropic 工具混合输入与原协议/双状态展示，以及页面改名、移除后历史筛选、同Key重新加入、主动暂停/恢复；WebSocket 场景在同连接提交五次，验证逐条详情、协议与独立状态、上限改为7字节/刷新/拒绝非法值/恢复默认且不追溯改旧正文。同一宿主连续两轮且每次使用唯一标记/记录 ID，旧数据不能冒充本轮通过。有限证据保存在 `.test-data/verification-*/result.json` 和状态标签截图；没有成功验收的结果不会报告为通过。

浏览器环境使用精确版本 Playwright 和明确的 `zh-CN` locale，匹配固定管理前端；不宣称所有浏览器/语言/未来前端已验证。验收责任与工作区审查的项目约定见 `docs/agents/verification.md`；个人技能文件不修改。

## Chat 输入边界验收

`tests/system/chat_boundaries_test.go` 使用真实 HTTP、受保护列表/详情/状态与可控上游，覆盖末尾角色、顺序混合块、工具嵌套排除、未知/歧义块、空白、64 KiB 合计及跨块多字节前缀。预期为独立夹具字面值；未知或应跳过案例以随后的唯一提交落盘作为屏障，不依赖固定 sleep。重启验证旧记录缺字段按 false 读取，以及新记录截断/不完整状态保留。

专项检查扫描整个隔离审计目录和宿主/插件诊断输出，检查字段白名单及禁止标记，不只检查管理 API 没返回。开启/关闭审计的可控上游字节逐次相等；宿主本来无法转发的畸形请求比较原响应。查询正文只能包含实际保存前缀，本票不提前实现后续搜索接口。

```bash
make build
make test-one TEST='TestChat'
```

## Responses HTTP 输入边界验收

`tests/system/responses_http_test.go` 和 `tests/system/responses_http_execution_test.go` 沿用已确认的公开系统边界：真实客户端 HTTP → 固定补丁宿主/原生插件 → 管理列表、精确详情与状态。覆盖字符串、末尾消息、纯工具续跑、混合非文本/未知块、空白/空输入、协议字段歧义、请求模型未知、鉴权/未监控/握手跳过、各 HTTP 入口、UTF-8 截断及正常重启。

可控上游验证 Responses 转换为 Chat 执行后请求字节与宿主公开响应在审计开启/关闭时一致，且实际模型别名不同于持久化请求模型。阻塞上游后先查到已保存输入，再释放上游失败响应，证明采集不等待回复。使用宿主已有 `upstream.codex.orphan-delegation-compatibility` 与合成 `X-Openai-Subagent` 触发工具输出→user 改写，证实上游实际收到改写后的 user，但审计不保存该工具输出/历史。

正文观察是 `ReadRequestBody` 解码内容编码之后、handler 业务改写之前的原协议输入，不是压缩 wire bytes；接收快照早于 handler 读体，不保证早于宿主既有中间件第一次读取 socket。隔离目录及诊断扫描检查白名单和禁存标记；不保存整条请求、会话标识、认证 Key 或附件载荷。

```bash
make test-one TEST='TestResponsesHTTP'
```

固定内容白名单：`input_image` 要有非空字符串 `image_url` 或 `file_id`；`input_file` 要有 `file_data`、`file_url` 或 `file_id`；`input_audio` 识别嵌套 `input_audio.data` 或扁平 `data`；`input_video` 识别 `video_url` 字符串或对象内的 `url`。这些字段只用于瞬时判断占位，不持久化来源或数据。畸形/未知形状报告缺口，不用宿主转换后消息替代。协议类型依据 [OpenAI Responses schema](https://github.com/openai/openai-openapi/blob/main/openapi.yaml) 与固定宿主已支持形状限定，不承诺未来新增类型自动兼容。

## Anthropic Messages 输入边界验收

`tests/system/anthropic_messages_test.go` 与 `tests/system/anthropic_execution_test.go` 使用相同公开系统边界，覆盖末尾用户选择、纯工具续跑/混合新内容、附件/文档内部内容排除、未知块、空白、字段歧义、认证/未监控跳过、管理鉴权、UTF-8 安全前缀及正常重启。`/v1/messages/count_tokens` 不属于模型提交，不采集。附件 `source` 白名单依据 [Anthropic Messages schema](https://github.com/anthropics/anthropic-sdk-typescript/blob/main/src/resources/messages/messages.ts)：`image` 识别 `base64`/`url`/`file`，`document` 另识别 `text`/`content`；只判定来源，不保存或递归提取载荷。音频、视频和未来类型不因其他协议支持就推断为已识别，报告缺口。

本地原生 Anthropic 上游逐次比较审计开启/关闭的请求字节和 HTTP/SSE 响应，核对历史、系统指令、超限尾部和未知块仍由原宿主正常转发。宿主既有 system 内容数组规范化及 `cache_control` 注入不由审计改变；测试另核对业务内容保持完整。客户端 DD 模型名经宿主解码和上游别名映射后仍以原请求名进入审计，证明观察先于业务改写。阻塞上游直到受保护详情已查到本轮输入，再返回失败，证明不等待回复。审计目录/诊断扫描只检查字段白名单及禁存标记，不复制输入或日志原文为报告。

```bash
make test-one TEST='TestAnthropic'
make browser-run
```

Anthropic 本票未宣称该协议的重试/断开完整矩阵、其他平台或后续票据已完成；Responses WebSocket 单独见下节 ticket 07 验收。

## HTTP 一次提交语义验收

`tests/system/http_once*_test.go` 在 Chat Completions 已可用链路验证通用机制，不据此宣称 Responses HTTP、Anthropic Messages 的完整失败/重试矩阵已完成；三协议回归仍由 ticket 13 执行。

- 相同文本、相同客户端请求 ID/会话信息重交两次，产生不同宿主提交 ID 的独立记录，不按内容或客户端字段去重。
- 正常、上游失败、上游 `408` 超时响应及 SSE 请求，在上游响应/首 token 被通道屏障阻塞时已能查询本轮精确详情；SSE token 后失败保留原错误完成语义。公开响应及转发请求与关闭审计的宿主对照，不把正常协议改写当作审计副作用。
- 公开 `routing.retry` 配置触发同账号重试或真实账号切换；SSE 在建立流后先发错误帧，触发首 token 前内部重试。可控上游明确收到两次完整执行请求，但管理员始终只看到一次客户端提交。
- `tests/fixtures/http-once-plugin` 是仅测试使用的 C ABI 插件，从原客户端请求拦截回调调用官方 `host.model.execute`。上游确实收到内部输入及原客户端输入，审计只保存后者。夹具编译/审计库复制到全新 `.test-data/` 插件目录，不加入默认 `.build/plugins/` 或部署产物。
- 实际客户端 HTTP 取消和期限超时传到上游；SSE 另在客户端收到首 token 后断开。已经接收的合格输入仍保存，取消不变为采集故障。
- 路由前失败仍保存；鉴权失败不记录。真实 TCP 在认证后 `100-continue` 屏障后发送含完整 JSON 的不完整 chunk，并半关闭写端：只能报 `read_error`，不得保存收到的正文前缀；畸形 JSON 报 `invalid_json`。后续唯一提交落盘是负向检查屏障，不靠固定 sleep。
- 外部隔离存储权限故障和模型失败同时发生时，公开模型错误保持不变，保存失败单独报 `storage_error`，不冒充成功保存。管理详情和持久化字段仅含输入白名单，不增加响应正文、状态追踪或执行日志。

每个可重复场景在同一实例跑两轮，以本轮唯一标记/记录 ID 等待已保存结果，管理状态明确核对 `saved`、`queued` 和固定缺口计数。共享等待/上游对照模式可用于最终三协议交付回归；不通过私有 lifecycle 或 usage queue 证明提交次数。

宿主原有执行失败 warning 可能包含上游错误正文，关闭审计时也会出现；本票比较开关审计的标记次数，确认审计未新增该输出，并单独扫描整个审计目录确保错误正文没有落盘。插件不使用或复制该执行日志，不能把本项目的输入白名单保证误解成宿主已有日志已被脱敏。

```bash
make build
make test-one TEST='TestHTTPOnce'
```

## Responses WebSocket 消息级验收

`tests/system/responses_websocket*_test.go` 在标准和 Codex direct GET 入口使用真实 WebSocket 客户端、真实宿主与原生插件、受保护管理 API、本地 Chat SSE/Codex WebSocket 上游。每个完整 Text/Binary 帧在共同 reader 的输入队列之前观察；普通 handler 和 Codex duplex 后续 `response.create`、`response.append`、带业务 `input` 的 `response.steer` 都覆盖。提交使用独立宿主 ID 和服务端时间，握手不产生记录、不冻结监控配置；名字、名单、暂停和1–65536字节上限在同连接下一提交生效。原生 Key 移除拒绝新的鉴权，但不为审计改变已有连接的原认证语义。

审计只提取原帧本次显式 `input`，不恢复历史、不查询 `previous_response_id`、不记录工具续跑；未显式传模型时显示未知。`generate:false` 预热带有显式输入时仍记录，宿主后续恢复的空续跑不再采集。普通 fallback 原本拒绝字符串 input，审计不改变该拒绝；原生 duplex 的字符串 steering 可转发。未知/歧义事件仅报告固定缺口，已知控制/取消不记录；收到非终帧前缀后断开只报 `read_error`、不保存前缀。

公开上游和事件证明：实际历史合并/预热恢复没有扩大审计正文；首 token 前内部重试仍只保存一次；活跃上游阻塞时后续帧已取得快照并保存，随后的暂停/上限变更不追溯影响旧副本；真实首 token 后 interrupt 与断开取消上游但保留记录。普通 HTTP fallback 及原生 duplex/steering 的审计开关对照逐次比较上游请求字节与公开事件，截断/未知块及真实目录权限故障不改变模型执行。隔离存储全字段白名单与诊断禁存标记扫描不只检查管理 API。

```bash
make test-one TEST='TestResponsesWebsocket|TestSettings'
make browser-run
```

管理页面复用原列表/单条详情，`responses-websocket` 显示为 Responses WebSocket，截断和提取不完整独立标记。当前浏览器七场景各两轮，仅保存有限状态截图/结果；macOS arm64与原SSH Debian 13 / Linux amd64环境均已验证，预算新增场景也在两端实际运行。异常恢复、Linux arm64、Linux 磁盘故障及三协议最终失败矩阵仍未完成，未宣称完整第一版已交付。

## 保留期与配置验收

```bash
make build build-retention-test
make test-one TEST='TestRetention|TestSettings|TestSearch'
```

`make test` / `make verify` 会成对构建普通产物及 `.build/retention-test/` 的隔离时间产物。隔离宿主使用 Go overlay 只替换 HTTP/WebSocket 审计接收取时，隔离插件使用 `audittest` build tag；两者读取同一专用时钟文件，推进当前时间与触发维护分别控制。生产构建不读取该控制路径，不提供时间修改或手动清理 API。轮询超时仍采用真实时间，不等待实际七天。

`tests/system/retention_test.go` 经公开 API 验证默认值/范围/确认/原子保存、七天纳秒边界、缓存和扫描详情、搜索与分页总数、伪造客户端时间无效、慢体延迟、调长不延长旧记录、缩短再调长不复活、暂停/移除/停机和重启。物理回收扫描本轮隔离输出，比较幸存原始行和输出空间，不访问私有表；验收数据保留，不自动删除目录或镜像。

保留策略与当前设置一次保存：成功事务的开始时刻是策略生效时刻，和该事务重叠的提交按服务端接收时刻归属，不按内存发布时间或落盘顺序归属。正文上限仍按各提交的接收快照处理。服务端时钟回拨到既有历史边界之前时，调长会被拒绝，避免生成次序不明的规则。

## 总预算与容量恢复

`tests/system/storage_budget_test.go` 经公开宿主接口和隔离输出验证动态预算、严格校验、连续提交、重启、辅助文件增长、降额、混合/整段回收、恢复滞回和主动暂停。运行 `make test-one TEST='TestBudget'`；可控时间仍只在独立验收构建启用。

- 占用为专用目录下每个文件和目录的 `max(按4KiB向上取整的逻辑长度, 实际分配块)`，空文件/目录至少4KiB；包括身份seed、锁、配置、固定容量诊断、未知辅助文件和子目录，不仅统计正文。当前使用JSONL，没有数据库WAL或持久索引；未来添加的持久化仍必须计入。
- 正文准入：`used + 本次最坏增长 + 1MiB配置预留 + (1MiB+4KiB)维护预留 <= budget`。配置原子替换可以使用自己的预留，但临时副本及目录增长仍须在预算内；混合段维护需要完整副本峰值，不能用临时文件绕过限制。
- 降低预算先在旧预算内完成设置文件替换及同步，然后发布新预算。允许已有未过期历史暂时超过新预算，管理状态显示 `over_budget_bytes` 并暂停新采集。超额时需要新增临时空间的配置/混合维护会明确拒绝；仍可提高到足够预算，整段到期回收无需增长空间，不受前面混合段阻塞。超额期间主动暂停写入也可能被拒绝，不能把容量暂停当作持久的主动暂停。
- 仅容量原因自动恢复：空闲至少 **2,891,776字节**，即配置/维护预留 + 512KiB最坏编码行 + 4KiB轮转开销 + 256KiB滞回量。启动、每分钟维护和成功配置事务后重评，必须实际写入并同步诊断文件；真实存储/完整性故障不会被提高预算清除。
- 固定4KiB容量告警文件只保存版本、容量标记和最后发生时刻，异步合并原位写，不存原Key、正文或逐提交日志。正常关闭尽力同步；基础摘要在保留期内跨恢复/重启存在，以最后容量事件起算，调长不延长旧摘要，活动故障不因到期隐藏。运行期计数并非跨重启精确丢失数；完整缺口历史及已阅交互由后续票完善。诊断磁盘写入失败时 `diagnostics_persisted=false`，运行期仍显示真实故障，不保证底层磁盘耗尽时告警可持久化。
- 容量暂停仍通过只读观察判断预期跳过，工具续跑、控制和空输入不变成容量缺口；本应保存的新输入拒收才报告容量原因，不触发宿主桥接warning。模型调用不等待诊断落盘，不改变上游输入。1GiB不承诺完整保存任意流量七天。

## 管理 API

全部动态接口要求**宿主管理鉴权**；匿名、普通客户端 Key 和对象 ID 都不能读取。以下路径由官方 native 管理注册合同托管，并非第二套账号系统。

| 方法 | 路径（前缀 `/v0/management/plugins/request-input-audit`） | 行为 |
| --- | --- | --- |
| GET | `/objects` | 当前内置 Key 与已知/历史对象的联合目录，含 `valid`、`monitored`、`has_history`；目录不可用时 `available=false`，不将缓存对象冒充有效 Key |
| POST | `/monitor` | `{"object_id":"64hex","name":"名称"}`；只添加/启用当前有效内置对象，名称最多256 UTF-8字节 |
| POST | `/rename` | 相同参数；仅更新已知对象的当前名称，不改启用状态，允许无效 Key 的历史对象 |
| POST | `/remove` | `{"object_id":"64hex"}`；停用后续采集，不删除名称或历史 |
| POST | `/pause` | `{"paused":true}` 或 `false`；成功提交不解除既有存储故障；保存失败可能新增存储故障 |
| GET | `/settings` | 当前 `max_text_bytes`（默认65536）、`retention_days`（默认7）、`budget_bytes`（默认1073741824） |
| POST | `/settings` | 可部分更新 `max_text_bytes:1..65536`、`retention_days:1..30`、`budget_bytes:4194304..1073741824`（均为整数）；缩短保留期必须附 `confirm_retention_shortening:true`，否则409；三字段一次原子提交，非法设置400，不接受重复/未知字段或null |
| GET | `/records?offset=0&limit=20&object_id=<64hex>&from=<RFC3339Nano>&to=<RFC3339Nano>&model=<name>&q=<keyword>` | 所有筛选可选、取交集后分页；`offset`为0–10000000，`limit`为1–100，默认20；仅返回短预览及白名单元数据 |
| GET | `/detail?id=<recordID>` | 独立记录正文和白名单元数据 |
| GET | `/status` | 实时观察契约状态和基础采集诊断 |

`from`/`to` 按服务端接收时刻比较，包含两端；RFC3339Nano 可使用时区偏移，同一时刻不因字符串表示不同而被排除。页面输入使用浏览器本地时间，发送时转为 ISO 时间。`model` 精确匹配客户端提交的模型名，最多1024 UTF-8字节，不采用上游实际名。`q` 最多65536字节，按已保存正文的字面子串搜索，仅 ASCII 英文字母忽略大小写，不 trim，不解释正则或搜索语法。空模型/关键词表示不限制；时间参数提供后不得为空。重复的已知参数、非法时间/反向区间或越界参数返回固定400错误，不回显值。关键词仅作为本次查询使用，不持久化查询历史。

保留状态为 `retention: receive_time_expiry_periodic_cleanup`。普通列表、筛选、搜索、计数/分页及详情采用同一到期规则，到期边界即不可查，详情404；`has_history`只代表尚未到期的已保存历史。物理删除可晚于查询隐藏，启动和每分钟周期处理；混合分段保留未到期原始行，临时文件同步后原子替换，失败保留权威段并显示故障。

`tests/system/search_browse_test.go` 在真实宿主中验证交集、筛选后分页、时间端点/时区、预览外匹配、字面字符、状态保留、四协议共用查询、管理权限与搜索日志隐私。运行 `make test-one TEST='TestSearch'`。

公开静态 shell 位于 `/v0/resource/plugins/request-input-audit/index.html`，不包含任何对象、记录或凭据。插件管理注册仍使用官方 `v8.0.27` 的 native `/v0` 合同，不因此修改宿主其他弃用管理功能。

记录只持久化：`id`、`object_id`、`received_at`、`protocol`、`model`、`text`、`truncated` 和为 true 时的 `extraction_incomplete`。列表/详情显式返回两种布尔标记；旧记录缺失不完整字段按 false 读取，不重写历史正文。名称存在对象配置中，查询时关联，不逐条快照保存。配置、状态和新增诊断不能包含原认证 Key、历史、指令、工具数据、模型回复、请求头、完整 URL 或上游账号。

对象及设置配置采用version4 `monitors.json`，包含 `max_text_bytes`、`retention_days`、`budget_bytes` 和最多29条按接收时刻划分的历史期限上限；兼容旧 `map[id]name`、version2及version3配置，旧字段缺省时使用65536字节/7天；缺少持久预算时使用宿主 `budget-bytes`（0表示1GiB）作为初始值，保存后持久预算优先，同目录YAML重载不覆盖它。旧格式在下一次成功修改时升级，不在启动时擅自改写。损坏、未知版本、歧义字段或超限配置会明确停采，不能以管理操作覆盖成空名单。新配置也纳入总目录预算；没有删除已知对象的管理功能。配置保存失败不发布本轮内存变更，但替换文件后的目录同步/扫描失败不能保证磁盘回滚：会停采，重启可能加载已替换值，管理员应核对结果，不把错误响应理解成必然维持旧磁盘配置。

接收快照不持久化。请求只携带固定小决策、上限与随机采集器代际，不保存待提交映射或旧配置副本；观察者替换/采集器重开导致旧快照失效，属于能力故障，不伪装为主动暂停。普通关闭、存储或容量故障仍可能丢失副本，不承诺零丢失。宿主准备/投递桥接的可恢复错误、无效快照及旧实例失效使用实例内有界饱和计数，管理状态合并到 `gaps.observer_unavailable` 并在无活动故障时保留 `observer_bridge_error` 提示（已有存储故障原因优先展示）；后续成功不抹去历史计数，正常重启会重置该运行期计数，不冒充完整可恢复缺口历史。

生命周期系统验收见 `tests/system/lifecycle_test.go`、`tests/system/snapshot_test.go`。慢体测试使用 chunked + `Expect: 100-continue` 屏障，验证配置只影响下一提交；普通小 Content-Length 会被宿主既有日志中间件提前读体，不能用其100响应证明接收快照时序。设计取舍见 [CONTEXT.md](CONTEXT.md)、[ADR-0001](docs/adr/0001-minimal-audit-loop.md) 与 [ADR-0002](docs/adr/0002-admission-snapshot.md)。
