# A 版请求监控工作台验收

## 范围与基点

用户先体验单 HTML 模拟原型，再明确选择 A · CPAMP 工作台。本票仅改原生单页展示与相关浏览器验收，不修改采集后端、宿主补丁或 API。固定工作区审查基点为 `b5e3e494220eb68207792b89ca68402bf3c5a995`，分支 `feat/cpamp-style-audit-ui`。审查包含基点后的未提交差异与新增 `tests/browser/ui-layout.js`，不以提交差异为空跳过工作区审查。

实现入口：`internal/native/assets/index.html`；需求与原型依据：`.scratch/cpamp-style-audit-ui/spec.md`、`issues/01-production-monitor-ui.md`、`request-monitor.prototype.html`。正式页面没有模拟数据、场景切换、B/C 布局或原型工具条。

## 实现与契约

- 浅蓝灰页面、白色面板、顶部操作及采集提醒、紧凑筛选、四项摘要与输入列表。对象、设置、诊断在次级面板，详情使用原生右侧 dialog。
- 摘要分别来自筛选后的 `total`、运行期 `saved/queued` 和保守占用/预算；错误或缺失显示未知，不用零冒充。没有 HTTP 调用成功率、Token、费用或虚构趋势。
- 筛选仍由原 API 完成，保持交集、精确模型、已保存正文的字面关键词及每页20条。时间预设使用本地自然日：今天00:00或含今天前6日00:00为起点，终点开放；全部清空端点，自定义只聚焦输入。
- 保留管理 Key 内存驻留、同源受保护接口、正文/名称/模型纯文本、双标记独立、三项设置联合保存及缩短确认、缺口快照已阅语义。十秒轮询仅更新状态，不覆盖草稿。
- CSP 仍为内联内容的精确 SHA-256，不增加 `unsafe-inline`、外部资源或动态 HTML 渲染；状态进度使用原生 progress，不依赖内联 style。
- 401/403 与 pagehide 显式关闭全部顶层 dialog 并隐藏受保护内容。查询失败清除旧列表、匹配数与详情。模态内操作结果同步显示，不要求用户关闭面板才能看到错误。

## 实际发现与修复

1. 首次浏览器启动因缺少仓库指定 Chromium 失败；执行 `make browser-install` 安装依赖后再验收，不把依赖准备算成功场景。
2. 宿主右上角浮动工具条覆盖 iframe 抽屉关闭按钮，点击会误触宿主退出，导致 macOS `search-browse.js:101` 和 Linux `smoke.js:55` 后续找不到 iframe。正式页面仅在 iframe 内为抽屉页头预留88px以避开宿主工具条，主题按钮移到左侧标题旁；真实宿主双轮回归已证明可关闭后继续操作列表。
3. Standards 审查用延迟详情响应复现：请求未返回前关闭抽屉，原查看按钮被 guard 禁用，焦点丢失。新增待恢复标记，在请求完成后恢复原按钮焦点，且不抢走已转移到其他控件的焦点；刷新与撤权清除引用。`ui-layout.js` 用精确 route 和 Promise 阻塞/释放本轮详情，验证响应前 Escape 与解锁后焦点返回，不使用固定 sleep。

## 固定范围审查

### Standards

1 项实质发现：慢详情提前关闭的焦点丢失；已修复并经原 finding 限定复核，无新增发现。

### Spec

0 项实质发现。核对已选 A、真实指标、纯文本、鉴权关闭、设置确认、缺口语义与轮询范围，未发现需求偏差。

## 最终源码与环境

最终执行源码共86文件；包含正式 HTML、新增布局辅助和全部受影响场景，不含 Markdown、`.scratch` 原型、依赖、产物、认证配置或审计数据。逐文件 SHA-256 清单摘要：`9be2b40f59580196bd7d9689942d1e2e171554e089cf8d418bed029360b0a3aa`，本地 `.test-data/cpamp-ui/linux-source-manifest-r3.{json,sha256}`。

