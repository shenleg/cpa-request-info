# 官方 CPA 插件构建与安装说明

本文说明正式精简版的构建、插件安装、发现启用和回滚，不执行生产部署。`0.2.0` 已完成 macOS arm64、Linux amd64 原生与浏览器验收，以及固定官方 Docker 镜像内非 root 加载、采集和重启验证；实际平台、限制及产物见[本轮交付记录](verification/official-http-input-2026-10-16.md)。

## 兼容条件

| 层次 | 当前要求 |
| --- | --- |
| 官方宿主 | CLIProxyAPI `v8.0.27`，commit `bf9341629ef2300ac9d846a2e80fe2756f63a671` |
| 原生契约 | C ABI `1` / RPC schema `6`，官方 `RequestInterceptor` + `Management` |
| 采集范围 | Before 所见的三协议外部 HTTP/SSE 执行；After 空修改，不采集 WebSocket |
| 动态库 | 与宿主 OS、CPU 架构兼容；Linux 还须兼容目标 libc |
| 管理前端 | 本轮验收使用 `v1.26.2`；插件页面内嵌于动态库，现有主管理页可用时不必另行挂载 HTML |

契约入口为 `host/manifest.json` 和 [ADR-0004](adr/0004-official-http-execution.md)。不再需要私有观察合同或宿主补丁，不承诺未来 CPA、其他 fork、任意 libc 或其他认证来源兼容。当前验收覆盖内置客户端 Key；固定管理前端 `v1.26.2` 已完成本轮双端真实浏览器验收。

## 先构建插件，不必重编用户 CPA

在仓库根目录使用 Go ≥ 1.26、CGO 和目标平台 C 编译器：

```bash
make build-plugin
```

产物只有正式插件构建所需文件；安装使用以下动态库：

- macOS：`.build/plugins/request-input-audit.dylib`。
- Linux：`.build/plugins/request-input-audit.so`。

该命令不获取、修改或重编宿主。macOS 库不能复制到 Linux 使用；Linux 库必须在与目标架构/libc 兼容的环境中构建，特别注意宿主机与容器可能不同。交叉编译成功不等于在目标宿主已加载、运行。

本轮已验证的 Linux amd64 库保留在 `.build/distribution/http-execution-0.2.0-linux-amd64.94Rl4a/request-input-audit.so`，同目录有 `SHA256SUMS` 和 `verification.json`；该路径是本次本地产物，不是已公开发布的下载地址。Linux arm64 精简版尚未验收。

开发与系统验收才使用：

```bash
make build
```

`scripts/build.sh` 检查固定 commit 和 `patches: []`，从精确 Git 对象全新导出官方源码，不使用缓存工作树的修改、不应用 patch，再构建开发宿主 `.build/bin/cliproxyapi` 和插件。完整构建另需 Git、Python 3、tar，首次会获取固定源码及 Go 模块；不运行下载的安装脚本，不自动清理源码或数据。**安装插件不需要拿开发宿主替换用户 CPA**。

`make build-retention-test` 使用另一个精确官方宿主及 `audittest` 时钟插件，不修改宿主读取器。此产物和其他测试夹具不能安装到用户插件目录。旧私有 `build-compatibility-test` / compat-overlay 不参与正式默认验收。

## 安装前保存原配置

先确认目标确为固定官方版本，并保留原镜像引用/摘要或二进制、Compose、配置、环境设置和原插件目录的恢复副本。现有 `.env`、认证 Key、auth 目录、端口、管理访问策略及其他插件均沿用，不以本文示例替换。更改生产配置或部署须单独授权。

给新插件分配一个**全新的专用 state-dir**。不要复用旧补丁版目录；旧 raw-Key 身份与新 caller-scope 身份不等价，没有自动迁移。

### 裸机或既有官方宿主

将对应平台动态库放入**现有原生插件目录**，保留其中其他插件。在现有配置中合并以下字段；`plugins.dir` 应填写现有插件目录，其他 `plugins.configs` 项继续保留：

```yaml
plugins:
  enabled: true
  dir: /absolute/path/to/existing/plugins
  configs:
    request-input-audit:
      enabled: true
      state-dir: /absolute/path/to/existing/plugins/request-input-audit-data
      budget-bytes: 1073741824
```

state-dir 必须对宿主运行用户可读写，目录 `0700`、文件 `0600`，祖先不能是符号链接；macOS 使用物理路径。每个实例各用自己的目录，不共享。

