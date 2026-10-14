# 05：Anthropic Messages 输入审计

本文定义 Anthropic Messages 的新用户内容与工具续跑边界，并将结果贯通到管理员页面。

**What to build:** 管理员可以查看 Anthropic Messages 本次末尾新用户输入；同为 user role 的工具结果续跑不会被误判为用户发言，混合新内容只保存允许部分。

**Blocked by:** 03 — Chat Completions 完整输入边界。

Status: completed

**Spec:** [请求输入审计 spec](../spec.md)。主要覆盖 User Stories 11–24、26–28、40–41。

## Acceptance criteria

- [x] 观察实际客户端提交的原始 Anthropic 协议输入，先于模型名业务改写、协议转换或执行路由；关联宿主已认证的客户端对象，不关联上游账号。
- [x] 仅包含 tool_result 的末尾 user 消息正常跳过，不因 role=user 就保存，也不向前回找旧用户发言。
- [x] 同一末尾 user 消息混合 tool_result 与新文本时仅保留新文本及允许非文本占位符；工具结果内部嵌套文本、图片、文件和引用均不提取。
- [x] 连续多个用户消息只取最后一条，不保存历史、system/developer 指令、模型回复或工具调用。
- [x] 已识别非文本块只保存固定占位符；未知块报告提取不完整或解析缺口；空输入、仅空白和普通文本秘密遵循既定规则。
- [x] 64 KiB UTF-8 安全前缀仅影响审计副本；管理列表和详情正确显示协议、请求模型、正文及截断或不完整状态。
- [x] 可提取输入的执行前路由失败不漏出观察范围；认证失败和未监控提交不记录；采集不等待模型回复成功。
- [x] 可控上游验证原请求不被审计改写、截断或拦截；持久化与诊断专项检查验证禁止内容不被额外保存。

## Testing decisions

通过真实 Anthropic HTTP 提交、受保护管理 API 与少量页面验收，覆盖纯文本、仅工具结果、混合工具结果与新文本、工具结果内嵌图片、未知块、空白和截断。预期使用独立夹具，不查询私有数据库结构。

## Implementation constraints

不能以转换后的 OpenAI 消息列表推断 Anthropic 最新输入。对无法识别的格式保守报告问题，不序列化完整请求或工具结果作为回退。

## Comments

- 2026-10-13：用户批准拆分后创建，尚未开始实施。
- 2026-10-14：按 `/implement` 在 `feat/anthropic-messages-audit` 实施；固定审查基点 `5b5fa5b83c4f819f5d10027616211199013e3546`。沿用已确认的真实 HTTP、受保护管理 API、可控上游和少量页面边界，不新增私有提取器测试。`/v1/messages` 观察先于 DD 模型名改写/协议转换/执行路由；纯 `tool_result` 整块跳过，混合末尾 user 只留新文本和 image/document 固定占位符；工具结果与文档来源不递归提取。
- 2026-10-14：TDD 红→绿：`TestAnthropicMessagesBeforeRouting` 初次无已保存记录失败，新增宿主接入/协议读取后通过；`TestAnthropicMessagesToolBoundary` 初次错误内容/缺口失败，独立 Anthropic 块白名单后通过；`TestAnthropicMessagesAdmission` 初次支持协议声明失败，更新能力门禁/管理状态后通过；`make browser-run` 初次于 `tests/browser/truncation.js:147` 协议 cell 失败，增加页面标签/范围说明并同步 CSP 哈希后通过。扩展附件、歧义、UTF-8 和 SSE 矩阵在上述切片已有实现下通过，不宣称每组另经历业务红灯。上游夹具初次把原宿主 system 数组规范化及 cache_control 注入误判为审计修改，按原有宿主行为修正独立业务断言后通过；完整开/关审计字节差分仍严格相等。
- 2026-10-14：`make check` 通过（gofmt、vet、测试编译）；`make test-one TEST=TestAnthropic` 通过（system 11.791s，含六组容量边界、25组内容案例、字段歧义、纯工具/混合输入、认证及未监控跳过、原生上游 HTTP/SSE、先保存后失败回复、正常重启）；禁存标记/持久化字段白名单与诊断扫描通过。`make test` 全套通过（system 73.400s，tooling 5.651s，含真实 ENOSPC/只读卷与队列压力）；`make tools-test` 通过（Go 工具测试及 Node 7/7）。固定补丁宿主 `go test ./sdk/api/handlers/claude ./internal/api ./internal/pluginhost` 全部通过。
- 2026-10-14：`make browser-run` 通过；smoke/truncation/lifecycle 各两轮，同一真实宿主、独立唯一标记与精确详情 ID，Anthropic 协议/客户端模型、工具混合占位符、不完整与双状态可见，外部加载0、进程回收成功。有限证据 `.test-data/verification-2ai9Dh/result.json`；已查看 `truncation-1-anthropic.png` 与 `truncation-2-anthropic-combined.png`，仅截状态标签，不附完整输入。
- 2026-10-14：本机 macOS arm64 / Go 1.27.2 / Node 24.18.0 验收；Linux amd64/arm64 未运行，不配置远端 CI。本票不宣称其他内容类型、Responses WebSocket、完整重试/断开矩阵、搜索、过期清理或完整故障恢复已完成。本地 hook 已配置 `core.hooksPath=.githooks`，存在可执行 pre-commit，未覆盖或重装。未提交、未推送、未部署；无用户需手动完成的本票本机技术验收。工作区双轴审查完成：Spec 0 项；Standards 1 项注释规范问题（夹具3秒等待上限原因）与2项维护性建议（采集/重启协议白名单共用、跨协议字符串字段辅助命名）已全部修正。原 finding 限定复核确认3项均 fixed，剩余0项。
- 2026-10-14：审查修正后 `make check`、`make build`、`make test-one TEST='TestAnthropic|TestResponsesHTTP|TestChat'`（system 34.912s）、`make browser-run` 和 `git diff --check` 再次通过。最新浏览器有限证据 `.test-data/verification-BaIWm2/result.json`；已查看 `truncation-1-anthropic.png` 与 `truncation-2-anthropic-combined.png` 标签截图，仍为同实例两轮通过、外部加载0与正常进程回收。清单指定9个宿主包回归全部通过（其中 config_access 无测试文件，未声称该包有运行用例）；`go test -race ./tests/system -run 'TestAnthropicMessages' -count=1` 通过，仅说明系统测试进程/夹具无报告竞争，不冒称未启用 race 的原生宿主内部已验证。完成状态仅指本票本机范围；未提交、未推送、未部署。
