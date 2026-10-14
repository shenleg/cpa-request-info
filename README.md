# CPA 请求输入审计：最小安全闭环

本仓库提供 CLIProxyAPI `v8.0.27` 的最小宿主补丁及真实 C ABI 原生插件。当前实施 [ticket 01](.scratch/request-input-audit/issues/01-minimal-safe-audit-loop.md)，**仅供隔离合成数据验收，不是完整第一版或生产审计交付**。

## 已实现范围

- 固定官方 commit `bf9341629ef2300ac9d846a2e80fe2756f63a671`，保持 C ABI 1 / RPC schema 6，新增独立只读观察契约 1。
- 宿主在内置客户端 Key 鉴权成功、原协议读取后、业务改写及模型路由前观察。不是 usage queue 或执行请求拦截器。
- 宿主随机 seed + HMAC-SHA-256 产生稳定无秘密对象 ID；不同 Key 分开，目录和认证采用同一算法，不依赖配置下标/名称。标识不能登录。
- 默认监控名单为空。管理员选择 ID 并命名；不粘贴原客户端 Key，不改变宿主 Key 配置。
- 仅保存 Chat Completions 最后一个 `user.content` 字符串。末尾 `assistant` / `tool` 不回找；空字符串跳过，空白原样保存。未支持内容和解析失败有诊断，不保存完整请求兜底。
- 正文最多 64 KiB UTF-8 安全前缀，只截断审计副本。请求模型缺失/非字符串时记为未知，不猜测。普通文本里的 URL、秘密、Base64 **不会自动脱敏**。
- 64 条 / 8 MiB 有界副本队列，约 1 MiB 分段 JSONL，写入并同步成功后才允许管理查询；入队不等于保存。
- 默认总预算 1 GiB，合法启动预算 4 MiB–1 GiB；保守核算整个专用目录及辅助文件，预留 1 MiB 配置更新等维护空间。满额停采，不覆盖或删除旧记录。
- 宿主插件菜单、无数据静态 shell、受保护对象/选择/状态/列表/详情 API。正文和名称只按纯文本展示，不渲染 HTML/Markdown，不自动加载外部资源。

## 未完成与风险

尚无其他协议、复杂内容块、WebSocket、完整 SSE/重试/断开矩阵、组合搜索、七天过期清理、动态预算管理、满额自动恢复、异常退出恢复、完整缺口历史或 Linux 双架构运行验收。**已保存输入目前不会自动到期清理。** 配置保留和正常重启的最小检查不等于完成 ticket 02/11。

应用层不加密。专用目录为 `0700`、文件为 `0600`，仍需保护主机、磁盘、快照和备份。没有安全擦除或 native 进程崩溃隔离保证；常规解析/队列/存储错误 fail-open，不代表零丢失、零延迟或吞吐承诺。诊断是有界运行期原因码和已知计数，不是完整可恢复缺口历史。

宿主 `identity.seed` 必须与数据目录一起保护和备份；插件不读取/修改它的内容。已有数据但 seed 缺失、损坏或权限不合规时停采，不静默轮换全部对象。目录祖先不能是符号链接，macOS 使用真实路径，例如 `/private/var/...` 而非 `/var/...`。

官方宿主启动仍可能进行 npm/版本等**既有后台网络请求**；`--local-model` 不表示完全离线。本项目模型验收流量仅去本地可控上游，不使用真实提供商或凭据。

## 本地构建

需要 Go ≥ 1.26、启用 CGO、C 编译器、Git、Python 3、`tar` 和 `patch`。已在 macOS arm64 / Go 1.27.2 验证；仅交叉编译不算目标平台运行通过。

```bash
make build
make check
make test-one TEST=TestSavedLatestInput
make test
```

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

少量自动浏览器验收脚本位于 `tests/browser/smoke.js` 和 `tests/browser/truncation.js`，可通过 Playwright `browser_run_code` 的 `filename` 执行；先导航到 demo 实例地址。前者验证真实菜单、选择、详情、字面 HTML/Markdown、无外部加载及插件不新增凭据存储；后者实际提交超限多字节文本，等待已落盘后验证列表和详情的“已截断”提示、65,535 字节安全前缀、无坏字符且不展示超限尾部。

## 管理 API

全部动态接口要求**宿主管理鉴权**；匿名、普通客户端 Key 和对象 ID 都不能读取。以下路径由官方 native 管理注册合同托管，并非第二套账号系统。

| 方法 | 路径（前缀 `/v0/management/plugins/request-input-audit`） | 行为 |
| --- | --- | --- |
| GET | `/objects` | 当前内置 Key 的无秘密对象与选择状态 |
| POST | `/monitor` | `{"object_id":"64hex","name":"名称"}`；名称最多 256 UTF-8 字节 |
| GET | `/records?offset=0&limit=20` | 已保存记录短预览，`limit` 为 1–100 |
| GET | `/detail?id=<recordID>` | 独立记录正文和白名单元数据 |
| GET | `/status` | 实时观察契约状态和基础采集诊断 |

公开静态 shell 位于 `/v0/resource/plugins/request-input-audit/index.html`，不包含任何对象、记录或凭据。插件管理注册仍使用官方 `v8.0.27` 的 native `/v0` 合同，不因此修改宿主其他弃用管理功能。

记录只持久化：`id`、`object_id`、`received_at`、`protocol`、`model`、`text`、`truncated`。名称存在对象配置中，查询时关联，不逐条快照保存。配置、状态和新增诊断不能包含原认证 Key、历史、指令、工具数据、模型回复、请求头、完整 URL 或上游账号。

设计取舍见 [CONTEXT.md](CONTEXT.md) 和 [ADR-0001](docs/adr/0001-minimal-audit-loop.md)。
