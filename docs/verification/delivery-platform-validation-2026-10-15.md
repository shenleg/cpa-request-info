# 完整交付与目标平台验收

本文记录票13的固定基线交付、同源码实际构建／加载／运行，以及系统行为矩阵。固定审查基点为 `14e56f5e3b6b65a659494ab7f3379f157542b4bc`；工作分支为 `feat/delivery-platform-validation`。macOS arm64、Linux amd64／arm64均完成本票约定验收；票13完成表示此固定基线的功能交付，不代表生产部署、性能或下文未验证项已通过。

## 实现范围

- 三协议共用公开HTTP行为矩阵，分别观察真实上游、已同步详情和精确保存／缺口计数；每个可重复场景同实例两轮，不用Chat结果代替Responses或Anthropic。
- 新增可复现Linux私有64MiB tmpfs控制器及CLI工具测试，显式入口不进入快速检查或自动提权流程。
- 管理员独立构建、加载、鉴权及路由前保存步骤见[本地交付说明](../local-delivery.md)。该文档生成的合成配置已在真实宿主验证：保护接口拒绝匿名／客户端Key，模型路由400，单条原协议／模型输入保存，指令排除，正常停止。
- 新增HTTP矩阵实际发现测试夹具的跨秒响应差异；固定合成上游 `created=1`，没有改变模型宿主的生产时间行为。Responses嵌套执行夹具先失败，补齐原始 `input` 触发后通过，不把未触发内部执行的调用算作覆盖。
- arm64将构建缓存／产物放入独立tmpfs，真实审计数据仍在ext4；首轮发现Go overlay未解析 `.build` 符号链接，兼容性故障夹具实际未生效。构建脚本改用物理路径，并补公开工具回归；不改生产插件以迎合故障夹具。

## 固定基线与同源码

宿主为官方 `v8.0.27`／`bf9341629ef2300ac9d846a2e80fe2756f63a671`，C ABI1／RPC schema6，新增观察契约3。清单固定补丁摘要，普通、时间及兼容性构建均从精确Git对象导出新目录并无fuzz应用；不是把已有工作树当官方基线。

运行源码清单包括第一方Go、脚本、前端、构建和锁文件，不包括Markdown结果、认证配置、审计数据、依赖或产物。三端在执行前及结束时核对同一88文件逐文件SHA256清单，验收时清单摘要为 `eedf56fd7e9494cbb69b5c804b78f4adee22bda740978b4eedd347875d552e11`。文档结果在验收后补写；后续提交门禁仅移除 `scripts/verify-linux-disk.py` 文件末尾一个多余空行，工具CLI 4/4及真实暂存快照检查重新通过，不改变运行逻辑，不将这次格式修正宣称为又一轮三端完整回归。

## 环境与实际命令

| 环境 | 普通运行用户与隔离路径 | 工具链与执行性质 |
| --- | --- | --- |
| macOS arm64 | 当前用户，本仓库 `.test-data/ticket13-*` | Go1.27.2、Node24.18.0，本机原生开发链路 |
| SSH Debian13 Linux amd64 | `cpa-verify` UID1000，`/opt/cpa-linux-ticket13.eN7RAN/source` | 复用原隔离Go1.27.2／Node24.18.0／Chromium，x86_64原生，不以root运行模型宿主 |
| OrbStack Debian12 Linux arm64 | `collin` UID501，`/home/collin/cpa-ticket13-arm64.1RIQU8/source/frozen` | Go1.27.2／Node24.18.0，AArch64原生Linux虚拟机，无用户态架构仿真 |

arm64缺少Go和Chromium运行库，均在本票新目录准备。官方Go／Node归档经官方SHA256校验；浏览器归档核对官方摘要。Debian运行库和文泉驿字体按已验签官方索引校验后仅 `dpkg-deb -x` 私有提取，通过 `LD_LIBRARY_PATH`／Fontconfig供本票浏览器使用，没有安装系统包、执行第三方下载脚本或修改全局配置。实际浏览器启动及中文字体使用已检查；未运行受权限限制的额外 `fc-match` 命令，不影响Chromium实际字体检查。无录像需求，未执行未取得官方摘要的FFmpeg归档。

arm64前缀：

```bash
source /home/collin/cpa-ticket13-arm64.1RIQU8/scripts/environment.sh
source /home/collin/cpa-ticket13-arm64.1RIQU8/scripts/build-tmpfs-environment.sh
source /home/collin/cpa-ticket13-arm64.1RIQU8/scripts/browser-environment.sh
```

amd64使用既有 `/opt/cpa-linux-verify.IZCb2S/tools/` 的Go／Node与隔离HOME缓存。三端执行同一命令：

```bash
make check
make build build-retention-test build-compatibility-test demo
go test ./... -count=1 -json
make tools-test
make browser-run
```

Linux另执行 `make verify-linux-disk SOURCE=<本票源码> OUTPUT=<新空目录> RUN_USER=<普通用户>`，root只控制新私有mount namespace和卷；macOS在Go完整回归内使用hdiutil新镜像。

## 最终结果

