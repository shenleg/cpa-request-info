# 异步采集故障与异常重启验收

本文记录 ticket 11 的实现与实际检查；不代表 ticket 12 完整缺口交互、ticket 13 全协议/平台交付或生产部署已完成。

## 实现范围

- 沿用64槽、8 MiB白名单副本队列，队列满放弃当次审计，模型请求不等待落盘；同步提取仍有处理开销。
- 固定4 KiB诊断升级v2，保存基础原因最后时刻、运行dirty标记及恢复不确定性。v1迁移不能推断过去正常退出；新空目录不误报；损坏标记停采且保留原文件。旧二进制不支持静默降级。
- 启动采集前同步dirty；正常排空且必要同步成功才clean。异常重启的损失数量/起止为未知，发现时刻单独展示，后续正常重启不提前清历史信号。不保存整请求或逐提交待办来追求精确崩溃计数。
- 启动/维护故障不凭空增加确定丢失计数；已同步记录之后的目录核验故障不将同一记录再算丢失。故障修复不提前清除基本历史。
- 缺观察扩展、版本/协议不兼容、桥接失效有固定安全原因；不回退执行层观察。管理也缺失或整插件熔断时，依靠宿主固定诊断，不绕过熔断调用native管理代码。
- 页面独立显示状态，目录/记录/设置失败分别报告并清除旧呈现，401/403仍隐藏受保护区域；未知损失不显示为零。

## 测试边界与红绿证据

测试边界经实施计划批准：真实模型HTTP＋受保护管理API、真实管理页面＋同一API；管理缺失时才用进程固定启动诊断。故障只在隔离文件系统、进程和测试宿主overlay中控制，没有生产故障API。

- `go test ./tests/system -run '^TestRestartCrashUncertainty$' -count=1 -v`：首次失败“管理状态未提供重启损失是否确定的信息”，实现后通过。
- `go test ./tests/system -run '^TestRestartStorageGapSurvivesRepair$' -count=1 -v`：首次失败“实际未保存提交没有独立运行计数及基本历史信号”，实现后通过。
- `TestRestartQueuedCrash`：强杀前真实公开queued非零，重启报告未知损失；观察到35个待保存副本的定向运行通过，这个数字不是精确崩溃损失数。
- 兼容矩阵逐项先红后绿：缺接口、版本/四协议不齐、无法判断的回调故障、观察者缺失、能力恢复及旧慢体快照、管理缺失、熔断恢复。
- 浏览器首红是objects/records真实失败时首次登录仍隐藏内容；修复后同故障实例两轮通过。额外红测发现读取失败仍留旧详情正文，修复后两轮通过。完整故障场景覆盖真实段权限错误、SIGKILL、锁权限错误使settings不可用、旧呈现清理、历史保留、未知计数/时段、错误Key隐藏及故障中模型200。
- 首次 `make verify` 系统阶段失败于新增 `TestCompatibilityFusedObserver` 缺少固定宿主诊断，未计为通过；修复后重新验收。
- 第二次 `make verify` 揭示正常关闭回调不可用：`TestRestartCleanSession`误报未知退出，`TestRestartCrashUncertainty`的已有原因被覆盖。单独兼容夹具通过不能替代正式宿主回归；修复生命周期最终计数传递后重验。
- 后续全套还发现 `TestCompatibilityFinalCallbackUncertainty` 的测试控制路径在停机时丢失，实际上未注入故障；修正测试侧目录缓存后直接运行兼容矩阵，不使用漏选前缀测试的正则冒充通过。
- Linux首次在线克隆失败，改用既有官方Git对象缓存；随后GNU patch严格应用暴露日志hunk上下文空行和计数不一致。修正为规范unified diff并重算摘要，不放宽fuzz或跳过补丁。
- Linux工具阶段固定管理页面下载长时间停在15%；核实完整进程祖先归属后只停止本轮下载，未完成文件改名保留。复用上一票官方页面缓存并验证同一SHA256，再重跑工具和浏览器；没有放宽页面摘要或修改被测源码。以上失败均未计为验收通过。

## 工作区审查

固定基点 `6ec7a19a0d0315e3df159a4eadaf1d2d99474433`。使用 `git diff <基点> --` 加未跟踪新文件构成同一非空清单，分别执行Standards/Spec，不使用空的已提交差异代替审查。

- Standards发现P2：prepare同步重新探测能力/打开存储会等待审计磁盘；移出模型回调，并验证管理探测阻塞时模型仍可完成。
- Spec发现P2：最后一次桥接失败未经status便正常关闭会遗漏最终累计；关闭前读取最终累计，读取失败只报告不确定性，不虚构一次确定损失。
- 原finding限定复核：Standards 0项，Spec 0项；关闭修复另经正常退出假阳性回归及最终回调真实不可用测试。复核只看原问题与相关差异，没有重新展开全库调查。

## macOS

最终命令：`make verify > .test-data/ticket11-verify.ZzNkaN/macos-verify-passed.log 2>&1`，退出0。

