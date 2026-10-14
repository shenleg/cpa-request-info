# CPA 请求输入审计：最小安全闭环

本仓库提供 CLIProxyAPI `v8.0.27` 的最小宿主补丁及真实 C ABI 原生插件。当前覆盖 [ticket 01](.scratch/request-input-audit/issues/01-minimal-safe-audit-loop.md)、[ticket 02](.scratch/request-input-audit/issues/02-monitoring-lifecycle.md) 与 [ticket 03](.scratch/request-input-audit/issues/03-chat-input-boundaries.md) 的本地验收范围，**仅供隔离合成数据验收，不是完整第一版或生产审计交付**。

## 已实现范围

- 固定官方 commit `bf9341629ef2300ac9d846a2e80fe2756f63a671`，保持 C ABI 1 / RPC schema 6，新增独立只读观察契约 2；旧契约不静默降级。
- 宿主在最终内置客户端 Key 鉴权后、读体前同步冻结本次提交的无秘密身份与采集决策；读体后、业务改写及模型路由前只读观察，不重新按名单或主动暂停决定资格。不是 usage queue 或执行请求拦截器。
- 宿主随机 seed + HMAC-SHA-256 产生稳定无秘密对象 ID；不同 Key 分开，目录和认证采用同一算法，不依赖配置下标/名称。标识不能登录。
- 默认监控名单为空。管理员选择 ID 并命名、仅改名、移除或重新加入；名称仅在对象目录保存，旧/新记录显示当前名称。同 Key 重返名单沿用历史，不同 Key 即使同名也独立。不粘贴原客户端 Key，不改变宿主 Key 配置。
- 全局主动暂停/恢复单独持久化，不解除存储故障、不把预期跳过记为缺口；后续变更不取消已接收/入队副本。对象名称、停用及暂停状态正常重启后保留。
- 当前有效 Key、已移除但有历史及 Key 已无效的对象具有独立状态；管理员可按对象筛选历史，查询不要求原 Key 仍有效。已知对象总数最多1024（包含停用对象），名称最多256 UTF-8字节。
- 仅判断 Chat Completions 最后一条显式消息；连续 `user` 只取最后一条，不跨消息拼接。末尾 `assistant` / `tool` / `function` / `system` / `developer` 正常跳过，不回找旧用户发言。
- 选中 `user.content` 支持字符串或内容数组：`text` 原样保存；`image_url`、`input_audio`、`video_url`、`file` 的对应非 null 对象载荷仅生成 `[图片]`、`[音频]`、`[视频]`、`[文件]`。按原顺序直接组合，不保存附件载荷、文件名、URL、Base64 或引用标识。`tool_call` / `tool_use` / `tool_result` 整块排除，不递归提取嵌套内容。
- 未知/畸形块不序列化：安全部分保存并标记 `extraction_incomplete`，每次提交增加一次 `unsupported_content` 缺口；完全无法安全提取则只报缺口。缺失字段、重复键和大小写别名不静默回退为完整输入，也不保存整请求兜底。空字符串、显式 `null`、空数组、仅合法空文本或工具块正常跳过，空白文本不 trim。
- 文本与占位符合计最多 64 KiB UTF-8 安全前缀，不含元数据；恰好上限不误报，首次字符越界后不再拼后续短块。只截断审计副本，列表和详情分别显示截断/提取不完整，两种状态可同时存在。请求模型缺失/非字符串时记为未知，不猜测。普通文本里的 URL、秘密、Base64 **不会自动脱敏**。
- 64 条 / 8 MiB 有界副本队列，约 1 MiB 分段 JSONL，写入并同步成功后才允许管理查询；入队不等于保存。
- 默认总预算 1 GiB，合法启动预算 4 MiB–1 GiB；保守核算整个专用目录及辅助文件，预留 1 MiB 配置更新等维护空间。满额停采，不覆盖或删除旧记录。
- 宿主插件菜单、无数据静态 shell、受保护对象/选择/状态/列表/详情 API。正文和名称只按纯文本展示，不渲染 HTML/Markdown，不自动加载外部资源。
- 最近查询页最多保留 100 条记录的分段/偏移位置提示，详情只读取对应已同步行并重新校验；不缓存正文、不建立全量 ID 索引、不新增持久化文件。未命中时仍可扫描查询。

## 未完成与风险

尚无其他协议、上述白名单以外的内容类型、WebSocket、完整 SSE/重试/断开矩阵、组合搜索、七天过期清理、动态预算管理、满额自动恢复、异常退出恢复、完整缺口历史或 Linux 双架构运行验收。**已保存输入目前不会自动到期清理。** 当前只实现最小闭环、配置生命周期与 Chat 输入边界；暂停、移除不会改变接收时间或删除历史，保留期限/清理由后续票完成，正常重启不等于异常恢复 ticket 11 已完成。

