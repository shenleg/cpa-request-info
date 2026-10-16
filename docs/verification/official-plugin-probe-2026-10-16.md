# 原版 CPA 官方插件接口隔离验证

本文记录 2026-10-16 的真实原生集成实验，回答是否值得将现有审计插件改为无需宿主补丁的官方插件。探针是实验夹具，不是可部署的输入记录插件。

## 结论

**值得考虑有明确覆盖限制的 HTTP 输入记录版；不能作为现有四协议入站审计的等价替代。**

常规三协议 HTTP/SSE 输入在本轮样本中原样到达执行拦截器；同 Key 身份摘要稳定，普通重试可用执行 ID 去重，管理 API 与无数据静态资源均可在原版宿主中加载。硬性差异也已实际复现：路由前失败没有任何回调；嵌套执行产生额外执行；Messages 的 DD 模型改写早于回调；普通 WebSocket 回调包含补全的历史；Codex duplex 后续 append/steer 确实到达上游，但完全绕过执行回调。

测试驱动通过表示测量和转发已按预期完成，**不表示原版接口满足现有审计需求**。

## 固定输入与隔离方式

- 项目审查基点：`6711c1cb7a08c432e8690e66c87a3a74ad752480`。
- 实验分支：`experiment/official-plugin-probe`；未提交、推送、合并或部署。
- 官方宿主：CLIProxyAPI `v8.0.27`，commit `bf9341629ef2300ac9d846a2e80fe2756f63a671`。
- 缓存工作树已有修改，本轮从该精确 Git 对象导出全新目录构建；未应用本项目补丁，未调用 `scripts/build.sh`，未覆盖默认宿主/插件产物。探针额外确认 `host.input_audit.capabilities` 返回不支持。
- 全部模型流量使用本地 HTTP/WebSocket 合成上游；客户端/管理/上游 Key 均为独立合成值。宿主绑定回环地址，使用独立配置、auth、插件和运行目录，不访问生产服务或真实凭据。
- 探针只在内存保留最多 512 条事件摘要；本轮没有 dropped。报告不含原始正文、请求头、Key、系统/历史/工具正文或模型回复。正文只在回调和测试客户端内存中比较。

## 环境与实际执行

| 环境 | 快速检查 | 原版宿主/探针原生构建 | 集成实验 | 相关工具检查 |
| --- | --- | --- | --- | --- |
| macOS arm64，Go 1.27.2 | `make check` 通过 | 通过 | `make verify-official-probe` 通过，7.87 秒 | `go test ./tests/tooling -count=1`、带 officialprobe tag 的 vet、bash 语法检查通过 |
| Debian Linux amd64，Go 1.27.2，普通用户 cpa-verify | `make check` 通过 | 通过 | `make verify-official-probe` 通过，7.16 秒 | 同上通过 |

两端使用相同实验源码，7 个构建/实验入口文件的 SHA-256 一致；场景、轮次、发送数、上游接收数和各阶段回调/ID 数的完整矩阵一致。每端同实例两轮，共 **44 条场景观测、42 次客户端模型提交、46 次实际上游尝试**；观察到 Before 38 次、After 42 次、complete 38 次。差额来自已测的重试、嵌套和无回调提交，不解释为随机丢失。

命令入口：

```bash
make check
make verify-official-probe
go test ./tests/tooling -count=1
go vet -tags officialprobe ./tests/system
bash -n scripts/verify-official-probe.sh
```

`make verify-official-probe` 从本地缓存 Git 对象构建官方宿主和两个独立测试插件，再执行 `go test -tags officialprobe ./tests/system -run '^TestOfficialProbe' -count=1 -v`。缓存可由 `CPA_OFFICIAL_SOURCE` 显式指定；对象不存在时明确失败，不借用修改过的工作树。实验 build tag 不改变默认正式系统测试范围。

## 两端一致的实测结果

下表为每轮数据，除注明外均在同一实例执行两轮。