| 阶段 | 实际结果 |
| --- | --- |
| `make check` | 第一方gofmt、vet、测试编译通过 |
| `make build build-retention-test build-compatibility-test` | 固定对象导出、无fuzz补丁、正式及两种隔离宿主/native插件构建通过 |
| `go test ./... -count=1` | 全部通过；系统包151.658秒，工具包3.397秒，包含真实队列满、macOS ENOSPC/只读镜像以及全部重启/兼容性场景 |
| `make tools-test` | Go工具检查通过，Node runner 7/7通过、0跳过 |
| `make browser-run` | 八场景各两轮16/16通过，`cleanup=true`；包含真实故障实例、历史信号及未知损失展示 |
| 状态截图 | 已查看第二轮故障标签截图，明确“丢失数量：未知；起点未知；终点未知”，未截取输入正文 |

最终浏览器有限结果：`.test-data/verification-Vf3hpP/result.json`。此前同份应用的单独工具/浏览器检查也通过，记录在 `.test-data/verification-QPdu06/`；最终结论使用统一验收结果。

## SSH Linux amd64

沿用 `ssh linux-service`，最终独立根目录 `/opt/cpa-linux-ticket11.YOpo2C`；早先失败尝试目录保留，没有覆盖其证据。构建和常规测试使用普通用户 `cpa-verify`，复用 `/opt/cpa-linux-verify.IZCb2S` 内隔离Go/Node工具链及既有Chromium；不更改生产服务、凭据或CI/CD。

真实磁盘夹具由root控制器在私有mount namespace中创建全新64 MiB tmpfs，被测宿主降权运行。只填独立卷，不填根盘；只读采用“停止写入者→只读重挂→重启”，不声称热切只读通过。正常回收本轮挂载和进程，不删除文件，只回传有限结果和状态截图。

源码快照103文件，清单SHA256：`32c58d438fbe03b6bbebe15240fbdbf056ac5b5ece95489f294deaec47c27393`。验收后仅补充Markdown结果与票据，不改运行/构建/测试源码。

真实磁盘结果（用户另行明确授权root控制器后执行）：

- **ENOSPC通过**：独立64 MiB卷实际填充67,026,944字节后返回`ENOSPC`。故障前后模型均HTTP200，上游2次输入完整一致；仅原1条保存记录，失败副本不冒充保存，公开`storage_error`。仅释放本轮填充文件内容、不删除文件后重启，旧详情仍正确，仍保留已发生或不确定损失信号。
- **只读重启通过**：停止写入者后私有卷只读重挂，外部探测实际`EROFS`；模型仍HTTP200且上游输入不变，采集显示`storage_unavailable`。原记录字节不变；恢复可写后正常重启，列表及原详情正确。
- 两项均为普通UID1000宿主，root只控制私有挂载，不以root运行应用。正常卸载，没有force/lazy；合成输出复制到本票目录保留，不回传完整审计数据。

常规验收按`make verify`对应阶段执行，准备依赖、快速检查、原生构建、全Go、工具和浏览器均完成；工具下载失败后只重跑未完成的工具/浏览器，不修改源码。

| 阶段 | 实际结果 |
| --- | --- |
| `make deps`、`make check` | 通过；依赖安装不执行npm生命周期脚本 |
| `make build build-retention-test build-compatibility-test` | GNU严格补丁及Linux amd64正式/隔离产物构建通过，产物为x86-64 ELF |
| `go test ./... -count=1 -json` | 136个顶层、169个子测试通过，0失败；系统包137.310秒；本票所有`TestRestart*`/`TestCompatibility*`通过 |
| Go跳过项 | 原macOS镜像夹具`TestRealENOSPCFailOpen`和`TestRealReadOnlyVolumeFailOpen`明确跳过；Linux对应真实errno与应用行为由上面的私有tmpfs夹具补测，不将skip计入pass |
| `make tools-test`重试 | Go工具检查通过，Node runner 7/7、0失败 |
| `node scripts/browser-runner.cjs --output .test-data/ticket11-browser` | 八场景各两轮16/16通过，`cleanup=true`；已查看第二轮重启未知状态标签截图 |
| 源码与退出 | 快照哈希/权限核验一致，`git diff --exit-code`通过；普通测试用户无遗留进程，本票无遗留挂载 |

正式宿主SHA256：`6b8e69e824872a79c283ebb0bf8b00741856569976bf36b283fba86f552338f4`；插件SHA256：`77b74195dd31a068065b06e41d3b6450205b6160439a64d9e5c99ed9b4350979`。

本机有限证据在`.test-data/ticket11-verify.ZzNkaN/`：`macos-summary.json`、`validation-summary.json`、`browser-result.json`、两项`disk-*-result.json`及`linux-failure-status-2.png`。远端原始日志、完整输入、认证材料、审计目录和镜像不回传、不提交。

## 边界与交付

不提供native进程级崩溃隔离，不恢复内存队列，不承诺exactly-once、零丢失、零延迟或安全擦除；应用层不加密。诊断持久化只能尽力，不能保证磁盘彻底不可写时告警必然落盘。Linux arm64、断电/块设备损坏、运行中热切只读、完整缺口历史/已阅及全协议失败矩阵未作为本票验证结论。

本机既有 `core.hooksPath=.githooks`，pre-commit可执行，未重装或修改hook。用户已明确授权本次本地提交并合并到main；提交前展示变更摘要并执行真实暂存区检查。未授权推送或部署。
