# 总预算、容量暂停与自动恢复验收

本文记录 ticket 10 的 macOS arm64 与原 SSH Linux amd64 环境的有限证据，不代表完整第一版、异常恢复或其他平台完成。

## 环境与范围

- 日期：2026-10-15。
- 系统：macOS / darwin arm64；Go 1.27.2，Node.js v24.18.0。
- 宿主：清单固定 CLIProxyAPI v8.0.27 / `bf9341629ef2300ac9d846a2e80fe2756f63a671`，使用原观察契约 v3，未修改宿主补丁。
- 实施分支：`feat/storage-budget`；固定工作区审查基点：`2a8778d0e77f741241f9ec4affa5331a9a079ce5`。
- 所有模型调用、Key、上游、时间、磁盘镜像及数据目录均为隔离合成夹具。源码经用户授权上传至自有 `ssh linux-service` 验收环境，只取回有限摘要和状态截图；无生产部署、公开发布或外部通知。

## 命令与结果

| 命令 | 结果 | 有限证据 |
| --- | --- | --- |
| `make verify` | 通过，退出0 | 快速检查、普通/隔离时钟成对构建、`go test ./... -count=1`、工具测试和真实浏览器依次通过。系统包123.162秒；Node工具测试7/7，无跳过。浏览器报告 `.test-data/verification-U89Ebl/result.json`：7场景各2轮，`passed`、`cleanup=true`。 |
| `make check && make build build-retention-test && make test-one TEST='TestBudget\|TestRetention\|TestSettings\|TestReal\|TestStorageFault\|TestQueue'` | 审查修复后通过，退出0 | 受影响系统包47.142秒，含新增重启阈值回归；真实磁盘故障及队列压力再次通过。 |
| `node --check tests/browser/storage-budget.js && node scripts/browser-runner.cjs --output .test-data/ticket10-browser-reviewed` | 异步等待修复后通过，退出0 | 本轮缺口计数增长、标记与诊断落盘共同等待；7场景各2轮，`passed`、`cleanup=true`。 |
| `make check && node scripts/browser-runner.cjs --output .test-data/ticket10-browser-final` | 最终修复版本通过，退出0 | 快速检查通过；最终浏览器报告 `.test-data/ticket10-browser-final/result.json`，7场景各2轮，`passed`、`cleanup=true`。 |

完整验收在实施结束执行一次；审查修复后只重建并复测受影响范围，没有用编译替代真实运行。

## 行为证据

- `TestBudgetSettingsPersist`：默认1GiB；4MiB–1GiB严格整数校验；正文上限/保留期/预算原子保存；持久预算优先于重启YAML初值，非法更新不部分生效。
- `TestBudgetStopsWithoutEvicting`、`TestBudgetExpiryResumesWithGap`：真实连续提交至满，模型路由结果不变，未到期历史不被挤掉，容量拒收不变成桥接故障；正常到期清理后续采，保留仍未到期的容量摘要，重启继续可见。逐次检查目录保守分配，不只累加正文长度。
- `TestBudgetLoweredMaintenanceContinues`：降额到现存占用以下立即明确超额，不删除有效历史；配置临时副本无空间时拒绝且不增长占用；混合段无法复制不阻止后续整段回收，幸存正文不变，已有主动暂停不被解除。
- `TestBudgetAuxiliaryGrowthHysteresis`：嵌套辅助文件、目录和实际分配块均计入公开占用；正常满额可使用预留保存主动暂停；重启保留暂停及基础告警；正文门槛以上、恢复门槛以下仍暂停，释放足够空间才恢复存储可用，主动暂停保持；可控上游逐次收到完整原输入。
- `TestBudgetNeverClearsStorageFault`：已有真实存储故障不被预算不足覆盖，提高预算不能自动解除它。
- `TestBudgetAlertRetention`：容量摘要按最后事件及既有保留规则到期；调长不延长旧摘要，未恢复容量故障到期仍显示。
- `TestBudgetExpectedSkipsStaySkips`：容量暂停时，空输入、assistant尾项和工具续跑仍正常跳过，不增加容量或桥接缺口。
- `TestBudgetRestartKeepsRecoveryThreshold`：4MiB预算、约1.875MiB辅助数据，64KiB NUL文本编码膨胀后触发容量暂停；没有释放空间重启，不能从默认active绕过2,891,776字节恢复门槛。
- 浏览器7场景每轮使用独立标记；新增预算场景每轮70条64KiB记录，验证精确MiB转换、三项确认/取消、非法值零POST、重载、容量与超额文案、历史保留、调高恢复和主动暂停。截图只保留状态标签，不保存正文截图或认证材料。

## 真实故障与隐私边界

- 修复后真实64MiB隔离HFS+卷实测 `ENOSPC`，外部填充65,413,120字节；模型HTTP200，原已保存记录仍为1，失败副本不冒充保存，采集显示`storage_error`。
- 真实只读重挂 `EROFS`，模型HTTP200，观察能力不可用，未伪装正常采集。仅卸载本次卷，不删除镜像。
- 队列压力：本轮1024次真实HTTP/上游调用全部成功且输入校验正确；338条同步保存、686次`queue_full`、最终队列0，全部338条详情摘要核对通过。
- 插件仍无新增持久日志。容量诊断固定4KiB，只保存版本、标记和最后时刻，纳入总目录预算；不能保证磁盘完全不可写时告警持久化。运行期计数重启归零并不表示没有历史缺口，完整缺口列表/已阅和异常恢复仍属后续票。
- 数据目录、宿主日志及镜像保留在忽略目录，不作为提交附件；不上传认证材料、完整输入或日志原文。