应用层不加密。专用目录为 `0700`、文件为 `0600`，仍需保护主机、磁盘、快照和备份。没有安全擦除或 native 进程崩溃隔离保证；常规解析/队列/存储错误 fail-open，不代表零丢失、零延迟或吞吐承诺。诊断是有界运行期原因码和已知计数，不是完整可恢复缺口历史。

宿主 `identity.seed` 必须与数据目录一起保护和备份；插件不读取/修改它的内容。已有数据但 seed 缺失、损坏或权限不合规时停采，不静默轮换全部对象。目录祖先不能是符号链接，macOS 使用真实路径，例如 `/private/var/...` 而非 `/var/...`。

官方宿主启动仍可能进行 npm/版本等**既有后台网络请求**；`--local-model` 不表示完全离线。本项目模型验收流量仅去本地可控上游，不使用真实提供商或凭据。

## 本地构建

需要 Go ≥ 1.26、启用 CGO、C 编译器、Git、Python 3、`tar` 和 `patch`。已在 macOS arm64 / Go 1.27.2 验证；仅交叉编译不算目标平台运行通过。

```bash
make deps              # 明确准备 Go/Node 依赖；不执行 npm 生命周期脚本
make browser-install   # 明确下载固定 Playwright 所需 Chromium
make check             # 快速格式、vet 与编译检查，不启动宿主
make hooks-install     # 只给本仓库安装暂存快照检查；遇已有 hook 冲突即停止
make test-one TEST=TestSavedLatestInput
make verify            # 全部系统/工具/浏览器验收；正常停止进程，保留证据
```

快速检查离线执行，缺少依赖会失败，需先 `make deps`。pre-commit 通过 `git write-tree` 导出真实 index 的独立快照，避免部分暂存时工作树的未暂存修复掩盖坏代码；它不自动格式化、不修改 index、不下载依赖、不跑压力或磁盘故障测试。导出快照保留在 `.test-data/`。可显式运行 `make check-staged`。

本项目仅执行本地验收，不配置或触发远端 CI。统一入口为 `make verify`；本地通过仅表示当前机器上的实际结果，不代表未运行平台已验证。

构建脚本核实 Git commit 和补丁摘要，从精确 Git 对象导出**全新隔离目录**，应用无 fuzz 补丁，避免把旧工作树改动混入构建。不执行下载的安装脚本，不自动删除目录。

产物：

