# 固定基线构建与隔离本地加载

本文说明管理员如何从固定源码构建、加载普通原生插件并检查本地实例；不执行生产部署。实际平台结果见[交付验收记录](verification/delivery-platform-validation-2026-10-15.md)。

## 兼容条件不是单一版本号

| 层次 | 必须满足的条件 |
| --- | --- |
| 官方源码基线 | CLIProxyAPI `v8.0.27`，commit `bf9341629ef2300ac9d846a2e80fe2756f63a671` |
| 官方 native 合同 | C ABI `1`、RPC schema `6`；宿主和动态库须为相同目标平台／架构 |
| 本项目新增观察合同 | `host/manifest.json` 的独立契约 `3`；Chat Completions、Responses HTTP、Anthropic Messages、Responses WebSocket 四协议能力齐备 |

仅显示 `v8.0.27` 不能证明二进制包含原生插件能力或本项目补丁。未应用补丁的官方宿主、其他 fork、旧契约或缺少任一协议都不能当作受支持的全覆盖宿主；不兼容时停采并诊断，不降级到有限执行钩子。

## 构建

在仓库根目录使用 Go1.27.2、CGO、对应架构C编译器、Git、Python3、tar及patch：

```bash
make build
bash scripts/fetch-management-ui.sh
```

`scripts/build.sh` 核对官方commit和清单中的补丁SHA256，从精确Git对象导出**全新目录**，先dry-run再以 `--fuzz=0` 应用最小观察补丁，原生成对构建：

- 宿主：`.build/bin/cliproxyapi`。
- macOS：`.build/plugins/request-input-audit.dylib`。
- Linux amd64／arm64：`.build/plugins/request-input-audit.so`。

首次构建会获取固定宿主源码与Go模块；不执行下载的安装脚本。已有目录和构建源码保留，不自动清理。这里的可复现指固定输入与可重做步骤，不承诺不同路径／工具链构建的二进制摘要逐字节相同。

`make build-retention-test` 和 `make build-compatibility-test` 是独立验收产物；`tests/fixtures/http-once-plugin` 是内部执行测试插件。**不要把它们复制到管理员的普通插件目录**。宿主与插件分别交叉编译成功也不等于该架构已加载／运行成功。

## 不依赖 demo 的独立加载

下面只创建新的合成配置，监听回环地址，不使用真实Key、上游凭据、auth目录或现有数据卷。确认端口 `18317` 未被占用，勿改成公网监听。

```bash
ROOT="$(pwd -P)"
mkdir -p "$ROOT/.test-data"
RUN="$(mktemp -d "$ROOT/.test-data/manual-delivery.XXXXXX")"
mkdir -m 700 "$RUN/plugins"
case "$(go env GOOS)" in
  darwin) SUFFIX=dylib ;;
  linux) SUFFIX=so ;;
  *) printf '%s\n' '本说明不覆盖此平台'; exit 1 ;;
esac
cp "$ROOT/.build/plugins/request-input-audit.$SUFFIX" "$RUN/plugins/"
python3 -I - "$RUN" <<'PY'
# 只生成新合成配置；JSON引号同时是合法YAML字符串，不读取现有凭据。
import json, os, pathlib, sys
run = pathlib.Path(sys.argv[1])
path = run / 'config.yaml'
with path.open('x') as output:
    output.write(f'''config-version: 8
server:
  host: 127.0.0.1
  port: 18317
management:
  secret-key: synthetic-admin-delivery-only
  disable-control-panel: false
  disable-auto-update-panel: true
access:
  api-keys: [synthetic-client-delivery-only]
auth-dir: {json.dumps(str(run / 'auths'))}
logging-to-file: false
request-log: false
plugins:
  enabled: true
  dir: {json.dumps(str(run / 'plugins'))}
  configs:
    request-input-audit:
      enabled: true
      state-dir: {json.dumps(str(run / 'audit'))}
      budget-bytes: 1073741824
''')
os.chmod(path, 0o600)
PY
printf '本轮隔离目录：%s\n' "$RUN"
(cd "$RUN" && MANAGEMENT_STATIC_PATH="$ROOT/.build/assets/management-v1.26.2" \
  "$ROOT/.build/bin/cliproxyapi" --config "$RUN/config.yaml" --local-model)
```

保持该终端运行。宿主可能会按自身配置更新这个**新合成配置**，不会接触真实 `.env`。`--local-model` 不保证完全离线：官方宿主仍可能发起既有版本等后台网络请求。本步骤没有配置真实提供商；需要真正完全禁网时另用部署环境的网络策略。

### 成功判据