### Compose：只合并片段，不替换原文件

固定镜像为 **`eceasy/cli-proxy-api:v8.0.27`**，不是 `latest`，不自建 CPA 镜像。上游 Dockerfile 的工作目录为 `/CLIProxyAPI`，宿主可执行文件为 `/CLIProxyAPI/CLIProxyAPI`，官方 Compose 原生插件目录为 `/CLIProxyAPI/plugins`。本轮已验证 Linux amd64 镜像直接挂载正式库，以 UID/GID 1000 运行，采集、管理保护和重启恢复通过。已验收库最大 glibc 符号需求为 2.34；其他架构或基础镜像须另验。

推荐把动态库和该插件的专用数据子目录统一放在宿主机 `plugins-dir` 下，通过现有的一条插件目录挂载使用：

```text
${docker_home}/cpa/
├── conf/
│   └── config.yaml
├── auth-dir/
└── plugins-dir/
    ├── request-input-audit.so
    └── request-input-audit-data/
        ├── .audit.lock
        ├── caller-scope-v1.seed
        ├── monitors.json
        └── …其他审计数据
```

**首次安装时只准备空的 `request-input-audit-data` 目录；树中的锁、种子和配置由插件自行创建，不手工填写。** 动态库必须在插件扫描目录根层，CPA 不递归扫描子目录。数据子目录只放该插件状态，不混放 `.so`、HTML 或其他插件数据。

下面以已验收的镜像版本展示目录映射。沿用你现有的服务名、配置、认证、端口和其他挂载，不整段覆盖 Compose；不要为了照抄示例直接降级正在使用的 CPA。`${docker_home}` 沿用现有 Compose 变量。

```yaml
services:
  cli-proxy-api:
    image: eceasy/cli-proxy-api:v8.0.27  # 已验收示例，现有版本须先核实兼容性
    container_name: cpa
    restart: unless-stopped
    ports:
      - "8317:8317"
    volumes:
      - ${docker_home}/cpa/conf/config.yaml:/CLIProxyAPI/config.yaml
      - ${docker_home}/cpa/auth-dir:/root/.cli-proxy-api
      - ${docker_home}/cpa/plugins-dir:/CLIProxyAPI/plugins
```

这条 `plugins-dir` 挂载已经包含数据子目录，**不需要额外的数据挂载、命名卷或 `.so` 单文件挂载，也不需要额外的插件 HTML 挂载**。插件目录挂载必须可写；`request-input-audit-data` 为 `0700`、状态文件为 `0600`，所有者应为容器中 CPA 的实际运行 UID/GID，不能统一假定为1000或放宽成777。目录和祖先不能经过符号链接。

如果目前使用 `latest` 与 `pull_policy: always`，建议先记录当前正常版本与镜像摘要，再固定到该版本或摘要，避免重建时顺带升级；这不是要求直接改成示例版本。

在**现有 CPA 配置**相应映射中合并以下字段，不改管理 Key、客户端 Key 或其他认证设置：

```yaml
management:
  disable-auto-update-panel: true
plugins:
  enabled: true
  dir: /CLIProxyAPI/plugins            # 若已有其他原生目录，沿用实际目录并改挂载目标
  configs:
    request-input-audit:
      enabled: true
      state-dir: /CLIProxyAPI/plugins/request-input-audit-data
      budget-bytes: 1073741824
```

保留原 `management` 与 `plugins.configs` 的其他字段，不重复定义顶层映射。确认容器内实际生效的 `state-dir` 是新路径，而不只是修改了另一份宿主机配置。若 CPA 通用插件设置页提示“没有声明可视化配置字段”，通过这份 `config.yaml` 设置启动参数；它不表示插件没有独立管理页面。

### 前端如何加载

插件页面已编译在 `request-input-audit.so` 内，注册成功后由 CPA 在 `/v0/resource/plugins/request-input-audit/index.html` 提供，并出现在主管理页面的“输入审计”菜单中。**不用另外放置插件 `index.html`，也不用启动前端服务。**

`management.html` 是 CPA 主管理页面，不是插件页面。现有管理页面已经能打开插件菜单时，可以沿用，不必新增 HTML 挂载。若需要复现本轮固定 `v1.26.2` 管理页面，才单独准备该资源目录：运行 `bash scripts/fetch-management-ui.sh` 并按 `host/management-ui.json` 校验后，将产物目录挂载到自选静态路径，用 `MANAGEMENT_STATIC_PATH` 指向它，并关闭自动更新。不要把它放进审计数据子目录；页面位于插件目录本身也不会被 CPA 自动采用。未来或其他管理前端版本仍需验证。