```text
.build/bin/cliproxyapi
.build/plugins/request-input-audit.dylib  # macOS；Linux 为 .so，运行尚未验收
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

镜像测试不使用 `sudo`、不格式化物理磁盘、不强制卸载、不修改全局磁盘设置。只卸载本次新镜像，不删除镜像、填充文件或验收目录。macOS 镜像单项失败会明确报错；其他平台此镜像夹具明确跳过，不能据此宣称 Linux 故障路径已验收。

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

自动验收只需 `make browser`：构建固定宿主/插件及 demo、核对管理页面、启动真实实例、运行三个场景各两轮并停止进程，不依赖 MCP。已有构建产物时可用 `make browser-run`。启动 JSON 只有健康检查通过后才发 `ready`，失败/超时有明确退出；runner 不保存子进程 stderr 原文或 Playwright 可能含凭据的调用日志，只报告固定阶段/原因和本项目场景行号。

场景位于 `tests/browser/smoke.js`、`tests/browser/truncation.js`、`tests/browser/lifecycle.js`。分别验证菜单与纯文本安全展示、65,535字节截断标签/提取不完整/双状态及可见缺口，以及页面改名、移除后历史筛选、同Key重新加入、主动暂停/恢复。同一宿主连续两轮且每次使用唯一标记/记录 ID，旧数据不能冒充本轮通过。有限证据保存在 `.test-data/verification-*/result.json` 和状态标签截图；没有成功验收的结果不会报告为通过。

浏览器环境使用精确版本 Playwright 和明确的 `zh-CN` locale，匹配固定管理前端；不宣称所有浏览器/语言/未来前端已验证。验收责任与工作区审查的项目约定见 `docs/agents/verification.md`；个人技能文件不修改。

## Chat 输入边界验收

`tests/system/chat_boundaries_test.go` 使用真实 HTTP、受保护列表/详情/状态与可控上游，覆盖末尾角色、顺序混合块、工具嵌套排除、未知/歧义块、空白、64 KiB 合计及跨块多字节前缀。预期为独立夹具字面值；未知或应跳过案例以随后的唯一提交落盘作为屏障，不依赖固定 sleep。重启验证旧记录缺字段按 false 读取，以及新记录截断/不完整状态保留。

专项检查扫描整个隔离审计目录和宿主/插件诊断输出，检查字段白名单及禁止标记，不只检查管理 API 没返回。开启/关闭审计的可控上游字节逐次相等；宿主本来无法转发的畸形请求比较原响应。查询正文只能包含实际保存前缀，本票不提前实现后续搜索接口。

```bash
make build
make test-one TEST='TestChat'
```

## 管理 API

全部动态接口要求**宿主管理鉴权**；匿名、普通客户端 Key 和对象 ID 都不能读取。以下路径由官方 native 管理注册合同托管，并非第二套账号系统。

| 方法 | 路径（前缀 `/v0/management/plugins/request-input-audit`） | 行为 |
| --- | --- | --- |
| GET | `/objects` | 当前内置 Key 与已知/历史对象的联合目录，含 `valid`、`monitored`、`has_history`；目录不可用时 `available=false`，不将缓存对象冒充有效 Key |
| POST | `/monitor` | `{"object_id":"64hex","name":"名称"}`；只添加/启用当前有效内置对象，名称最多256 UTF-8字节 |
| POST | `/rename` | 相同参数；仅更新已知对象的当前名称，不改启用状态，允许无效 Key 的历史对象 |
| POST | `/remove` | `{"object_id":"64hex"}`；停用后续采集，不删除名称或历史 |
| POST | `/pause` | `{"paused":true}` 或 `false`；成功提交不解除既有存储故障；保存失败可能新增存储故障 |
| GET | `/records?offset=0&limit=20&object_id=<64hex>` | 可选对象精确筛选；分页前过滤，总数仅统计匹配记录，`limit`为1–100 |
| GET | `/detail?id=<recordID>` | 独立记录正文和白名单元数据 |
| GET | `/status` | 实时观察契约状态和基础采集诊断 |

公开静态 shell 位于 `/v0/resource/plugins/request-input-audit/index.html`，不包含任何对象、记录或凭据。插件管理注册仍使用官方 `v8.0.27` 的 native `/v0` 合同，不因此修改宿主其他弃用管理功能。

记录只持久化：`id`、`object_id`、`received_at`、`protocol`、`model`、`text`、`truncated` 和为 true 时的 `extraction_incomplete`。列表/详情显式返回两种布尔标记；旧记录缺失不完整字段按 false 读取，不重写历史正文。名称存在对象配置中，查询时关联，不逐条快照保存。配置、状态和新增诊断不能包含原认证 Key、历史、指令、工具数据、模型回复、请求头、完整 URL 或上游账号。

对象配置采用版本化 `monitors.json`，兼容旧 `map[id]name`；旧格式在下一次成功修改时升级，不在启动时擅自改写。损坏、未知版本、歧义字段或超限配置会明确停采，不能以管理操作覆盖成空名单。新配置也纳入总目录预算；没有删除已知对象的管理功能。配置保存失败不发布本轮内存变更，但替换文件后的目录同步/扫描失败不能保证磁盘回滚：会停采，重启可能加载已替换值，管理员应核对结果，不把错误响应理解成必然维持旧磁盘配置。

接收快照不持久化。请求只携带固定小决策、上限与随机采集器代际，不保存待提交映射或旧配置副本；观察者替换/采集器重开导致旧快照失效，属于能力故障，不伪装为主动暂停。普通关闭、存储或容量故障仍可能丢失副本，不承诺零丢失。宿主准备/投递桥接的可恢复错误、无效快照及旧实例失效使用实例内有界饱和计数，管理状态合并到 `gaps.observer_unavailable` 并在无活动故障时保留 `observer_bridge_error` 提示（已有存储故障原因优先展示）；后续成功不抹去历史计数，正常重启会重置该运行期计数，不冒充完整可恢复缺口历史。

生命周期系统验收见 `tests/system/lifecycle_test.go`、`tests/system/snapshot_test.go`。慢体测试使用 chunked + `Expect: 100-continue` 屏障，验证配置只影响下一提交；普通小 Content-Length 会被宿主既有日志中间件提前读体，不能用其100响应证明接收快照时序。设计取舍见 [CONTEXT.md](CONTEXT.md)、[ADR-0001](docs/adr/0001-minimal-audit-loop.md) 与 [ADR-0002](docs/adr/0002-admission-snapshot.md)。
