# 07：Responses WebSocket 消息级审计

本文定义已认证 Responses WebSocket 连接内每次客户端模型提交的审计边界和配置生效行为。

**What to build:** 管理员可逐次查看一个 WebSocket 连接中的用户提交，不把连接握手、控制消息或宿主恢复的历史当作新输入；名单与采集配置在下一次提交生效。

**Blocked by:** 02 — 监控对象与采集配置生命周期；04 — Responses HTTP 输入审计。

Status: completed

**Spec:** [请求输入审计 spec](../spec.md)。主要覆盖 User Stories 11–24、29–32、40–41、52–54。

## Acceptance criteria

- [x] 在真实 Responses WebSocket 消息处理链路，先于历史补全、协议业务改写和模型执行观察每次客户端模型提交；不是仅记录 HTTP 握手或执行层还原 body。
- [x] 使用宿主对连接的实际认证身份匹配内置客户端审计对象，不重新猜测消息携带的 Key，不额外修改宿主握手或连接鉴权规则。
- [x] 每次合格模型提交使用宿主生成的独立提交标识；同连接多次提交分别显示，内部重试不新增记录，不持久化连接或会话标识以合并历史。
- [x] 本次显式新用户内容依 Responses 规则提取；字符串、结构化内容、图片等占位符、未知块、空白和截断保持既定语义。
- [x] 不回查 previous_response_id、不从宿主恢复的历史补正文；本次显式重发旧文本仍按新提交独立记录。
- [x] 握手、控制消息、取消事件与纯工具结果续跑不产生输入记录，不回找旧 user；取消或断开不删除此前已接收的合格输入记录。
- [x] 名单添加/移除、名称、全局暂停和单条上限变更在下一次提交采用当前配置快照，无须重连；已入队副本不受后续变更追溯影响。
- [x] WebSocket 消息解析或采集故障不为审计终止连接或模型调用，产生基础可见诊断；未知事件不序列化保存完整消息兜底。
- [x] 管理页面逐条显示记录及状态；可控上游与客户端公开事件证明审计不改变原提交及宿主原有执行语义。
- [x] 对插件存储和诊断检查，不新增保存原 Key、会话标识、原始整帧、历史、工具结果或非文本载荷。

## Testing decisions

通过真实 WebSocket 客户端建立连接并发送多次请求，使用管理 API 验证记录；覆盖工具续跑、控制/取消、previous_response_id、配置中途修改、重发和断开。保持原外部系统行为边界，不把 handler 私有状态作为合同。

## Implementation constraints

WebSocket 需要单独的消息级观察契约，HTTP 握手标识不能替代提交标识。若实际宿主事件语义与静态假设不同，应明确验证和必要技术调整，不静默降低已确定的新输入与故障边界。

## Comments

- 2026-10-13：用户批准拆分后创建，尚未开始实施。
- 2026-10-15：在 `feature/responses-websocket-audit` 实施；固定工作区审查基点 `2441a29412868cab8e91066e3af0e7b1d28b5385`。实际接入是宿主唯一完整帧 reader 的入队前位置，覆盖普通 handler 及 Codex duplex，避免后续 `create`/`append`/业务 `steer` 绕过观察。GET 仅绑定已认证对象工厂，逐帧生成独立宿主标识、时间和当前配置快照；没有为审计改变握手或重新鉴权连接。
- 2026-10-15：独立契约升为 v3，ABI 1 / RPC schema 6 不变，capabilities 必须声明三 HTTP 协议与 `responses-websocket`。本票补齐1–65536字节单条上限的受保护 GET/POST `/settings` 与页面，不提前实现保留期/预算。旧配置默认65536，历史及已接收副本不追溯重截断。宿主没有接受所有模式的字符串 input：普通 fallback 保持原拒绝，原生 duplex steering 的字符串已用公开上游验证；未显式传模型显示未知。
- 2026-10-15：真实客户端/管理 API/本地 HTTP SSE 和 Codex WebSocket 上游验收覆盖同连接独立重交、末尾/工具/附件/未知块、实际历史合并及 `previous_response_id`、本地预热恢复、原生 `steer` 失败确认、逐提交配置和排队时快照、Key 热移除、内部重试、首 token 后真实 interrupt/断开、部分消息终帧缺失、存储权限故障；普通及原生路径分别比较审计开关下的完整上游字节和公开事件。扫描全部隔离审计输出白名单及禁存标记，不仅核对管理 API；不保存完整输入或认证材料作为验收报告。
- 2026-10-15：TDD 先确认新 WebSocket 无记录及 settings 404；部分帧最初无读取缺口，补充 `read_error` 后回归通过。浏览器初次红测是夹具误用 `api_key` query 导致鉴权失败，改为宿主既有 `key` 参数，不修改鉴权；随后旧 JSON 合成上游仍失败，最小 SSE 上游使四个浏览器场景同实例两轮通过。有限证据：`.test-data/browser-ws-final-green-20261015/result.json` 与对应状态标签截图，进程正常回收。
- 2026-10-15：工作区 `code-review` 两轴使用固定同一文件清单，发现契约注释过时和1字节上限下空 UTF-8 前缀漏记；均已修复并由原审查者定点复核，无剩余 finding。新增公开回归覆盖汉字/emoji空截断前缀、图片占位符1字节前缀、未知块独立状态、真正空输入跳过及正常重启读取。

### 技术验收记录

| 命令/范围 | 结果 | 有限证据 |
| --- | --- | --- |
| `make check`（多轮） | 通过 | gofmt、vet 与全部测试编译；不替代真实系统测试 |
| `make test-one TEST='TestResponsesWebsocket\|TestSettings'` 对应 `go test ./tests/system -run '^TestResponsesWebsocket\|^TestSettings' -count=1 -v` | 通过 | WebSocket 和动态上限的公开行为专项，关键场景同实例两轮 |
| 固定宿主 `go -C .build/sources/patched-host.PC8mB8 test`：manifest 所列9个相关包 | 通过 | pluginhost、access、pluginapi/ABI、HTTP handlers、openai/claude、internal/api 无回归；后续宿主改动仅注释 |
| `make test && make tools-test && make browser-run` | 通过 | 全部 Go 系统/工具测试、7项 Node 工具测试、四浏览器场景各两轮；`.test-data/verification-Zx5qOY/result.json` |
| 审查修复后的最终 `make verify` | 通过 | 全部 Go 系统/工具测试、7项 Node 工具测试、四浏览器场景各两轮；包含真实队列满、macOS ENOSPC/只读环境重启，进程/挂载正常回收；`.test-data/verification-TmoWSN/result.json` |
| Linux amd64/arm64 运行与完整第一版交付 | 未执行 | 本票仅 macOS arm64 / Go 1.27.2 / Node 24.18.0 验收；目标平台与后续功能归 ticket 13 |

本仓库原有 `core.hooksPath=.githooks` / 可执行 pre-commit 保持不变；本轮未安装或修改 hook。首次交付时未暂存、commit、merge、push 或部署，因此当时未执行暂存快照门禁；无需人工重复已通过的技术验收。

- 2026-10-15：用户随后明确授权提交，并确认快进合并至 `main`、普通推送 `origin/main`（不强推）。提交前检查真实暂存区，提交时沿用原有 pre-commit；此授权不包含部署、发布或删除。