| 阶段 | macOS arm64 | Linux amd64 | Linux arm64 |
| --- | --- | --- | --- |
| `make check` | 通过 | 通过 | 通过 |
| 普通／隔离时钟／兼容性宿主及插件原生构建 | 通过 | 通过 | 通过，修复物理路径后overlay实际生效 |
| 完整Go顶层测试 | 156通过 | 154通过 | 154通过 |
| 完整Go子测试 | 219通过 | 219通过 | 219通过 |
| 测试失败 | 0 | 0 | 0 |
| 测试级跳过 | 0 | 2项macOS hdiutil夹具 | 2项macOS hdiutil夹具 |
| 系统包耗时 | 207.365秒 | 179.370秒 | 158.574秒 |
| `make tools-test` | Go工具通过；Node runner 7/7；磁盘CLI 4/4 | 同左 | 同左 |
| 完整浏览器 | 八场景各两轮16/16，ready／cleanup为true | 同左 | 同左，中文字体实际显示 |
| 真实磁盘 | hdiutil ENOSPC／只读重启通过 | 私有tmpfs ENOSPC／EROFS另测通过 | 私有tmpfs ENOSPC／EROFS另测通过 |
| 文档独立加载 | 管理鉴权、路由400仍保存、正常退出通过 | 同左 | 同左 |
| 结束检查 | 源码清单一致，无本轮应用进程 | 源码清单一致，无本轮进程／挂载 | 源码清单一致，无本轮进程／挂载 |

Linux两项skip只表示macOS镜像夹具不适用，不计pass；同一最终源码和正式产物另以私有tmpfs实测两种故障。浏览器三端全部为 `result=passed`，每端16个场景执行，安全浏览场景无外部加载，已查看第二轮故障状态标签截图。首次失败保留在下节，不将其混入最终通过计数。

正式产物SHA256：

| 平台 | 宿主 | 插件 |
| --- | --- | --- |
| macOS arm64 | `02b379e574b6d4605ef18e309207eb0b57efa958eb451d7cd08e42cb3b80b1c3` | `6215f7d002a244b91c597c27864d19fb2d1a01a304f34bb574141634ee8e1744` |
| Linux amd64 | `fd94423d2096af203473e74ff3483c55a106cf4f31500b780c09e461e804ebd6` | `3cfdfd112d7f35ff4d08d5da79ff332580b0a5e19f7267dd1dc61dd32df6bfe8` |
| Linux arm64 | `138aee1f5db238a42607e87e9836f1adba3eb19a793d6a83714d3279d254ce8d` | `34d0db8f4b39ebf1dec4a07a4f6d9c71b33917fcc84af24e55b7636045ee9d07` |

本机有限摘要为 `.test-data/ticket13-{macos,linux-amd64,linux-arm64}-*`。最终浏览器原结果分别为本机 `.test-data/verification-z3eThV/result.json`、amd64源码 `.test-data/verification-creMNV/result.json`、arm64源码 `.test-data/verification-h3GqWG/result.json`。Linux仅回传汇总JSON和状态标签截图，不回传原始Go日志、配置、完整输入或审计目录。

## 首次失败与复测

1. amd64首次完整Go：Responses成功响应对照失败；合成上游未带 `created`，宿主转换采用当前时刻，两个调用跨秒产生差异。固定夹具时间后，相关三项矩阵在macOS与amd64各连续三轮通过，三端最终完整回归通过。
2. arm64首次完整Go：兼容性夹具的管理缺失、熔断、契约不匹配等断言失败，实际呈现正常观察能力。确认overlay源路径含 `.build` 逻辑链接而Go使用物理目录；路径修正与工具回归后重构建再测，不将首次失败记录为平台通过。
3. amd64磁盘CLI首次从SSH默认 `/root` 降权运行导致目录EACCES；切到本票普通用户可访问源码目录后4/4通过，未改变脚本或业务行为。
4. 最终amd64磁盘复测首次把stdout重定向到要求为空的输出目录，控制器正确以 `output_not_empty` 拒绝，未挂载。改为把事件输出写到外部证据目录、新建空目录后两项通过，未删除失败目录。
5. 编辑中的Go工具测试曾短暂有未使用import，快速检查明确失败；完成测试代码后最终三端 `make check` 与完整回归均通过。arm64无 `file` 命令，未安装系统工具；以ELF头、Go目标、CGO实际运行及完整应用执行确认AArch64，不将该辅助命令缺失当作验收通过。

## Linux真实磁盘结果

两架构均在新私有namespace、64MiB tmpfs上执行，分别以UID1000／501运行正式宿主和插件：

- **ENOSPC**：外部填充67,026,944字节，实际写入返回ENOSPC；三次模型HTTP200，上游三次输入完整。旧1条已同步记录保留，失败输入不冒充保存；公开受影响次数，已阅后活动故障仍在。释放本轮填充内容并重启后受损尾行继续 `storage_error` 停采，没有自动截短历史或宣称必定恢复。
- **EROFS**：停止写入者、只读重挂、重新启动；外部创建实际返回EROFS。管理公开 `storage_unavailable`，确认200明确未持久化，故障未被已阅隐藏。恢复可写后由同一存活宿主同步pending历史，再正常重启；旧记录和历史保留，第三次输入成功保存，最终2条。
- 每份结果 `normal_unmount=true`；扫描所有可见进程mountinfo及本轮进程，未遗留挂载／宿主／控制器。只读重启不代表运行中热切，完全不可写时再停机不保证内存告警跨进程保存。