- macOS：当前用户、本仓库 `.test-data/` 下的隔离合成宿主；Go1.27.2、Node24.18.0、仓库固定 Playwright/Chromium。
- Linux：用户明确授权远端独立目录验收；`ssh linux-service`，Debian13 x86-64；所有构建/宿主/测试以既有普通用户 `cpa-verify` 运行。目录 `/opt/cpa-linux-ticket-cpamp-ui.DAxUGq/source` 与 `evidence/r3`；使用既有隔离 Go/Node、HOME缓存及依赖，不部署、不改服务或配置、不安装全局 hook、不删除旧证据。

## 验收结果

最终 r3 在两端重新执行全部阶段，未用 r2 通过记录代替新增焦点修复后的结果。未串行调用会在工具阶段中止的 `make verify`，而是分阶段运行其检查、构建、Go、工具及浏览器内容，保证工具失败后其余验收仍实际执行。

| 阶段 | macOS | Linux amd64 |
| --- | --- | --- |
| `make check` | 通过，exit 0 | 通过，exit 0 |
| `make build build-retention-test build-compatibility-test demo` | 全部通过，exit 0 | 全部通过，exit 0；7项产物为新构建 ELF x86-64 |
| `go test ./... -count=1 -json` | exit 1：372 pass / 3 已有工具失败 / 0 skip | exit 1：370 pass / 3 已有工具失败 / 2 hdiutil skip |
| `make tools-test` | exit 2：三个缺 hook 的测试失败 | exit 2：相同三个缺 hook 的测试失败 |
| `npm test` | 7/7通过，exit 0 | 7/7通过，exit 0 |
| `make verify-linux-disk-tools-test` | 4/4通过，exit 0 | 4/4通过，exit 0 |
| 八场景同实例两轮浏览器 | 16/16通过，ready/cleanup为true | 16/16通过，ready/cleanup为true |

Go pass/fail/skip计数包含测试函数和子测试，不能与顶层数量相加。macOS为153个顶层通过、3个顶层失败、219子测试通过；Linux为151个顶层通过、3个顶层失败、2个顶层跳过、219子测试通过。系统测试包分别为347和345项通过；Linux另有2项平台跳过。没有 UI 或系统行为失败被归入已知 hook 问题。

两端 `search-browse` 的两轮均实际记录 `pendingDetailEscapeFocus=true`，并通过首屏表头、390px局部表格滚动、浅深主题、设置草稿、管理不重载、时间预设、筛选摘要、正常Escape焦点和恶意正文纯文本验证。真实401/403呈现测试使用浏览器拦截下一轮状态响应触发，原来的真实错误管理 Key 验证同时保留。

有限证据：
- macOS：`.test-data/verification-Y1ixCe/result.json`、`.test-data/cpamp-ui/macos-final-summary.json`；最终全量 Go 原文仅留本机 `macos-go-r3.jsonl`。
- Linux：`/opt/cpa-linux-ticket-cpamp-ui.DAxUGq/source/.test-data/verification-0D1PYm/result.json`、`evidence/r3/`；取回的摘要为 `.test-data/cpamp-ui/linux-r3-{final-summary,browser-result,go-summary,stage-summary,process-summary,native-artifacts}.json`。
- 最终真实宿主视觉：`.test-data/cpamp-ui/final-A-{desktop,dark,narrow,narrow-settings}.png`，使用无输入的合成实例；有记录的整体布局图 `production-A-masked.png` 已遮蔽正文、对象和模型。
- 最终86项源码摘要两端相同，无本票远端进程、挂载或普通用户宿主/浏览器残留。正式与隔离时钟产物的测试控制标识隔离已核对。

## 已有阻塞与未覆盖项

