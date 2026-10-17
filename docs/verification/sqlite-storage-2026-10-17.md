# SQLite 存储改造验收

## 范围与授权

- 固定审查基点：`791c807616b65ec706ace55fd98e6788f26d78cd`；开发分支 `feat/sqlite-storage`。
- 规范：`.scratch/sqlite-storage/spec.md`；记录存储 SQLite v1，插件版本 `0.3.0`。
- 用户后续明确只授权 Linux 执行。macOS 本轮不下载驱动、不编译、不测试，只编辑源码；不得引用旧 macOS 通过结果。
- 不迁移旧历史、监控或设置，不删除旧目录，不生产安装，不提交或推送。

## Linux 隔离环境

- SSH：既有 `linux-service`；应用/构建用户 `cpa-verify`（UID/GID 1000），不以 root 运行模型宿主。
- 新隔离目录：`/opt/cpa-linux-verify.IZCb2S/sqlite-storage.VJ0h2h`，源码与有限结果分别保留。
- 实测工具链：Go `go1.27.2 linux/amd64`、Node `v24.18.0`；既有 GCC 和 Docker。
- 官方容器镜像实测存在：`eceasy/cli-proxy-api:v8.0.27`，ID `sha256:a4c50168380e8b06a52e22535e59746b10b517c75b34a7aa8338c9f30ca8c10e`，Linux amd64。镜像存在不等于本轮容器验收通过。

## 依赖与日志空间依据

- 已在 Linux 下载 `github.com/mattn/go-sqlite3 v1.14.52`，采用内嵌 SQLite。官方 Go proxy 连接超时，改用 `GOPROXY=https://goproxy.cn` 成功下载并由 Go 校验模块摘要。
- 模块摘要 `h1:wVbm2Qnf4OXkqhBTSPuCRZDRnxfbVrrmiCEroVdog8U=`；go.mod 摘要 `h1:6JTjA44L93a0QCyJef5YvlPoKXntQPjzWv5gtm9sB6w=`。
- 锁定 amalgamation `sqlite3-binding.c`：`MAX_SECTOR_SIZE=65536`（60051）；回滚页记录为页号4字节、4096字节页面及4字节校验（65698–65715）；`pInJournal` 位图限制每个原页面只写一次（65771–65776）。
- `syncJournal(...,1)` 的新增日志头发生于 pagerStress（64346），禁用 `SPILLFLAG_OFF` 对应缓存溢写后不执行该路径（64316 起）；提交为 `syncJournal(...,0)`（66294）。无 ATTACH / savepoint；日志长度按 `round4KiB(128KiB+M*4104)` 保守预留，128KiB 覆盖首头和对齐。
- PERSIST 提交通过 `zeroJournalHdr()` 清零前28字节并同步（61035–61050）。应用仅在无事务且日志头已失效时调整尾部；hot journal 先由 SQLite 恢复。
- 上述是锁定版本的逻辑文件长度依据；实际 `stat.Blocks`、事务峰值、回收和崩溃场景仍须执行本轮测试，不据源码推导冒充验证。

## 本轮执行结果

- Linux 首轮核心测试编译成功，运行失败：新 seed 初始化中的 `err` 被局部声明遮蔽，外层仍持有 ENOENT，导致身份拒绝；已修复并继续复测。
- 后续核心运行暴露新空库连接/建表期间 SQLite 删除重建无效 journal，应用持有旧 inode 后拒绝访问；已修复受控初始化/恢复处理并复测通过，不放松运行期文件替换核验。
- 已核证 Unix VFS 的真实 ENOSPC 也会清零 `lastErrno` 后返回 `SQLITE_FULL`（锁定 amalgamation 43909–43914），不能凭 `SystemErrno==0` 判断内部限页。实现改为 SQL 前申请完整增长窗口，容量不足在 SQL 前拒收，SQL 执行或提交 FULL 一律按真实存储故障处理。
- Linux `go test ./tests/system -run '^$'`：编译通过，不是系统业务运行通过。
- Linux `node --check scripts/browser-runner.cjs`：通过。
- Linux `node --test scripts/verify-linux-disk.test.cjs`：5 项通过，含真实 SQLite 快照、NUL、缺库/日志、损坏和错误 schema。
- Linux `make build build-retention-test demo`：首轮构建通过，官方固定源码新导出；之后核心仍有修改，不能安装该中间产物，最终须重建。
- 用户已明确授权本轮 Linux root 私有 mount namespace + 64MiB tmpfs 故障专项，应用仍以 `cpa-verify` 运行；不操作物理磁盘、生产数据或已有挂载。

### 核心与正式环境阶段结果