有限结果：本机 `.test-data/ticket13-linux-{amd64,arm64}-disk-{enospc,readonly}-final.json`；最终产物复测远端分别在 `/opt/cpa-linux-ticket13.eN7RAN/disk-final.9a8S6J/` 与 `/home/collin/cpa-ticket13-arm64.1RIQU8/disk-final.T2jOgW/`。前轮结果仍保留在各 `disk-evidence.*`，不删除或覆盖历史结果。合成状态、原始日志和数据保留原环境，不回传。

## 行为矩阵证据入口

| 主题 | 实际系统／浏览器测试入口 |
| --- | --- |
| 默认与管理鉴权、对象身份及生命周期 | `audit_test.go`、`lifecycle_test.go`、`snapshot_test.go`；smoke／lifecycle浏览器 |
| 三协议正常／SSE、回复前保存、上游失败／408、首token后错误 | `TestHTTPProtocolsBeforeReply`，原协议边界与native Anthropic上游专项仍完整回归 |
| 三协议真实客户端deadline／取消／SSE断开 | `TestHTTPProtocolsCancellation`、`TestHTTPProtocolsSSEDisconnect`；上游实际观察context取消，已接收输入保留 |
| 三协议重试／切换／一次提交 | `TestHTTPProtocolsRetries`、`TestHTTPProtocolsNestedExecution`、`TestHTTPProtocolsRepeatedSubmission`；真实两次执行与账号核对，不能仅数回调 |
| 三协议路由／认证／传输失败、存储失败／恢复 | `TestHTTPProtocolsReadFailures`、`TestHTTPProtocolsStorageFailure`；完整JSON传输前缀仍只报read_error |
| WS身份、多次提交、历史补全前来源、工具／控制／取消、下一提交配置 | 全部 `responses_websocket*_test.go` 与responses-websocket浏览器，不用HTTP握手替代 |
| 非文本／未知块／普通文本、64KiB／UTF-8、请求不变 | Chat／Responses／Anthropic边界和执行测试，truncation浏览器及审计开关上游字节对照 |
| 到期／暂停／停机／重启／降预算／辅助文件／满额／自动恢复 | `retention*_test.go`、`storage_budget_test.go`、真实queue与磁盘；retention-settings／storage-budget浏览器 |
| 历史／活动故障／已阅／异常退出／不兼容／桥接 | `gap_history_test.go`、`restart_test.go`、`compatibility_test.go`，failure-status浏览器 |
| 组合筛选／分页／详情／纯文本／外链 | `search_browse_test.go`、search-browse与smoke浏览器；检查无外部加载 |
| 持久化与管理隐私 | `assertChatPrivacy` 递归全部审计输出、辅助文件、白名单字段及诊断；`assertMatrixPrivacy`补管理目录／设置／状态／列表／详情；宿主既有错误warning用开关审计差分确认未新增 |

这些测试存在不等于已运行通过；最终结果以平台表和对应本轮有限摘要为准。408是上游HTTP状态，不冒充传输超时；本票真实超时证据是客户端deadline与上游实际取消，不声称另一个未运行的宿主独立传输超时配置。

## 安全、非保证与授权

应用不加密，文件权限不隔离主机高权限、卷及备份读取者；逻辑到期非安全擦除。普通文本主动粘贴秘密不自动脱敏。native与宿主同进程；常规fail-open不保证进程崩溃隔离、零丢失、零延迟、exactly-once、断电恢复或未落盘重放。不承诺吞吐、搜索延迟或连续高峰七天完整保存。宿主既有执行warning可能包含上游错误正文；审计不复制或新增该输出，不声称宿主日志自动脱敏。

未验证运行中热切只读、其他块设备／文件系统、断电、未来宿主／浏览器版本、性能目标。没有修改真实 `.env`、密钥、证书、CI/CD或生产配置，没有公开发布、部署或推送。验收完成后用户另行明确授权本地提交并合并到 `main`，不包含推送授权。用户批准了本票Linux私有挂载与工具新建临时文件清理；未删除既有数据、验收目录或镜像。

本机已有 `.githooks/pre-commit` 且 `core.hooksPath=.githooks`，未重装或覆盖；远端只初始化隔离Git用于源码清单／工具测试，不创建提交或安装hook。工作区审查按固定基点diff和新增文件同一清单进行Standards／Spec两轴，两轴均0项；结果文档补齐后仅复核新增证据和完成状态，不重复展开源码调查。没有需要人工补做的本票必需验收项。用户随后授权本地提交和合并，提交前已展示21文件变更摘要，并执行真实暂存区快照检查；推送及生产操作未获授权，不执行。
