# 06：HTTP 调用失败、流式与重试的一次提交语义

本文验证通用 HTTP 观察链路与模型执行结果、流式完成和内部重试之间的独立性。

**What to build:** 管理员看到的是每次客户端提交的输入，而不是每次执行尝试或成功响应。上游失败、SSE、超时、断开和内部执行重试不导致漏掉已接收的合格输入或重复增记。

**Blocked by:** 01 — 最小安全 Chat Completions 审计闭环。

Status: completed

**Spec:** [请求输入审计 spec](../spec.md)。主要覆盖 User Stories 22–28、52–55。

## Acceptance criteria

- [x] 正常非流式与 SSE 流式 HTTP 请求在接收阶段采集，不能等待响应成功、首 token 或完整响应才决定记录。
- [x] 相同客户端文本重新提交两次产生两条独立记录；客户端自报请求 ID 或会话信息不作为去重依据。
- [x] 同一入站提交的上游重试、账号切换和流式首 token 前重试最多产生一条记录；宿主生成提交标识与内部执行标识不混淆。
- [x] 插件或宿主发起的嵌套模型执行不当作新客户端提交，不因产生内部执行 ID 增加原提交记录。
- [x] 上游失败、超时或客户端在合格输入已接收后断开，不取消本应排队保存的副本；异步保存失败另报缺口，不把模型失败当作采集失败。
- [x] 可提取输入的模型/provider 路由前失败仍被观察；鉴权失败不记录。无法解析或不完整接收不捏造输入，应展示可确定的采集问题。
- [x] 可控上游和公开响应验证观察不会改变请求或宿主已有成功、错误、流式及取消行为；审计不能额外返回拒绝状态。
- [x] 管理详情不保存响应正文或状态跟踪信息来重建执行日志，仅使用 spec 允许的元数据和采集状态。
- [x] 本票以已经可用的 Chat Completions 链路验收通用机制，并提供可复用外部行为场景；最终交付票将其回归到三协议，不能据本票宣称尚未实现的协议已验证。

## Testing decisions

在原系统行为边界通过可控上游产生失败、延迟、重试和流式行为；真实客户端断开并通过管理 API 等待可观察结果。不要靠固定 sleep、usage queue 或直接调用私有 lifecycle 方法证明入站一次语义。

## Implementation constraints

保持有界异步和常规 fail-open，不能为精准去重引入会话拼接、客户端可控唯一性或无限历史索引。缺口生命周期完整交互在后续 ticket 完善，但本票发生的异常已有基础可见诊断。

## Comments

- 2026-10-13：用户批准拆分后创建，尚未开始实施。
- 2026-10-15：按 `/implement` 开始本票，固定审查基点 `7baf4cf8176f301d6c6914c4c2d368d8fb1a292d`，实施分支 `feat/http-request-once`。沿用 spec 已确认的真实客户端 HTTP → 固定补丁宿主/原生插件 → 受保护管理 API 边界，配合可控上游与隔离存储隐私检查；不将私有 lifecycle、执行 ID 或 usage queue 作为验收证据。本票只验收 Chat Completions 通用一次提交矩阵，三协议完整回归仍归 ticket 13。
- 2026-10-15：新增 `tests/system/http_once*_test.go` 的 8 个顶层公开系统测试，覆盖接收阶段保存、上游失败/408、SSE 完成/首 token 后失败、相同文本与客户端 ID 重交、同账号重试/账号切换、SSE 错误帧的首 token 前重试、真实客户端取消/期限及 SSE token 后断开、路由失败/鉴权拒绝、有效 JSON 前缀但未完整接收的真实 TCP chunk、畸形 JSON、模型/异步保存同时失败。可重复场景在同一实例连续两轮；首次新增测试暴露的是夹具误假设（SSE 既有 include_usage/结束原因、可重试错误类型、native 必填 metadata），已按关闭审计的公开基准修正，不据此宣称修复了生产业务故障。
- 2026-10-15：新增仅测试使用的真实 C ABI 插件，从原请求拦截 context 调用官方 `host.model.execute`，上游明确收到两次不同输入，管理只保存原提交。夹具只位于本轮 `.test-data/` 插件目录，不加入默认产物。业务采集/宿主补丁未修改，未引入会话拼接、客户端去重、历史索引或执行日志；管理详情/持久化白名单检查通过。宿主已有上游失败 warning 与关闭审计的次数一致，审计目录无错误正文；README 明示该已有日志边界。
- 2026-10-15：`make build`、清单中的 9 个宿主接入 package 回归、`make check`、`make test-one TEST='TestHTTPOnce'` 通过；`go test -race ./tests/system -run TestHTTPOnce -count=2` 通过。两轴工作区审查：Standards 首轮 1 项容量阈值注释硬违反和 1 项不可取消屏障可靠性判断均修正并定点复核关闭；Spec 0 项偏差。修正后 `make check` 及本票 `-race -count=1` 再次通过。
- 2026-10-15：首次 `make verify` 在既有 `TestChatTerminalMessage` 的管理 `/status` 查询遇到 `context deadline exceeded (Client.Timeout exceeded while awaiting headers)`，后续工具/浏览器阶段未执行。原测试独立 `-count=3` 全部通过，未修改该测试、未延长超时、未跳过；不能据此声称已确定或修复根因。正重新执行完整验收，最终结果另记。
- 2026-10-15：第二次 `make verify` 全部系统及工具测试通过，浏览器在 `lifecycle-2` 的 `tests/browser/lifecycle.js:106:68` 等待详情超时，其余5轮通过，`cleanup=true`；未改 UI、未放宽等待或跳过场景。独立 `make browser-run` 随后6轮全部通过，有限结果 `.test-data/verification-vo4Md2/result.json`，状态标签截图已查看，进程正常回收；尚未确认上述偶发等待失败的根因，不声称已修复。补充 native 构建夹具先创建验收根目录，以支持新检出仓库的单项执行；本票嵌套测试再次通过。正在最后整轮 `make verify` 复测，结果另记。
- 2026-10-15：最终完整 `make verify` 同一轮全部通过（exit 0）：快速检查、固定补丁重新构建、全部 Go 系统/工具测试（系统 87.022s，包含真实 ENOSPC/只读卷与队列满）、7 项 Node 工具测试、真实浏览器3场景各2轮。最终浏览器有限证据 `.test-data/verification-2MqT0S/result.json` 为 `passed`、`cleanup=true`，状态标签截图已查看。外部只读检查确认本项目宿主/demo 进程0、测试镜像挂载0；源码、合成数据与镜像均保留，不自动删除。
- 2026-10-15：本票验收项均有公开行为证据，设为 `completed` 并移除 Triage。Standards 原2项修正后未解决0项，Spec 0项偏差；小型夹具目录前置修正已定点重测及最终完整回归通过。本机仅 macOS arm64 / Go 1.27.2 验证；Responses/Anthropic 的完整失败矩阵、WebSocket、Linux双架构及后续恢复/缺口生命周期仍不宣称完成。现有 pre-commit `core.hooksPath=.githooks` 已启用，未重新安装或改动。工作留在 `feat/http-request-once`，未 commit、merge、push、部署或修改真实凭据/CI；无需要用户代做的本票技术验收。
- 2026-10-15：用户另行明确要求“提交并推送”。本次获独立 Git 授权，按全局约定在功能分支提交，经真实暂存快照检查后快进合并到 `main` 并推送 `origin/main`；保留功能分支，不删除或改写历史。提交/推送实际结果以 Git 命令和交付报告为准，不由此前实施授权推定。