### 已有精简版数据改放到子目录

如果原先使用 `/CLIProxyAPI/audit-http-execution` 且已经有对象或记录，不能只修改 `state-dir`，否则新目录会生成新身份，看不到原有监控配置和历史。

1. 正常停止 CPA，使写入排空。
2. 将原数据目录的**全部内容，包括隐藏文件、`caller-scope-v1.seed`、配置和记录**复制到新的 `plugins-dir/request-input-audit-data`。目标应为空，不合并两份独立审计数据；保留所有者和权限。
3. 修改 `config.yaml` 的 `state-dir`。如果 Compose 中有原数据目录的独立挂载，可移除该挂载项，但保留宿主机旧目录或旧命名卷，不删除数据。
4. 若修改了挂载或环境变量，使用 `docker compose up -d --no-deps --force-recreate --pull never --no-build cli-proxy-api` 重建服务；仅修改 CPA 配置时可使用 `docker compose restart cli-proxy-api`。服务名按实际填写，操作会短暂停服。
5. 确认状态可用，对象 ID、名称、监控状态和历史保持，再发送新的 HTTP 输入确认保存。旧目录继续保留，不能删除或重新生成 seed 来“修复”身份。

这是**同一精简版身份格式的目录迁移**，不是旧补丁版 `identity.seed` 的格式迁移。旧补丁版目录仍须保留并使用新的精简版空目录。

## 加载后的发现与启用

以下是应在隔离环境实际检查的步骤；不能把说明本身当作通过证据。

1. 通过 CPA 原有管理入口登录，进入“输入审计”菜单，确认页面明确是 **HTTP 执行记录**。插件页面由动态库提供，不要求单独 HTML 挂载；本轮主管理页登录复用以 `v1.26.2` 验收，不承诺其他前端版本兼容。
2. 用管理 Key 检查 `/v0/management/plugins/request-input-audit/status`：应为 `capture_mode=http_execution`，`supported_protocols` 为 `chat-completions`、`responses`、`anthropic-messages`，`responses_websocket=false`。状态可用不证明全部请求可见。
3. 默认名单为空。用目标客户端 Key 通过现有客户端发送一次**已成功路由到模型执行的 HTTP 请求**。首次发现不保存正文；未知模型或读体前失败可能根本没有回调，不能拿故意无提供商的 HTTP400 请求证明发现成功。无需等回复成功才采集，但必须确实进入 Before。
4. 在管理页选择本轮已观察匿名 ID、命名并启用，再发送新的输入。不要粘贴原 Key，不展示或凭 Key 片段认对象。目录不是配置中所有 Key 的列表，最多1024个内存匿名 ID；未选择对象重启丢失，已选择对象持久恢复。
5. 等待本轮唯一记录 ID 的精确详情确认已保存，不把模型回复、入队或旧记录当作本轮保存结果。只应看到末尾允许用户输入，不含历史、system/developer、工具结果、模型回复或附件 payload；文本秘密不会自动脱敏。时间为 Before 所见时刻，模型可能已被 DD 等逻辑改写。
6. 核查三协议 HTTP/SSE、内部嵌套排除、WebSocket 排除、管理鉴权、POST 搜索、预算/保留期及重启恢复。`source=plugin_host_model_callback` 的内部执行跳过；不按 TraceID 或正文合并。路由或读体在回调前失败不可见，不统计这部分遗漏。

插件使用 CPA 原端口。公开 `/v0/resource/plugins/request-input-audit/index.html` 只是无数据 shell；状态、对象、设置、搜索、详情等动态接口都要求管理 Key。普通客户端 Key、匿名对象 ID 或匿名访问不得读取。

列表与搜索使用 **POST `/v0/management/plugins/request-input-audit/records/search`**，筛选与关键词都在 JSON 正文，例如 `{"q":"本轮唯一输入","offset":0,"limit":20}`，另可加 `object_id`、`from`、`to`、`model`；详情仍为 GET `/v0/management/plugins/request-input-audit/detail?id=<recordID>`。不要用旧 GET 列表或把搜索词放进 URL；部署中的代理、正文日志、调试输出也不能保存搜索词。插件未修改官方宿主日志，不能声称已有模型 warning 已脱敏。