| 场景 | 客户端提交 / 上游尝试 | Before / After / complete | 判断 |
| --- | --- | --- | --- |
| Chat、Responses HTTP、Messages，分别非流式及 SSE | 每个组合 1 / 1 | 1 / 1 / 1 | 6 种常规组合的 Before 正文摘要与原提交相同；系统、历史和工具定义同时存在，不能整体保存回调正文 |
| Messages DD 模型改写 | 1 / 1 | 1 / 1 / 1 | 客户端编码模型在回调前变成 audit-test-model，Body 与 RequestedModel 都不再保留该原始模型名 |
| 相同 Key 两次同文重交，再用另一 Key 提交 | 3 / 3 | 3 / 3 / 3 | 同 Key 的 caller_scope 摘要一致、不同 Key 不同；三个执行 ID 均不同，不按正文去重 |
| 未知模型、路由前失败 | 1 / 0 | 0 / 0 / 0 | 已认证提交也可能完全不可见，插件无法据此统计漏采数量 |
| 同账号内部重试 | 1 / 2 | 1 / 2 / 1 | 同一执行 ID；After 会重复，Before 更适合作为执行记录入口 |
| 上游账号切换 | 1 / 2 | 1 / 2 / 1 | 两次上游尝试使用不同合成凭据，但仍属同一执行 ID |
| 其他插件在原 HTTP context 中发起嵌套执行 | 1 / 2 | 2 / 2 / 2 | 两个执行 ID、一个 HTTP TraceID；不能将全部执行直接当作客户端提交 |
| 上游失败、客户端真实取消，分别测试 | 每项 1 / 1 | 1 / 1 / 1 | 已观察输入不依赖模型成功或客户端持续连接 |
| 普通 Responses WebSocket，同一连接连续四次提交 | 每次 1 / 1 | 每次 1 / 1 / 1 | 四个执行 ID、一个 TraceID；四次 Before 正文分别包含 1、2、3、4 个历次用户标记，证明已补回历史 |
| Codex duplex 首条 create | 1 / 1 | 1 / 1 / 0 | 完成通知不是每条消息结束时发出 |
| 同连接后续 append | 1 / 1 | 0 / 0 / 0 | 客户端完成事件及上游接收标记均已确认，仍无执行回调 |
| 同连接 steer | 1 / 1 | 0 / 0 / 0 | 已确认上游收到并返回 steering 事件，仍无执行回调 |
| duplex 连接正常关闭 | 0 / 0 | 0 / 0 / 1 | 初始执行的生命周期延续至连接结束 |

所有实例均验证：管理 Key 能读取报告；匿名与两个普通客户端 Key 不能读取；无数据静态资源可公开访问且不包含 events。报告也验证了回调可携带认证请求头，因此正式改造不能整体保存 headers。`caller_scope` 只在本轮内置客户端 Key 环境验证，不能由此宣称支持任意第三方认证来源或原版完整匿名 Key 目录。

## 对正式改造的影响

### 可以继续使用的模块

现有正文白名单解析、截断、异步存储、预算、保留期、组合查询和大部分管理页面可复用。探针没有修改这些生产模块。

### 若采用官方插件版，必须明确的变化

1. 产品范围改为“进入模型执行的输入记录”，不承诺所有已认证客户端提交；路由前失败覆盖数未知。
2. 模型筛选采用执行入口模型，不能保证原始客户端模型名。
3. 对象目录改为已观察对象或另行设计的目录；身份映射、旧历史关联不能直接使用本实验摘要代替。
4. 普通重试可用执行 ID 避免重复；嵌套执行需要另外区分。不能把 TraceID 当通用提交 ID，尤其不能按它合并 WebSocket 消息。
5. 不支持把 Codex duplex 当作完整逐消息输入记录；普通 WS 也不能直接将恢复后的正文当作本次原始帧。
6. 关键词查询需改用 POST 正文，避免原版 URL 访问日志保存 q；此点来自前期源码评估，本轮没有改动正式搜索接口。
7. 界面显示明确的能力范围，不能将“没有收到回调”显示成“没有漏采”。

**建议**：若主要需求是常规 HTTP 事后人工查看，可以进入官方插件版设计；若 Codex WebSocket 完整输入采集是硬要求，不应继续把纯执行拦截插件作为等价替代，应评估独立前置采集代理或保留入站观察补丁。本实验不擅自降低现有 spec 或 ADR 的要求。

## 失败、修正与未验证范围

- 首轮实验驱动失败：503 夹具错误复用了 invalid_request_error，官方正确拒绝重试；修正为 server_error 后，两类真实重试各两轮通过。
- 首轮 duplex 驱动错误地在连接活跃时等待 complete；改为逐消息读取观测、关闭连接后等待生命周期终结，实际证明其连接级行为，没有把缺少逐消息回调改成成功。
- 首次 Linux 源码包携带 macOS AppleDouble 文件，快速格式检查报告 NUL；没有删除旧目录，在全新目录用不含扩展属性的源码包复测通过。该次没有进入系统实验。
- 当前结论只对应上述固定官方源码与本轮合成矩阵。未运行 Docker 镜像内装载、Linux arm64、完整管理 UI 浏览器回归、全量内容/工具续跑矩阵、生产流量和性能、其他版本或 fork。
- 两端实验宿主均已退出，未遗留本轮宿主进程；所有实验目录保留，未自动删除。

## 有限证据位置

- macOS：`.build/official-probe.wmrEu4/{build.json,result.json,test.log}`。
- Linux 取回的有限结果：`.test-data/official-probe-linux-result.dguJzc/{build.json,result.json,run.log}`；run.log 仅为验证驱动输出，不是宿主请求日志。
- Linux 独立源码：`/opt/cpa-linux-verify.IZCb2S/official-probe.3djlCY/source/`。
- Linux 独立产物：上述源码目录下 `.build/official-probe.oaPh1q/`；驱动输出位于兄弟目录 `evidence/run.log`。
- 初次失败目录与源码包保留供定位，不作为最终通过证据。