- Linux `go test ./internal/audit ./internal/native -count=1`、`go test -tags audittest ./internal/audit -count=1`：修复后通过。
- Linux `go test ./internal/audit -run TestSQLite -count=1 -v`：通过；覆盖 NUL/Unicode/%/_ 字面搜索、重复 ID 最新提交、事务回滚/索引、七种损坏或不匹配状态、实际物理回收、预算恢复及子进程异常退出后的 hot journal 恢复。
- 本轮 `make verify` 的快速检查及正式/时钟构建通过；完整系统、工具、浏览器仍在执行，不能提前报告整体通过。
- 官方容器独立验收通过，目录 `container-first/`：固定镜像以上述 ID 运行，UID/GID `1000:1000`，核实正式库加载、默认不监控、发现/选择后采集、管理鉴权、搜索/详情、新 SQLite 文件和重启后同一记录恢复。已正常停止本轮容器，保留现场。
- 该轮容器插件 SHA-256：`24ee8e1fe04d378c1374b078d4575447d1a5195e8df8c513d82aa1b96724190d`；若后续代码修复改变正式库，必须重建复验，不直接将此中间摘要当最终产物。
- Linux `make verify-linux-disk SOURCE=<本轮source> OUTPUT=<新空目录> RUN_USER=cpa-verify`：ENOSPC、只读两个模式通过，证据目录 `disk-first.3Ume4i/`。使用私有 mount namespace、固定64MiB tmpfs，宿主 UID1000，正常解除挂载。
- ENOSPC 实际外部填充 `65773568` 字节，运行期明确 `storage_error`；四次模型调用均 HTTP200，上游输入未变，旧两条记录完整，缺口/已阅状态可见。释放空间并正常重启后可以恢复采集；SQLite 回滚成功不等于内存队列无损。
- 只读测试范围为**停止写入后只读重挂并重启**，不是运行中热切只读；状态明确不可写，模型调用仍HTTP200，恢复可写并重启后旧记录可查、新采集恢复，缺口历史保存。未宣称不可写关闭时一定能同步诊断。

### 首轮系统失败与修复

- 首轮 `make verify` 的系统包失败：故障夹具在坏配置/坏库启动后仍要求对象历史查询成功；辅助函数改为先检查公开状态，仅对 active 新目录自动发现，后续故障断言保留。
- 降额维护用例在15秒内仍显示旧 used，现场最后确实缩至主库36864字节、journal172032字节、仅一条存活记录。原因是逐页事务提交和每页额外 Sync，且完成前不发布进度。改为每批最多64页同一事务、批后同步日志及更新实际占用，不放宽等待期限。
- `make build-plugin build-retention-test` + 精确核心/系统失败项复测通过（核心2.032秒、系统20.754秒）；随后启动最终完整 `make verify`。
- 一次重建曾被 OOM 终止：Linux仅2GiB内存，保留的本轮测试目录在 `/tmp` tmpfs 消耗约1.4GiB。将仅本轮生成的目录移至隔离磁盘目录 `preserved-unit-tmp/` 保留，未删除，后续使用本轮 `tmp/` 磁盘目录及 `GOMEMLIMIT=512MiB`，重新构建通过。
- Linux 最终 `go test -race ./internal/audit ./internal/native -count=1`：通过（35.131秒、2.207秒），无竞态报告。先前一次 wrapper 因复制脚本时读到交错内容失败，未将其作为通过证据。
- 最终正式库容器复验通过，目录 `container-final/`，镜像和UID/GID不变；本轮库 SHA-256 `7ece1b6b2d11ddb08b6ab885eb57e9e1157a3ec817726fdca2dc098723792452`。
- 最终源码 ENOSPC/只读专项复验通过，目录 `disk-final.oubKz7/`，两模式正常解除挂载。该结果替代中间版本结果作为最终故障证据。

### 工作区审查

- 范围：`git diff 791c807616b65ec706ace55fd98e6788f26d78cd --` 加全部未跟踪新文件；不以未提交时三点差异为空跳过。
- Standards：0项。Spec：0项。两个执行者均只读，没有本机执行项目代码。
- 系统失败修复后仅复核 `maintenance.go` 与 `official_helpers_test.go`，两轴仍无新实质问题；对应精确回归已实际通过。

### 完整系统再次运行暴露的短读锁冲突

最终全量再次只在降额维护用例失败：故障为 `storage_error` 而非超时。测试的独立 SQLite 只读快照与维护提交重叠；原连接 `_busy_timeout=0` 将正常短暂共享锁当作永久存储错误。新增持锁250ms的确定性回归：旧实现实际失败，记录停在 Saved=1、storage_error；改为后台 SQLite 连接最多等待5秒后，读锁、实际缩库、热日志恢复和预算恢复四组连续3次通过（7.046秒）。模型回调仍不等待磁盘，也不将磁盘故障改成预算恢复。

两轴仅复核这一行连接配置和新增回归测试，均无新问题。交付版正式库再次通过 `container-delivery/` 官方容器验收，SHA-256 为 `ac794ebfb2e52b5b06e6da7b0ef36dcd61b02fc7fe7d875630b48e6de3d487ee`，替代前述中间摘要。

