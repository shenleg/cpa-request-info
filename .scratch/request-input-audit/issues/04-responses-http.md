# 04：Responses HTTP 输入审计

本文定义 Responses HTTP 从本次客户端提交到管理员记录的协议扩展，不引入会话历史回查。

**What to build:** 管理员能够独立审查 Responses HTTP 的字符串和结构化新用户输入，看到正确协议、请求模型及提取状态；宿主执行所需的历史与协议改写不扩大审计范围。

**Blocked by:** 03 — Chat Completions 完整输入边界。

Status: completed

**Spec:** [请求输入审计 spec](../spec.md)。主要覆盖 User Stories 11–24、26–28、31、40–41。

## Acceptance criteria

- [x] 通过实际宿主观察扩展接收 Responses HTTP 本次原始协议输入，先于业务改写、路由和会话补全，不用转换后 Chat 消息替代。
- [x] 字符串 input 作为本次用户文本处理；结构化输入仅取末尾明确的新用户消息，不回找旧 user、不跨消息拼接。
- [x] 仅工具输出的续跑正常跳过；未知项或无法安全判断的格式产生提取不完整或解析缺口，不落盘完整 JSON。
- [x] `previous_response_id` 不用于审计历史回查，不存该标识，不从宿主还原的上下文补正文；本次显式重发的文本作为独立新提交处理。
- [x] 文本、非文本占位、普通文本不自动脱敏、空白、空输入和 UTF-8 截断沿用既定规则；工具结果内部内容不被提取。
- [x] 管理列表和详情可识别 Responses 协议，显示客户端请求模型与记录状态；不能以实际上游模型代替请求模型，无法可靠确定时显示未知。
- [x] 可提取的新输入即使模型/provider 路由前失败仍可查询；认证失败和未监控对象不产生输入记录。
- [x] 可控上游和宿主公开响应证明观察、截断或解析失败不改变原始请求与调用结果；记录不依赖模型回复落盘。
- [x] 专项持久化检查确认没有新增保存历史、指令、工具数据、非文本载荷、原始认证 Key 或整条 Responses 请求。

## Testing decisions

通过 Responses HTTP 提交和管理 API 查询验收字符串、结构化列表、工具续跑、previous_response_id、未知块与路由前失败；使用可控上游验证调用不变。共用原系统行为测试边界，不新增生产故障注入接口。

## Implementation constraints

本票不实现 WebSocket 握手或消息处理；其后续 ticket 复用已验证的 Responses 内容语义，但必须另外验证消息级原始输入来源。未完成 WebSocket 时明确显示实际支持范围。

## Comments

- 2026-10-13：用户批准拆分后创建，尚未开始实施。
- 2026-10-14：通过 `/implement` 在 `feat/responses-http-input-audit` 实施；固定审查基点 `8ba890d549809eb66c092d1550cbbd162ac3f52b`，前置票 03 已 completed。沿用父 spec 已确认的 HTTP→管理查询、可控上游、隔离持久化输出与管理页边界，不新增私有提取器测试。
- 2026-10-14：宿主在标准/compact/Codex direct 别名精确 POST 路由的最终原生鉴权后 prepare，`ReadRequestBody` 解码后、业务改写/路由前 observe；协议参数化为 `responses`，GET 握手排除。contract v2 保持不变，但 capabilities 必须声明 Chat 与 Responses 两协议，旧 Chat-only 同版本宿主不能静默降级。原始输入指解码后的协议正文，不冒称压缩 wire bytes 或早于既有中间件第一次读取 socket。
- 2026-10-14：红→绿证据：`TestResponsesHTTPStringBeforeRouting` 首次记录未保存，补宿主/插件支持后通过；`TestResponsesHTTPTerminalMessage` 首次结构化记录未保存，补末尾明确 user 与工具排除后通过；`TestResponsesHTTPContent/mixed` 首次占位混合记录未保存，补有限非文本白名单后通过；`TestResponsesHTTPRoutesAndAdmission` 首次 compact 入口未保存，补各精确入口与能力声明后通过；浏览器首次在 `truncation-1` 的 Responses 协议单元格等待失败，补列表/详情协议与请求模型、更新 CSP 后通过。已有共用 UTF-8 前缀、未知块、SSE、回复前落盘及实际业务改写专项直接通过，不宣称这些专项单独经历红灯。
- 2026-10-14：实际验收：`make check` 通过（格式、vet、测试编译）；`make test-one TEST='TestResponsesHTTP'` 通过（system 14.400s，含字符串/结构化/工具续跑/未知项/非文本/空白/鉴权/各入口/模型未知/UTF-8/SSE/上游失败与重启）；`make test` 全套通过（system 62.342s，含真实 ENOSPC、只读卷和队列满；tooling 3.850s）；`make tools-test` 通过（Go 工具与 7/7 Node 测试）；manifest 八个宿主兼容包全部通过。可控上游确认客户端 `responses-client-model` 映射为实际 `audit-test-model`，记录不被实际上游或回复模型替代；审计开启/关闭的实际上游字节和公开响应一致。
- 2026-10-14：上游阻塞屏障下先查询到本轮已保存输入，再释放失败回复，确认采集不依赖模型响应落盘。通过宿主已有 `upstream.codex.orphan-delegation-compatibility` + 合成 subagent header 实际触发工具输出→user 改写；上游收到改写后的 user，但审计仅保存后续唯一排空屏障，工具输出与历史未落盘。全隔离审计目录及诊断输出专项检查同时覆盖字段白名单与禁止标记，不只依赖管理查询。
- 2026-10-14：`make browser-run` 同一宿主 smoke/truncation/lifecycle 各两轮通过；每轮唯一 marker 与精确详情 ID，Responses 协议、已知/未知请求模型和不完整状态可见，外部资源计数 0、进程回收成功。证据 `.test-data/verification-R5ulRN/result.json`，已查看 `truncation-1-responses.png` 与 `truncation-2-responses-unknown-model.png` 的有限标签截图。初次失败证据 `.test-data/verification-iEDaIx/result.json` 保留，不自动删除。
- 2026-10-14：工作区固定清单双轴审查：Spec 0 项 finding；Standards 0 项硬违规、1 项判断性 Duplicated Code 建议（Chat/Responses 数组组合）。已局部抽出 `contentBlocks` 共用按序组合与独立双状态规则，保留各协议消息选择和块白名单；原 finding 限定复核确认 fixed，未解决 finding 为 0。修正后 `make check`、`make test-one TEST='TestChat|TestResponsesHTTP'`（system 22.871s）、`make browser-run` 与 `git diff --check` 再次通过，最新浏览器证据 `.test-data/verification-SWqhz7/result.json`，已查看两轮 Responses 已知/未知模型标签截图。
- 2026-10-14：本票范围验收通过，标记 completed，不代表父 spec、Responses WebSocket 或后续票完成。本机 macOS arm64 / Go 1.27.2 / Node 24.18.0；Linux amd64/arm64 未运行、不创建或触发远端 CI。七天清理、组合搜索、完整重试/断开/异常恢复等仍属后续票。现有 `core.hooksPath=.githooks` 保留，未覆盖或重装；未提交、未推送、未合并 main、未部署，暂存区无本轮变更；没有需要用户协助的人工验收待办。
