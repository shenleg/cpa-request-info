# 客户端辅助输入过滤验收

2026-10-16，本轮修复以 `f29b239b7e5f2bdbcb3d707ea1e74b238b52bec2` 为固定基点，在 `fix/client-input-filter` 分支实施。规范见[过滤 spec](../../.scratch/client-input-filter/spec.md)。未提交、推送、合并或部署，没有删除或改写既有记录。

## 实现与复现

- 自动补全、会话标题、动作描述及权限转录整体正常跳过；标题引用的真实原话不会被重新记录。
- 独立技能说明、完整客户端提醒与 IDE 上下文在截断前排除，保留独立正文、原始空白和固定附件占位符；工具/附件载荷不参与分类。
- 保持末尾消息选择、未知块状态、UTF-8 前缀、同文重交独立身份及实际上游请求/响应不变。Chat/Responses 文本语义不变。
- 首个公开采集回归与真实 `/v1/messages` 回归均先观察到建议请求误存；标题、动作、转录、技能、正文附加块分别先红后绿。旧构建的浏览器新增过滤断言也实际失败，不能计为新实现通过。
- 审查发现并修复三种误删：仅引用建议首行、独立转录结束后的真实新消息、被工具块隔开的代码围栏。各自公开接口回归先失败再通过；Standards 发现1项、Spec发现3项（包含同一转录问题），限定原问题复核均通过，无遗留发现。

## 已执行验证

| 项目 | macOS arm64 | Linux amd64 |
| --- | --- | --- |
| 最终源码 `make verify` | 通过 | 通过 |
| 快速格式、vet、测试编译 | 通过 | 通过 |
| 固定官方宿主、正式库及隔离时钟库原生构建 | 通过 | 通过，另跑 `make build-plugin` 通过 |
| 完整 Go 系统及工具测试 | 通过 | 通过 |
| Node runner / 磁盘参数工具测试 | 7/7、4/4通过 | 7/7、4/4通过 |
| 全部浏览器场景，同实例两轮 | 16/16通过，正常回收 | 16/16通过，正常回收 |
| `go test -race ./internal/audit ./internal/native -count=1` | 通过 | 未执行 |

macOS 最终源码另跑新回归：`go test ./internal/audit -run '^TestClientInput' -count=1 -json`，6个顶层测试、31个子例通过；`go test ./tests/system -run '^TestAnthropicClient' -count=1 -json`，6个顶层测试、38个子例通过。测试数不包含无测试文件包或编译检查。页面搜索场景两轮均报告 `clientInputFiltering=true`，列表、搜索、详情只显示本轮真实正文，辅助关键词不可检索。

Linux 首次远端执行被权限设置拒绝；用户随后单独授权以普通测试用户在新目录运行隔离构建与验收，不操作生产或凭据。最终源码以155个仓库文件的 SHA-256 与 macOS 快照核对，验收后再次核对完全相同；后续仅在本机补充本验收报告和任务完成状态，不改变已验证的实现或测试。构建复用只读官方 Git 缓存、固定管理页面和既有工具依赖，不借用旧插件产物。两端均为 Go 1.27.2，Linux 为 x86_64，Node 24.18.0。Linux 页面两轮均报告 `clientInputFiltering=true`、`cleanup=true`。

## 有限证据

- 本机源码快照与哈希：`.test-data/client-input-20261016/source-final.tar`、`source-hashes-final.json`。
- 本机新增回归结果：同目录 `unit-final.jsonl`、`system-final.jsonl`；仅合成输入，无真实用户正文或凭据。
- macOS完整最终日志与并发检查结果保留在本机临时证据目录，不公开机器绝对路径。
- macOS浏览器最终结果：`.test-data/verification-WGniSX/result.json`，16场景通过、`cleanup=true`；新增搜索场景包含辅助过滤两轮结果。
- Linux独立源码、日志和浏览器原始结果保留在获授权的隔离验收目录，不公开服务器路径或账户标识。
- Linux有限结果已取回 `.test-data/client-input-20261016/linux-final.log`、`linux-source-verified.json`、`linux-browser-final.json`。

## 边界

只按本轮已核对的客户端结构识别，不提供可信人工来源证明；与自动模板逐字相同的手工粘贴无法区分，未来模板不由本次获得保证。旧历史不会自动消失。未操作用户运行实例，必须在另行授权安装后才影响该实例的新记录。

本次不新增支持宿主版本、Linux arm64、其他客户端或生产负载保证；未重跑官方 Docker 容器、Linux专项真实磁盘故障或付费模型调用。默认套件的平台不适用跳过不计为通过，不能借用旧交付报告替代本轮结果。