全量随后仅暴露两个异步测试边界，已分别修正：首次状态查询可能早于动态插件路由注册，辅助函数在原15秒就绪期限内等待404消失；FIFO记录查询已经返回时，队列计数仍可能因旧写循环在释放I/O锁后才扣减而暂时为1，现将记账移至解锁前，保持ioMu→mu锁顺序。未修改过滤规则或放宽用途断言。WebSocket排除和降额维护连续5次通过（16.718秒），用途全协议矩阵及完整组件继续执行。

浏览器两轮累计保留140条64KiB正文；SQLite还需等量日志工作空间，填充/恢复阶段的预算从旧JSONL的16MiB改为32MiB。4MiB最低预算、降额超额、未到期历史不删除和告警断言不变，避免把日志开销遗漏在夹具容量计算之外。

## 最终验收结论（2026-10-18）

最终所有已授权 Linux 验收通过。早期 `make verify` 的失败已逐项修复，最后按 Makefile 的组成步骤完成全量，不将早期失败或单项通过冒称完整入口通过。

| 实际命令或场景 | 最终结果 |
| --- | --- |
| `make check` | 通过，gofmt / vet / 全部测试编译 |
| 固定官方源码 `make build`、`make build-retention-test` | 通过；没有宿主 patch |
| `go test ./... -count=1` | 全部包通过，system 299.038秒、audit 31.859秒、native 0.615秒、tooling 3.250秒 |
| 用途三协议 HTTP/SSE 矩阵连续5次 | 通过，27.109秒；FIFO队列计数回归不再失败 |
| WebSocket排除、降额维护连续5次 | 通过，16.718秒 |
| `make tools-test` | 通过，Go工具测试、5项磁盘脚本单测、7项runner单测 |
| `make browser-run` | 8个真实场景各2轮，16项通过，cleanup=true |
| 最终预算场景 `--scenario storage-budget` | 额外2轮通过，实际将主动暂停时预算从32MiB改为64MiB，cleanup=true |
| `go test ./internal/audit ./internal/native -count=1` | 最终版本通过，27.317秒 / 0.636秒 |
| `go test -tags audittest ./internal/audit -count=1` | 最终版本通过，23.156秒 |
| `go test -race ./internal/audit ./internal/native -count=1` | 最终版本通过，27.224秒 / 1.834秒，无竞态报告 |
| 官方固定镜像非root加载、鉴权、保存、重启恢复 | 通过，`container-verified/` |
| 最终真实 ENOSPC / 只读重启专项 | 两模式通过，`disk-verified.rHpnSw/`，正常解除挂载 |
| 工作区 Standards / Spec 审查与受影响复核 | 各0项；队列记账和就绪辅助修复已限定复核 |
| 独立安装构建 `make build-plugin` | 通过；不构建用户CPA，产物摘要与已验收版本一致 |

浏览器16项为 `smoke`、`truncation`、`lifecycle`、`responses-websocket`、`search-browse`、`retention-settings`、`storage-budget`、`failure-status` 各两轮。有限报告为 `source/.test-data/verification-2uKKJQ/result.json`；最终预算补验为 `browser-budget-final/result.json`。没有遗留本轮宿主或运行中的测试容器。

macOS按用户要求未执行，Linux arm64 / musl未验证；Linux中的macOS专用磁盘用例跳过不计为通过，真实Linux磁盘专项已另行执行。未生产部署，未提交或推送；工作区仍在功能分支。

## 正式产物

- Linux打包、本机取回的安装包：`.build/releases/sqlite-storage-0.3.0-linux-amd64.tar.gz`，包 SHA-256 `a7829d988f7f0bc6261e17547b90f5d5b2616c8ce01433a426f0cd6d0c30f6b2`。
- 本机正式交付目录：`.build/releases/sqlite-storage-0.3.0-linux-amd64.20261017/`，含 `request-input-audit.so`、`SHA256SUMS`、`INSTALL.md` 和 `verification.json`。本机只接收Linux已构建产物，未运行该动态库或本机构建。
- Linux原产物：`/opt/cpa-linux-verify.IZCb2S/sqlite-storage.VJ0h2h/source/.build/plugins/request-input-audit.so`。
- SHA-256：`ea153fbd0119189719a1658f2893c2213966c2a61fd4b8020e8b04acecce84bf`。最终容器、磁盘专项和独立重建均对应同一正式库。
- 平台：ELF x86-64 / Linux amd64，最大glibc符号需求 `GLIBC_2.34`；已实机运行于固定官方容器。不是Alpine/musl或arm64产物。
- 不安装 retention/audittest 动态库，不沿用旧 `.build/plugins/` 中的本机历史文件。安装步骤见 `docs/local-delivery.md` 的“从 JSONL 版本升级：不迁移历史”及交付目录 `INSTALL.md`。
