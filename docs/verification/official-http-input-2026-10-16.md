# 原版 CPA HTTP 精简版交付验收

本文记录 2026-10-16 对正式 `0.2.0` HTTP 执行输入记录插件的实际构建、系统、浏览器、容器和存储故障结果。它不是此前探针实验或旧补丁版验收的复述，也不代表已部署用户生产 CPA。

## 结论

正式插件已改为官方 `request_interceptor` Before 与管理能力，**不再依赖宿主补丁；已在原版官方 Docker 镜像中仅挂载 `.so` 成功运行**。三协议 HTTP/SSE 输入、对象发现及选择、查询、预算、保留期和诊断通过本轮验收。WebSocket 和插件内部嵌套执行明确不采集，路由前失败不可见，不宣称完整入站审计。

当前工作分支为 `experiment/official-plugin-probe`，未提交、推送、合并或生产部署。保留之前未提交探针；固定 Git 审查基点为 `6711c1cb7a08c432e8690e66c87a3a74ad752480`，此前未提交文件摘要位于 `.test-data/official-http-baseline._gtl_pg3/baseline.json`。

## 固定输入与源码一致性

- 官方宿主：CLIProxyAPI `v8.0.27` / `bf9341629ef2300ac9d846a2e80fe2756f63a671`，C ABI 1 / RPC schema 6。
- 开发构建从精确 Git 对象导出新目录，不使用有改动的缓存工作树，不应用 patch 或宿主 overlay。`host/manifest.json` 当前 patches 为空。
- `make build-plugin` 只构建正式动态库，不获取或重编宿主；普通库不含测试时钟入口。
- 本机和隔离 Linux 最终对照 106 个代码、脚本、模板、配置和依赖入口的 SHA-256，一致。Linux 磁盘驱动从旧 GET/完整目录调整为真实 HTTP 发现及 POST 后另行同步、语法/工具验证和实际故障验收，不改变插件运行时代码。
- 21 个插件运行时代码/模板/依赖文件的规范化摘要为 `76d5dca601da133944007d36668a59f812d4d10774923f2edb29de4bdf9064e2`。容器所加载库的对应源码与最终版本一致。

## 双端默认验收

| 项目 | macOS arm64，Go 1.27.2 | Debian Linux amd64，Go 1.27.2 |
| --- | --- | --- |
| `make check`：格式、vet、默认测试编译 | 通过 | 通过 |
| 官方宿主、正式插件、独立 audittest 插件原生构建 | 通过 | 通过 |
| `make verify` 的 `go test ./... -count=1` | 全部包通过，系统包 156.703 秒 | 全部包通过，系统包 169.430 秒 |
| 相关 Go 工具测试 | 通过 | 通过 |
| Node runner 工具测试 | 7/7 | 7/7 |
| Linux 磁盘驱动参数/安全工具测试 | 4/4 | 4/4 |
| 真实管理页面浏览器：8 场景各两轮 | 16/16，cleanup=true | 16/16，cleanup=true |
| 独立 `make verify-official-probe` | 通过 | 通过 |

Linux 默认 Go 测试中的两项 hdiutil 专用场景按平台不适用跳过；它们不算 Linux 故障验收证据，Linux 已另跑下述私有 tmpfs 真实 ENOSPC/EROFS。macOS 默认系统测试实际运行这两项。没有测试文件的包输出不算行为测试。

正式验收主要执行：

```bash
make verify
make verify-official-probe
```

Linux 在既有隔离根下创建全新源码/结果目录，使用普通 `cpa-verify`，沿用 Go、Node、浏览器依赖与只读官方源码缓存，不修改生产服务或系统依赖。构建环境限定 `GOMAXPROCS=2 GOFLAGS=-p=2 GOMEMLIMIT=1GiB GOTOOLCHAIN=local`。测试进程不继承真实云凭据、上游账号或 `.env`。

### 本轮实际覆盖

- Chat Completions、Responses HTTP、Anthropic Messages，普通/SSE、成功、上游失败、真实客户端取消/断开，以及内部重试/账号切换与同文重新提交。
- `Metadata.source=plugin_host_model_callback` 的内部执行排除；不按 TraceID 或正文推断去重。
- 普通 Responses WS 和 Codex duplex 真实调用成功，但正式插件没有新增记录或错误缺口。浏览器原 `responses-websocket` 场景现明确是 `websocket-not-captured` 负例，不作为 WS 支持证据。
- 目录初始为空、首次已路由请求只发现不保存、管理员选择后记录后续输入、未选对象重启丢失、已选对象与新种子重启恢复；目录上限和换目录代际隔离。
- Before 当前配置/时间、DD 改写后的当前模型；未知模型的路由前请求不被伪造为已观察事件。
- 新 `caller-scope-v1.seed` 的持锁空目录初始化、权限和同步、身份稳定、原 scope 不保存、旧身份目录拒绝且不覆盖，种子计入预算。
- 正文白名单、末尾用户选择、工具续跑排除、附件占位符、UTF-8 截断和未知内容标记。
- POST JSON 列表/搜索、分页/多条件交集、详情、关键词不进入 URL；管理 Key 鉴权、匿名和客户端 Key 拒绝、纯文本安全、无外部资源加载、桌面/窄屏与明暗主题布局。
- 保留期、配置缩短确认、预算满额/恢复、队列压力、存储故障、缺口历史与已阅、异常退出后的未知损失信号。

## 官方容器实际加载