1. 打开 `http://127.0.0.1:18317/management.html`，以合成管理Key `synthetic-admin-delivery-only` 登录，进入“输入审计”。公开静态资源只是无数据shell，不代表已鉴权。
2. 状态应显示采集可用；受保护 `/status` 的 `available=true`、`supported_protocols` 包含四协议、`responses_websocket=true`，且无观察版本／桥接失败。默认名单为空，尚无记录。若插件未加载、ABI不符或管理注册失败，应检查宿主有限诊断，不以模型端口能访问推断审计成功。
3. 对象目录只出现无秘密对象标识。选择本轮对象并命名，不能输入原客户端Key当作对象ID。
4. 在另一终端发起合成请求：

   ```bash
   curl -sS -H 'Authorization: Bearer synthetic-client-delivery-only' \
     -H 'Content-Type: application/json' \
     --data '{"model":"synthetic-no-provider","messages":[{"role":"system","content":"不得采集的合成指令"},{"role":"user","content":"本轮独立加载验证输入"}]}' \
     http://127.0.0.1:18317/v1/chat/completions
   ```

   **预期模型HTTP400**：本步骤故意未配置提供商，验证路由前失败仍被观察，不冒充模型成功调用。管理列表／详情应最终出现一条“本轮独立加载验证输入”，协议为Chat Completions、模型为原请求名，不含system指令。等待明确保存结果，不把HTTP结束或入队当作已保存。
5. `/v0/management/plugins/request-input-audit/status`、`/objects`、`/settings`、`/records`、`/detail` 都需要管理Key；匿名、上述客户端Key或对象ID不能读取保护数据。正文按纯文本显示，不自动加载其中的链接、图片或HTML。
6. 以 `Ctrl-C` 正常停止；保留本轮配置、状态和数据目录，不把未获授权的清理命令夹在操作末尾。需要重启时沿用同一专用 `audit` 目录和 `identity.seed`，不能仅拷正文或删除seed重新初始化。

上述手动步骤只证明独立加载和路由前采集。三协议正常、SSE、失败／重试、WebSocket、浏览器、预算及故障的完整验证使用仓库合成上游：

```bash
make check
make build build-retention-test build-compatibility-test
make test-one TEST='TestHTTPProtocols|TestResponsesWebsocket'
make test
make tools-test
make browser
```

`make test` 包含真实宿主运行，不是只编译测试。macOS磁盘夹具使用新建64MiB镜像；Linux不把这两项平台skip记为通过，应显式补运行下节入口。

## Linux真实磁盘故障

只在获准的隔离Linux环境执行，先以普通用户构建普通宿主／插件。调用者显式提供绝对源码路径、已新建的空证据目录及既有非root用户：

```bash
SOURCE=/absolute/path/to/isolated/source
RUN_USER=your-existing-test-user
OUTPUT="$(mktemp -d "$(dirname "$SOURCE")/disk-evidence.XXXXXX")"
sudo make -C "$SOURCE" verify-linux-disk \
  SOURCE="$SOURCE" OUTPUT="$OUTPUT" RUN_USER="$RUN_USER"
```

可先追加 `--validate-only` 调用 `scripts/verify-linux-disk.sh` 检查参数；不挂载、不启动宿主。控制器只在新私有mount namespace中创建64MiB tmpfs，隔离挂载传播，核验宿主实际非root UID，实测ENOSPC／EROFS；不自动sudo、不操作物理磁盘、不强制卸载、不删除既有目录。该入口不会由 `make check`／`make verify` 自动提权执行。

只读场景是**停止写入者、只读重挂、重新启动**，不证明运行中热切。ENOSPC可能损坏未同步尾行，释放空间后继续停采并报告真实错误是正确结果，不为通过测试自动截短历史。仅容量预算暂停自动恢复，不把真实I/O损坏改写为健康。卸载前有界保留合成状态，有限结果仅在正常卸载后记为通过；全部测试目录保留。

## 数据与故障边界

- 正文明文保存，应用层不加密。专用目录 `0700`、文件 `0600` 不能阻止主机高权限、数据卷、快照或备份读取者读取正文；管理员负责主机／卷／备份访问与外部副本保留。
- 普通文本主动粘贴Key、秘密、URL或Base64不会自动脱敏；非文本载荷、历史、工具内容等白名单外数据不会因此变成允许采集内容。
- `identity.seed` 和数据目录一起保护／备份；缺失、损坏或权限不合规会停采，不能以轮换seed掩盖问题。目录祖先不能是符号链接，macOS须使用真实路径。
- 到期隐藏和空间回收不等于安全擦除，不控制文件系统、磁盘、快照或备份残留；不提供提前清空未过期历史的入口。
- native插件与宿主同进程，不能隔离非法内存访问、进程退出或整机故障。常规解析、队列、存储及接口错误fail-open，不保证宿主故障时页面可访问、告警必定持久化或未落盘副本重放。
- 不承诺exactly-once、零丢失、零延迟、请求吞吐、搜索延迟或连续高峰七天完整保存。1GiB预算与默认七天是策略，不是任意流量下的保存保证。