用户在本票开始前已删除 `.githooks/pre-commit`，`core.hooksPath` 仍为 `.githooks`。工具夹具 `tests/tooling/checks_test.go` 仍复制该文件，造成三个真实失败：`TestInstallTargetsRootDespiteForeignGitEnvironment`、`TestInstallRefusesEmptyHooksPath`、`TestInstallIsIdempotentAndHookChecksIndex`。两平台均已实际复现；本票未恢复文件、改 hook 配置或跳过这些测试。因此不能宣称全量 Go/工具验收通过，issue 保留未完成。

Linux 的两个 hdiutil 夹具不适用于该平台，单独记 skip，不计 pass；本票没有新增 Linux 实盘故障、arm64 或生产部署验收。浏览器使用真实合成宿主，不连接真实模型服务或真实凭据。截图限状态标签及遮蔽正文/对象/模型的布局图；原始 Go/构建日志、审计目录和完整正文不上传。

r3 验收时未提交、未推送、未合并，原型保留；当时已有工具测试阻塞未解决。后续用户决定和新验收结果见下节，以上 r3 失败记录保留。

## 后续：用户取消 pre-commit（2026-10-16）

用户明确说明 `.githooks/pre-commit` 是主动删除且不再需要，并通过选择题及实施计划授权清理安装功能。因此处理方式是取消功能，不是恢复文件、伪造夹具或跳过失败测试。

- 删除 `scripts/install-hooks.sh`，移除 `Makefile` 的 `hooks-install`；取消本仓库 local `core.hooksPath`，未改全局配置。默认 `.git/hooks/pre-commit` 也不存在。
- 移除六个专属 `TestInstall*`（含两个子测试）及仅为其服务的 `copyPublicTools`。其余 Fast/Staged/overlay 测试保留，手动 `make check` 和 `make check-staged` 保留，后者只改过时注释、未改逻辑。
- 更新 README 和当前验收指导；历史票据和 r3 的三个真实失败不改写。取消功能的票据为 `.scratch/cpamp-style-audit-ui/issues/02-remove-pre-commit.md`。
- 本次相对 r3 的执行源码变更只有 Makefile、check-staged 注释、安装脚本删除和工具测试取消；A 版页面、业务后端和浏览器测试均未改动。Standards 0项、Spec 0项实质发现。

r4 执行源码85文件，清单摘要 `5d00108e31a784a8356075b1cb9532890448feba976c108dfa20663e4bfdfba6`，本地 `.test-data/cpamp-ui/linux-source-manifest-r4.{json,sha256}`。远端使用新目录 `/opt/cpa-linux-ticket-cpamp-ui.DAxUGq/source-r4`、证据 `evidence/r4`，保留旧 source/r3 文件而非删除旧环境。

macOS `make verify` 已完整通过（exit 0），包括快速检查、全部原生构建、Go系统/工具、Node工具7/7、磁盘CLI工具4/4和浏览器16/16；浏览器报告 `.test-data/verification-i21Ydr/result.json`。日志仅留本地 `.test-data/cpamp-ui/macos-verify-r4.log`。Linux r4 同85项源码的全部阶段也通过：快速检查、三套原生构建/demo、完整 Go、工具及浏览器退出码均为0；Go 365通过、0失败、2平台跳过（148顶层与217子测试通过，两个 hdiutil 夹具按平台跳过，不计通过）。Node工具7/7、磁盘CLI工具4/4；八场景两轮16/16，ready/cleanup为true，两轮慢详情焦点回归均通过。本轮无待恢复 hook 或待补安装测试。

Linux 最终报告：`source-r4/.test-data/verification-NsNmmr/result.json`；有限摘要 `.test-data/cpamp-ui/linux-r4-{final-summary,go-summary,stage-summary,browser-result,native-artifacts,process-final-strict,source-change-summary}.json`。85项源码前后及本地/远端摘要完全一致，旧 r3 源码/证据未改；普通测试用户无遗留进程、本票无遗留挂载。两票现已按新结果完成，未提交、推送、合并或部署，无需要用户补做的技术验收。