直接使用已发布的 `eceasy/cli-proxy-api:v8.0.27`，未构建自定义 CPA 镜像，也未替换镜像内宿主。

- 架构：Linux amd64；镜像 ID `sha256:a4c50168380e8b06a52e22535e59746b10b517c75b34a7aa8338c9f30ca8c10e`。
- 管理响应确认版本 `v8.0.27`、commit `bf93416`，与固定官方 commit 前缀一致。
- 运行 UID/GID `1000:1000`；正式 `.so` 只读挂载，合成配置和新状态目录可写；仅回环地址监听，本地合成上游。
- 库 SHA-256：`eb3092361cdd0f204c7541dd8ac8573faa8210450b775f116fdf5c859553c01a`；该原生库最大 glibc 符号需求为 2.34，实际已在官方 bookworm 运行层加载。
- 发现请求不保存；命名启用后连续两条输入实际保存。匿名/普通客户端 Key 不能读取管理数据；POST 查询关键词未进入容器日志，详情没有系统/历史正文。
- 正常重启后两条历史、已监控对象和种子均保持，种子没有轮换。
- 本轮容器已停止并保留，未删除容器、镜像、卷或数据。

Linux 直接拉取大层曾停滞；改为本机拉取同一官方标签的 amd64 镜像，经 `docker save/load` 传入隔离 Linux。没有修改镜像内容。首次查询无 v 前缀的 `8.0.27` 标签不存在，最终使用实际发布的 `v8.0.27`，不回退到 latest。

可安装的已验证 Linux amd64 库保留于：

`.build/distribution/http-execution-0.2.0-linux-amd64.94Rl4a/request-input-audit.so`

同目录附 `SHA256SUMS` 与 `verification.json`。它是本地交付产物，未上传发布；不能据此宣称 Linux arm64、Alpine/musl 或其他宿主版本已验证。

## 真实 Linux 存储故障

使用正式原版宿主和同版本正式插件，root 控制器只创建新的私有 mount namespace 和固定 64 MiB tmpfs；应用始终以 UID 1000 运行。源码及输出路径显式指定，所有挂载正常卸载。

```bash
bash scripts/verify-linux-disk.sh \
  --source /opt/cpa-linux-verify.IZCb2S/official-http-final.D3fNNo/source \
  --output /opt/cpa-linux-verify.IZCb2S/official-http-final.D3fNNo/disk-evidence \
  --user cpa-verify
```

两项均通过：

- **ENOSPC**：外部填充实际得到 ENOSPC；发现、正常记录、故障期间、修复后四次模型请求均 200，上游正文未改变。故障不冒充保存，旧详情保持，storage_error/受影响次数/已阅及恢复历史可见。此次保留了损坏尾部，释放空间后仍明确停采，**不静默截断修复，也不宣称所有 ENOSPC 都会自动恢复**。
- **EROFS**：停止写入者后真实只读重挂，外部写入实际得到 EROFS；只读环境重启明确报告不可采集、内存告警没有伪称已持久化，模型调用仍 200。重新可写后在同实例移交告警并同步，再正常重启，历史与种子保留，新输入恢复保存。本项不是运行中热切只读验证，也不保证完全不可写期间直接退出仍能持久保存告警。

## 测试迁移与工作区审查

旧补丁观察专用用例保留为明确 `legacy_patched` 历史文件，不进入新默认产品范围，不以批量 skip 宣称旧保证仍成立。对应的新默认覆盖在 `tests/system/official_http_test.go` 及仍适用的 HTTP、存储、查询和故障测试中。

主要历史组包括：私有 observer/capabilities/桥接故障、原帧 WS/duplex、读体前快照/慢体配置与保留时刻、路由前读体失败、失效 Key 的完整宿主目录、工具业务改写前观察及四协议搜索。需要重现旧版时使用原固定提交的独立源码目录，不用当前精简插件运行旧合同。

固定基点审查涵盖 `git diff <基点> --` 及所有新文件，保留探针旧差异；重点代码检查没有发现已确认的实质性缺陷。`git diff --check` 通过。生产采集无 `.env`/凭据/CI 修改，没有自动提交或推送。

## 有限证据与未验证项

- 最终源码与有限 Linux 结果：`.test-data/official-http-final.ck0qil8l/`；其中 `source-hashes-final.json` 对照 106 个入口，`linux/` 保存 verify 输出、两项磁盘 JSON、browser/probe/container JSON；不含宿主请求日志或完整输入。
- macOS 最终浏览器：`.test-data/verification-XtQmXf/result.json`，16 场景及 cleanup=true；同目录保留有限状态截图。
- Linux 最终源码：`/opt/cpa-linux-verify.IZCb2S/official-http-final.D3fNNo/source/`；浏览器 `.test-data/verification-CXUxLg/result.json`；探针 `.build/official-probe.Drv3sS/`；驱动证据位于兄弟 `evidence/` 与 `disk-evidence/`。
- Linux 容器/库来源：`/opt/cpa-linux-verify.IZCb2S/official-http.ixXif8/`，与最终插件 21 个运行时文件摘要相同；仅有限摘要取回本机。
- 进程、挂载与本轮容器正常停止；文件和停止后的容器保留，不自动清理。

本轮未验证 Linux arm64 正式精简版、其他官方版本/fork、第三方认证来源、生产负载/性能、断电、其他文件系统及运行中热切只读。不提供 native 崩溃隔离、零丢失、exactly-once、内存队列重放或不可见入站请求总数。正文应用层明文、普通文本秘密不自动脱敏，逻辑到期不等于安全擦除。
