# 已确认自动内容过滤验收

2026-10-17，分支 `fix/confirmed-client-content-filter`，固定审查基点 `4fdceaad10a3d8ccee266fa6861eae3e0a60a12c`。本轮修复范围见[实现票](../../.scratch/client-input-filter/issues/02-confirmed-automatic-content.md)，完整匹配条件集中列在 [README](../../README.md#识别与过滤规则)。

## 实现

- unknown Anthropic 请求按固定前三句识别返回会话摘要；以独立 `<conversation>` 行开头且随后非空的纯文本会话包装整条跳过。不绑定业务标题，不要求未确认的结束标签。
- 所有保留用途的 Anthropic 请求清理独立 `/run` 重新调用说明块，以及块首 `Web page content:`、分隔符和非空内容组成的网页块；其他独立正文、附件占位符、原始空白和代码围栏保留。
- 仅 unknown Chat 的末尾纯文本整消息精确等于 `follow system prompt` 时跳过。不 trim、不忽略大小写、不匹配子串、不影响其他协议，不在共享 `chatContent` 中添加关键词过滤。
- 未改用途分类、未知用途继续采集策略、存储格式、模型请求或回复。仅影响新审计副本，不过滤旧历史、不要求客户修改配置。
- README 集中说明执行来源、采集资格、请求头/路径用途识别、协议边界、所有整请求与独立块规则，以及同形手工粘贴也会被过滤的限制。

## 复现与修正

1. 临时 overlay 先复现截图五类可见文本被提取；随后公开 Collector 回归实际失败，新增摘要、conversation、独立技能/网页和 Chat 精确指令均出现误存。该过程仅使用合成样例，没有读取真实客户请求。
2. 过滤实现后，首次新增长度测试错误地把诊断 map 当成只包含非零项；现有 map 预置所有原因，因此断言失败。改为复制原计数并只增加预期的 `unsupported_content`，精确比较全部计数；未改变生产诊断行为。
3. 真实 HTTP 专项发现摘要匹配额外依赖 `Lead with…`，使完整固定前三句仍误存。取消该额外条件后统一重建插件，专项通过。
4. 测试初稿把网页标题放在 `Web page content:` 之前，与批准的块首规则不符；正向样例改为分隔符后的标题，标题在前作为保留反例，未扩大生产匹配范围。Chat 测试初稿读取列表预览代替全文详情，已改为按本轮新增 ID 获取详情。
5. 部分早期定向 HTTP 测试与主会话构建交叠，不能统一归因于旧产物，也不计作最终验收。最终命令在统一重建后运行。

## 已执行结果

| 检查 | macOS arm64 | Linux amd64 |
| --- | --- | --- |
| `go test ./internal/audit ./internal/native -count=1` | 通过 | 全套内通过 |
| `go test -race ./internal/audit ./internal/native -count=1` | 通过 | 未执行 |
| `go test -tags audittest ./internal/audit -count=1` | 通过 | 通过 |
| 最新插件构建后的 `TestAnthropicClient.*`、`TestChatFollowSystemPrompt`、`TestHTTPRequestPurposeAnthropicMain` | 单独通过，51.327秒 | 全套内通过 |
| `make verify` | 通过，快速检查、原生构建、全套 Go/工具与浏览器全部完成 | 通过，同样完成全部阶段；另跑 `make build-plugin` 通过 |
| 真实浏览器，同实例两轮 | 16/16，辅助过滤与搜索两轮均通过，cleanup=true | 16/16，两轮过滤与搜索通过，cleanup=true |
| Standards / Spec 工作区审查 | 两轴均0项 | 共用同一源码审查 |

所有负向保存断言使用本轮标记或新增记录 ID 与正常正文 FIFO 屏障，不以固定延时或旧数据证明跳过。用途矩阵覆盖 unknown/main/turn；原生 Anthropic 和 Chat/Responses 比较审计开关前后的实际请求与响应，确认过滤只影响审计副本。

## 双端源码与有限证据

- 本机 `.test-data/confirmed-client-content-20261017/` 保存182项源码摘要、源码快照和分阶段结果；源码/测试/README 的相同快照送往既有隔离 Linux 环境，由普通 `cpa-verify` 用户运行，不操作生产服务。
- Linux 首次准备脚本误写工具安装路径，`go: command not found`，未进入构建或测试；原目录保留。在新独立目录修正 Go/Node 路径后重跑。未安装工具、修改凭据或关闭校验。
- macOS 实际浏览器结果为 `.test-data/verification-650wI5/result.json`，16/16、ready=true、cleanup=true；两轮 `search-browse` 均报告 `clientInputFiltering=true`。已查看该轮有限状态标签截图，不保存完整正文截图。
- Linux 普通用户以 Go 1.27.2、Node 24.18.0 运行，沿用只读固定官方 Git 缓存、固定管理页和现有依赖，不复用旧插件产物。最终浏览器结果为本轮独立目录下 `source/.test-data/verification-rD6Sl5/result.json`，16/16、ready=true、cleanup=true，两轮 `clientInputFiltering=true`；取回的 `linux-source-and-browser.json` 与有限状态截图已核对。
- 两端验收结束后再次校验182项源码摘要完全一致。随后只更新本报告与实现票完成状态，不改变已测的实现、测试或 README。macOS/Linux 系统包实际分别耗时221.685秒、225.994秒；Node 工具测试两端均7/7及4/4通过。Linux race 未单独执行。
- 本轮未重跑官方 Docker 容器、Linux 专项真实小卷 ENOSPC/热切只读或其他架构，不以默认套件中的平台跳过算作通过；没有新增这些能力保证。

## 范围与授权

未抓取真实客户正文，未修改 `.env`、密钥、token、客户端设置或 CI/CD；未删除既有文件、数据、seed、镜像或分支。没有 commit、push、合并或生产部署。Linux arm64、其他宿主版本、所有客户端自动模板覆盖率与人工来源准确率不在本次验证范围。