## 审查与交付

- 按同一非空文件清单分别执行Standards/Spec工作区审查，覆盖已跟踪差异和三个新代码/测试文件，不使用空的已提交差异冒充无变更。
- Standards：0项finding。
- Spec：2项P2，均已修复并限定复核通过：重启绕过恢复余量、浏览器误把模型响应当异步告警保存完成。前者有修复前真实失败及修复后通过证据；后者按本轮条件等待，不改变生产异步语义。
- 本地既有 `core.hooksPath=.githooks`，pre-commit可执行；未重新安装或修改hook。
- 用户已授权本次本地提交并合并；提交前展示摘要，执行真实暂存区门禁，未授权push或部署。没有需要人工执行的技术验收。

## SSH Linux amd64 补充验收

2026-10-15沿用上一票的 `ssh linux-service`，没有改用Docker或另一台测试机。每票macOS与该Linux环境双端验收已写入 `docs/agents/verification.md`。

| 环境/检查 | 实际结果 |
| --- | --- |
| 系统与测试身份 | Debian 13，Linux `7.0.14-16-pve`，x86_64，glibc 2.41；应用、构建和常规测试均由普通用户 `cpa-verify`（UID1000）执行 |
| 工具链 | 复用上一票隔离工具链Go1.27.2、Node24.18.0和既有Chromium；CGO启用，`GOMAXPROCS=2 GOFLAGS=-p=2 GOTOOLCHAIN=local GOMEMLIMIT=1GiB` |
| 独立目录 | `/opt/cpa-linux-ticket10.zlMPR9`，本票源码、脚本、证据及故障夹具均独立保存，不覆盖上一票结果 |
| 源码一致性 | 98文件快照清单SHA-256：`93272bd9b07617ffd43f53715299aa4d10bbdaec37e711aab57384642450d473`；解包后及验收后逐文件哈希/权限一致，远端源码无修改。待提交72个非Markdown运行/构建/测试文件与该快照一致，验收后仅补充文档和票据状态 |
| `make deps`、`make check` | 通过；锁文件依赖安装不运行npm生命周期脚本 |
| `make build build-retention-test` | 通过；正式宿主和插件均为Linux x86-64 ELF，隔离时钟产物独立构建 |
| `go test ./... -count=1 -json` | 119个顶层测试、160个子测试通过，0失败；系统包122.946秒。全部9个顶层`TestBudget*`通过，包含8个本票新增场景；真实队列满通过 |
| Go测试跳过 | `TestRealENOSPCFailOpen`、`TestRealReadOnlyVolumeFailOpen`的原有夹具依赖macOS `hdiutil`，在Linux明确跳过；对应Linux真实故障由下述独立文件系统夹具补测，未将skip记作pass |
| `make tools-test` | Go工具测试通过，Node工具测试7/7通过 |
| `node scripts/browser-runner.cjs --output .test-data/linux-ticket10-browser` | 七场景各两轮14/14通过，`cleanup=true`；预算场景每轮70条本轮记录，已查看第二轮采集状态截图 |
| 退出检查 | `cpa-verify`无遗留进程，本票挂载无遗留，`git diff --exit-code`通过；合成输出保留，没有删除文件 |

### Linux真实文件系统故障

控制器仅在root创建的私有mount namespace中挂载新建64MiB tmpfs；被测宿主仍以`cpa-verify`运行。独立设备和容量先校验，不填充根文件系统、不格式化物理磁盘、不修改全局挂载、不强制卸载。诊断后只保存审计目录的合成输出，填充数据不取回本机；正常卸载私有挂载。

- **ENOSPC通过**：审计目录之外填充67,026,944字节后实际返回`ENOSPC`；故障前后模型均HTTP200，上游2次完整输入一致，失败副本未保存，仍仅1条已保存记录；管理状态`storage_error`，旧详情正文不变。
- **只读重启通过**：先保存记录并正常停止写入者，原私有文件系统只读重挂，外部写入实测`EROFS`；重启后采集不可用并显示`storage_unavailable`，模型仍HTTP200、上游原输入不变。只读故障期间持久记录逐字节不变；恢复可写并正常重启后，列表及原详情仍正确。
- 首次只读夹具因LXC映射UID被libmount重放为`101000`失败；显式使用容器UID1000后，活动写入者使重挂返回`EBUSY`。这两次均未计为通过，也未强制卸载；最终改为与macOS既有场景一致的“停止写入者→只读重挂→重启”并通过。**不声称运行中热切只读已验证。** 未改动被测源码。

本机有限证据位于 `.test-data/linux-ticket10-approved-oeh2r1uh/`：`validation-summary.json`、`browser-result.json`、两项`disk-*-result.json`和状态标签截图；远端脚本保留于本票 `scripts/`，摘要位于 `evidence/`。原始宿主日志、完整输入、审计目录及填充数据不作为提交附件。

正式宿主SHA-256：`7d9e702326c607080a065ef76bfee85ff9c290807fb53424760ca68c7a6194dd`；正式插件SHA-256：`35d10d976b1649f143120c194d5bb333dedbd2ed1082086774311de957a38eda`。

## 未验证

Linux arm64、其他文件系统的块设备故障、运行中热切只读、断电/异常退出完整恢复及最终三协议失败矩阵未包含在本票验收结论中；tmpfs实际errno验收不等于所有磁盘/文件系统都已验证。1GiB不保证完整保存任意流量七天。
