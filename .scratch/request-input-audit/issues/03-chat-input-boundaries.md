# 03：Chat Completions 完整输入边界

本文定义 Chat Completions 从客户端原始提交到管理员详情的内容提取、截断和隐私验收。

**What to build:** 管理员能够查看 Chat Completions 本次提交末尾最新一条新用户输入，正确识别工具续跑、非文本占位、未知块、空输入与 UTF-8 截断，不保存历史或其他禁止内容。

**Blocked by:** 01 — 最小安全 Chat Completions 审计闭环。

Status: completed

**Spec:** [请求输入审计 spec](../spec.md)。主要覆盖 User Stories 11–24、40–41、54、59。

## Acceptance criteria

- [x] 只取本次显式提交末尾新用户消息；末尾连续多个 user 只取最后一条，不跨消息拼接。末尾 assistant、工具调用或工具结果不倒序回找旧 user。
- [x] 同一选中消息的允许文本和占位符按原顺序组合，工具内容及嵌套工具结果不提取；历史、system/developer、模型回复、工具调用或结果不保存。
- [x] 已识别图片、音频、视频、文件等只保存固定占位符，不保存载荷、文件名、URL、Base64 或引用标识；仅图片等输入仍可形成占位符记录。
- [x] 普通文本中的链接、Base64 或秘密仍原样保存，不进行启发式图片识别或通用脱敏，不把普通文本和协议非文本块混淆。
- [x] 未知内容块不序列化保存；可安全提取的部分标记不完整并产生可见诊断，完全无法判断时报告解析缺口，不保存整请求作为回退。
- [x] 真正空内容正常跳过且不报缺口；仅空白文本原样保存，不自动 trim。未知内容不能被归类为空而掩盖失败。
- [x] 默认按文本与占位符合计 UTF-8 字节执行 64 KiB 前缀上限，不含元数据；恰好上限不误报，多字节字符不会被切断，超限尾部不持久化。
- [x] 列表和详情明确显示截断或提取不完整，不宣称拥有完整输入；可查询正文只包含实际保存部分。
- [x] 截断、未知块和解析失败不修改上游原请求，也不为审计拒绝调用；异步队列不暂存完整请求。
- [x] 持久化输出和插件日志的专项检查验证禁止内容没有新增保存，而不是仅证明管理 API 不返回它们。

## Testing decisions

使用真实 Chat Completions HTTP 提交、受保护管理查询和可控上游，覆盖末尾角色排列、混合内容、非文本载荷、未知块、空白和 UTF-8 边界。预期来自独立夹具，不用提取器重算预期；不另立未经确认的提取器私有测试边界。

## Implementation constraints

普通正常跳过与解析缺口须区分；截断为已知容量限制，不冒充完整输入。本票建立的内容与状态语义供后续协议扩展复用，但不因复用需要提前实现全部协议。

## Comments

- 2026-10-13：用户批准拆分后创建，尚未开始实施。
- 2026-10-14：按批准方案在 `feat/chat-input-boundaries` 实施；固定审查基点 `da676dc8158d232e4273a2eaeb1f1be1c2aa535f`。末尾已知非用户角色正常跳过，有限 Chat 块白名单按序形成受限前缀；未知/畸形块不保存，安全部分持久化 `extraction_incomplete` 并产生固定缺口诊断。宿主契约、接收快照、队列和存储结构不扩展为其他协议或完整请求。
- 2026-10-14：TDD 红→绿证据：`TestChatTerminalMessage` 初次因非用户末尾误报缺口失败，修复通过；`TestChatOrderedContent` 初次无混合记录失败，修复通过；`TestChatUnknownContent` 初次安全部分缺失/显式 null 误报失败，修复通过；浏览器不完整标签初次在 `truncation-1` 失败，更新状态展示及 CSP 后通过。跨块容量专项在前一切片已有受限前缀实现下直接通过，不宣称该测试单独经历红灯。
- 2026-10-14：`make check` 通过（Go 格式、vet、测试编译）；`make test-one TEST='TestChat|TestTailsAndUnsupported|TestProtocolFieldAmbiguityDoesNotLeak'` 通过；`make test-one TEST='TestChatUTF8PrefixAndFailOpen'` 七组边界通过；`make test` 全套通过（system 46.998s，含实际 ENOSPC/只读卷与队列压力）；`make tools-test` 通过（Go 工具测试及 7/7 Node 测试）。
- 2026-10-14：`make browser-run` 通过；同一真实宿主的 smoke/truncation/lifecycle 各两轮，唯一标记与精确详情 ID；不完整、双状态、占位符与缺口可见，外部资源加载计数 0，进程回收成功。有限证据：`.test-data/verification-66jAmF/result.json`；已查看 `truncation-1-combined.png` 与 `truncation-2-incomplete.png` 状态截图，未截图或附加完整输入。
- 2026-10-14：本机 macOS arm64 / Go 1.27.2 / Node 24.18.0 验收；Linux amd64/arm64 未运行，不创建远端 CI。关键词/时间/模型搜索、其他协议、持久缺口历史、过期清理仍属后续票，本票只保证可查询正文为实际保存部分。本地 hook 已配置 `core.hooksPath=.githooks`，未覆盖或重装；未提交、未推送、未部署。工作区双轴审查：Spec 0 项 finding；Standards 2 项低优先级 finding（容量夹具数值推导注释、浏览器重复流程），已补注释并复用本文件 `openSavedDetail`，原 finding 限定复核确认两项均 fixed，剩余 0 项。
- 2026-10-14：审查修正后 `make check`、七组 `TestChatUTF8PrefixAndFailOpen`、`make browser-run` 和 `git diff --check` 再次通过。最新浏览器证据 `.test-data/verification-Is3lcn/result.json`；同一实例各场景两轮，已查看 `truncation-1-incomplete.png` 与 `truncation-2-combined.png` 标签截图，无外部资源加载，进程正常回收。