模型下拉选项由受管理鉴权保护的 GET `/v0/management/plugins/request-input-audit/models` 提供，来自全部已同步、未到期历史，不局限于当前页或其他筛选。选项保留原模型名称并精确匹配；不是当前可调用模型列表。

列表与详情的“思考强度”展示 Before 正文的请求值，等级、模式、预算分别表达；新记录无声明显示“未指定”，旧记录未采集显示“未记录”。不从模型后缀猜测，也不保证是 CPA 转换或配置覆盖后的最终值。详情“复制纯文本”只复制已保存正文，保留换行与空白，不附元数据；普通 HTTP 在缺 Clipboard API 时使用选区复制，浏览器拒绝或复制失败会提示手动复制。

## 数据保护与回滚

- `caller-scope-v1.seed` 为格式 magic + 32随机字节，取得 `flock` 后仅在除锁文件外为空的目录创建，并同步文件及目录。新 seed、配置、诊断和正文须作为**整个 state-dir** 持久保存、保护和备份，不只复制 JSONL。目录 `0700`、文件 `0600`，不能多实例共享。
- 旧 `identity.seed`、已有数据却缺新 seed、seed 损坏或权限不合规时明确停采拒绝。不删除、不迁移、不覆盖、不轮换，不用“换一个种子”修复旧目录。新格式不能当作旧对象身份继续使用。
- 默认预算1GiB、保留7天、正文上限64KiB，管理范围分别为4MiB–1GiB、1–30整天、1–65536字节。整个专用目录纳入预算；到期不可查不等于安全擦除，暂停和停机不冻结期限。
- 应用层明文保存，普通文本秘密不自动脱敏。主机高权限、卷、快照和备份读取者仍能读正文，管理员需保护这些副本。不要把合成测试 Key 复制到生产。
- 新字段 `request_reasoning` 可被本版本读取，旧记录缺字段保持原样；旧二进制的严格解码器不认识新字段，写入新记录后不能将同一 state-dir 直接交给旧插件降级，不自动改写或迁移。
- 出错先在管理页停止新采集；管理不可用时停用该插件或正常停止本实例，再恢复原配置/镜像/插件加载方式。保留原镜像和配置，以及新旧 state-dir/卷；不删除 volume，不自动清理数据，也不把新目录交给旧版插件打开。
- 恢复采集前核对故障和实际状态；提高预算只能解除符合条件的容量暂停，不能修复真实 I/O 或完整性错误。正常停止尽力排空，同进程崩溃可能丢失未落盘副本，未提供 native 崩溃隔离或 exactly-once。

## 隔离验收入口与证据

仅使用新合成配置、受控本地上游和独立结果目录，不接触生产凭据。可使用仓库 `make demo` / `make browser` 准备真实合成实例；演示密码只用于该实例，不是生产安装参数。

```bash
make deps
make browser-install
make check
make build build-retention-test
make test
make tools-test
make browser
```

命令定义以 `Makefile` 为准；本轮实际执行、平台、浏览器、容器及故障结果已记录在[交付记录](verification/official-http-input-2026-10-16.md)。原探针和旧票13的通过结果不能替代后续正式版验收。`legacy_patched` 仅保留历史用例，不用 skip 冒充当前兼容；默认不构建私有 compat-overlay。

Linux 真实磁盘故障需在获准的隔离环境另外执行，不因 macOS 夹具在 Linux skip 而算通过：

```bash
SOURCE=/absolute/path/to/isolated/source
RUN_USER=your-existing-test-user
OUTPUT="$(mktemp -d "$(dirname "$SOURCE")/disk-evidence.XXXXXX")"
sudo make -C "$SOURCE" verify-linux-disk \
  SOURCE="$SOURCE" OUTPUT="$OUTPUT" RUN_USER="$RUN_USER"
```

该入口要求调用者明确提供权限、绝对源码路径、新空证据目录和既有非 root 用户；可先调用 `scripts/verify-linux-disk.sh --source "$SOURCE" --output "$OUTPUT" --user "$RUN_USER" --validate-only` 检查参数。它使用私有 mount namespace 的64MiB tmpfs，不操作物理磁盘、不自动 sudo、不强制卸载、不删除文件。只读测试是停止写入者后只读重挂再启动，不冒充运行中热切。进程和挂载正常回收，证据文件保留。

官方宿主可能仍有既有后台网络请求，`--local-model` 不保证完全离线。两端自主验收、固定基点工作区审查、有限证据和独立提交授权遵循[验证与交付约定](agents/verification.md)。
