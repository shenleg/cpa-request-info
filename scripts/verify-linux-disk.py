#!/usr/bin/env python3
"""在私有64MiB tmpfs验收真实Linux ENOSPC/EROFS；普通用户运行宿主，保留有限合成状态，正常卸载，不删除文件或自动提权。"""
import argparse
import pathlib
import os
import pwd


import errno
import json
import signal
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import ctypes
import shutil
import tempfile
import sys

ADMIN = "synthetic-admin-audit-test-only"
CLIENT = "synthetic-client-audit-a-only"
API = "/v0/management/plugins/request-input-audit"
TEXT = "LINUX-DISK-UNMODIFIED-SYNTHETIC-" * 3072
received = []


class Upstream(BaseHTTPRequestHandler):
    """核对实际模型输入并返回固定回复；不写请求日志，读取或结构错误返回400。"""

    def do_POST(self):
        """接收模型提交并记录输入一致性；返回合成HTTP回复，异常交给服务端报告。"""
        body = self.rfile.read(int(self.headers["Content-Length"]))
        request = json.loads(body)
        valid = request["messages"][-1]["content"] == TEXT
        received.append(valid)
        self.send_response(200 if valid else 400)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"id":"linux-disk-synthetic","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}')

    def log_message(self, format, *args):
        """关闭HTTP库默认请求日志；无返回值或异常。"""


def request(base, path, method="GET", payload=None, key=ADMIN):
    """调用本地公开接口；返回HTTP状态和JSON或原字节，传输错误直接失败。"""
    raw = None if payload is None else json.dumps(payload).encode()
    req = urllib.request.Request(base + path, data=raw, method=method, headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"})
    try:
        response = urllib.request.urlopen(req, timeout=10)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        body = response.read()
        try:
            result = json.loads(body)
        except (ValueError, UnicodeError):
            result = body
        return response.status, result


def wait_for(check, description):
    """轮询本轮可观测结果；返回满足条件的值，15秒内未满足抛出明确错误。"""
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(0.05)
    raise AssertionError(description)


def gap_item(alerts, reason):
    """从公开gap_alerts取得指定原因摘要；返回摘要，字段缺失或原因不存在时明确失败，不输出响应正文。"""
    for item in alerts["items"]:
        if item["reason"] == reason:
            return item
    raise AssertionError("公开gap_alerts缺少本轮磁盘故障原因")


def public_status(address):
    """读取本轮公开状态及提醒快照；返回状态，HTTP失败或gap_alerts缺失时终止验收。"""
    code, state = request(address, API + "/status")
    assert code == 200, "公开状态不可读取"
    assert state["state"]["gap_alerts"]["generation"], "提醒快照缺少generation"
    return state


def port():
    """取得回环临时端口；返回端口号，监听失败直接抛错。"""
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def stop(process):
    """停止本次创建的宿主进程组；等待退出，超过期限强制回收，不删除文件。"""
    if process.poll() is None:
        os.killpg(process.pid, signal.SIGINT)
        try:
            process.wait(timeout=8)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)


def run_case(args, user, work):
    """验收一种真实磁盘故障；参数限定源码、用户及本次工作目录，返回有限摘要，契约失败抛错并回收宿主。"""
    global received
    received = []
    mount = work / "volume"
    expected = 64 << 20
    assert mount.is_mount() and mount.stat().st_dev != work.stat().st_dev, "not_isolated_mount"
    fs = os.statvfs(mount)
    assert fs.f_blocks * fs.f_frsize == expected, "volume_not_64MiB"
    server = ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    host_port = port()
    address = "http://127.0.0.1:" + str(host_port)
    source = args.source
    config = work / "config.yaml"
    contents = f'''config-version: 8
server:
  host: 127.0.0.1
  port: {host_port}
management:
  secret-key: {json.dumps(ADMIN)}
  disable-control-panel: true
  disable-auto-update-panel: true
access:
  api-keys: [{json.dumps(CLIENT)}]
auth-dir: {json.dumps(str(work / "auths"))}
logging-to-file: false
request-log: false
plugins:
  enabled: true
  dir: {json.dumps(str(source / ".build/plugins"))}
  configs:
    request-input-audit:
      enabled: true
      state-dir: {json.dumps(str(mount / "audit"))}
      budget-bytes: 1073741824
openai-compatibility:
  - name: controlled
    base-url: http://127.0.0.1:{server.server_port}/v1
    api-key-entries:
      - api-key: synthetic-upstream-only
    models:
      - name: audit-test-model
        alias: audit-test-model
'''
    config.write_text(contents)
    config.chmod(0o600)
    os.chown(config, user.pw_uid, user.pw_gid)
    log = (work / "host.log").open("xb")
    def start_host():
        """以普通用户启动本轮宿主；返回独立进程组句柄，启动失败抛错，不继承真实凭据。"""
        return subprocess.Popen(["setpriv", "--reuid=" + str(user.pw_uid), "--regid=" + str(user.pw_gid), "--clear-groups", str(source / ".build/bin/cliproxyapi"), "--config", str(config), "--local-model"], cwd=work, env={"PATH": os.environ["PATH"], "HOME": str(work), "TMPDIR": str(work)}, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
    process = start_host()
    try:
        def ready():
            """读取宿主本轮受保护入口判断就绪；仅连接尚未建立可重试，其余错误直接失败。"""
            if process.poll() is not None:
                raise AssertionError("合成宿主提前退出")
            try:
                code, state = request(address, API + "/status")
                if code == 200:
                    identity = next(line for line in pathlib.Path(f"/proc/{process.pid}/status").read_text().splitlines() if line.startswith("Uid:"))
                    assert identity.split()[1:] == [str(user.pw_uid)] * 4, "被测宿主不是指定普通用户"
                return state if code == 200 else None
            except urllib.error.URLError:
                return None
        wait_for(ready, "宿主未就绪")
        payload = {"model": "audit-test-model", "messages": [{"role": "user", "content": TEXT}]}
        # 原版接口只发现实际执行的对象；首次请求未监控，不能把发现流量算成已保存记录。
        assert request(address, "/v1/chat/completions", "POST", payload, CLIENT)[0] == 200
        assert request(address, API + "/records/search", "POST", {})[1]["total"] == 0
        code, catalog = request(address, API + "/objects")
        assert code == 200 and len(catalog["objects"]) == 1
        object_id = catalog["objects"][0]["id"]
        assert request(address, API + "/monitor", "POST", {"object_id": object_id, "name": "Linux合成磁盘验收"})[0] == 200
        assert request(address, "/v1/chat/completions", "POST", payload, CLIENT)[0] == 200
        prior = wait_for(lambda: request(address, API + "/records/search", "POST", {})[1]["records"], "首条输入未同步保存")[0]
        prior_outputs = {entry.name: entry.read_bytes() for entry in (mount / "audit").glob("records-*.jsonl")}
        assert prior_outputs
        written = 0
        if args.mode == "enospc":
            fd = os.open(mount / "external-filler.bin", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            try:
                while written <= expected:
                    try:
                        written += os.write(fd, b"z" * 4096)
                    except OSError as error:
                        assert error.errno == errno.ENOSPC, "不是实际ENOSPC"
                        break
                else:
                    raise AssertionError("限定卷没有返回ENOSPC")
            finally:
                os.close(fd)
            failure_errno = "ENOSPC"
        else:
            # 与macOS既有场景一致：停止写入者再只读重挂，不能将EBUSY误称应用故障或强制卸载。
            stop(process)
            # LXC的mountinfo显示宿主映射UID；显式指定容器UID，避免libmount原样重放101000导致夹具失败。
            subprocess.run(["mount", "-t", "tmpfs", "-o", f"remount,ro,uid={user.pw_uid},gid={user.pw_gid}", "tmpfs", str(mount)], check=True)
            try:
                descriptor = os.open(mount / "readonly-probe", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            except OSError as error:
                assert error.errno == errno.EROFS, "不是实际EROFS"
            else:
                os.close(descriptor)
                raise AssertionError("只读挂载仍可写")
            failure_errno = "EROFS"
            process = start_host()
            wait_for(ready, "只读环境下宿主未启动")
        assert request(address, "/v1/chat/completions", "POST", payload, CLIENT)[0] == 200
        assert received == [True, True, True], "发现/故障场景改变或遗漏了实际模型输入"
        def failed():
            """等待本轮采集拒绝或故障可见；返回公开状态，不以模型请求结束推断审计落盘。"""
            code, state = request(address, API + "/status")
            assert code == 200
            inner = state["state"]
            return state if not state["available"] or inner.get("state") == "paused" else None
        wait_for(failed, "磁盘故障后采集仍伪装正常")
        state = public_status(address)
        inner = state["state"]
        alerts = inner["gap_alerts"]
        fault_reason = "storage_error" if args.mode == "enospc" else "storage_unavailable"
        fault = gap_item(alerts, fault_reason)
        assert fault["active"] is True and fault["history_retained"] is True, "磁盘故障缺少活动状态或已发生历史"
        if args.mode == "enospc":
            assert inner["reason"] == "storage_error" and inner["gaps"]["storage_error"] > 0
            assert inner["saved"] == 1 and inner["queued"] == 0
            assert fault["affected_count"] is not None and fault["affected_count"] >= 1, "ENOSPC没有确定受影响提交的公开摘要"
        else:
            assert state["available"] is False and inner["available"] is False, "只读启动未报告采集器不可用"
            assert inner["reason"] == "storage_unavailable", "只读启动未报告storage_unavailable"
            assert inner["diagnostics_persisted"] is False, "只读且无采集器时谎报诊断已持久化"
            assert {entry.name: entry.read_bytes() for entry in (mount / "audit").glob("records-*.jsonl")} == prior_outputs, "只读失败修改了原记录"
        # 固定诊断文件可原位覆盖；ENOSPC不必然使确认失败，不能将200误判为夹具不真实。
        ack_code, acknowledgement = request(address, API + "/gaps/acknowledge", "POST", {"generation": alerts["generation"], "revision": alerts["revision"]})
        assert ack_code in (200, 503), "磁盘故障确认未返回200或503"
        if ack_code == 200:
            assert acknowledgement["acknowledged"] is True, "确认200未报告已阅"
            if args.mode == "readonly":
                assert acknowledgement["diagnostics_persisted"] is False, "只读且无采集器时谎报确认已持久化"
        else:
            assert acknowledgement["error"] == "diagnostics_unavailable", "确认503未说明诊断不可持久化"
        after_ack = public_status(address)["state"]["gap_alerts"]
        if ack_code == 200:
            assert after_ack["generation"] == alerts["generation"] and after_ack["acknowledged_revision"] == alerts["revision"], "确认200没有标记本次展示的提醒版本"
        after_fault = gap_item(after_ack, fault_reason)
        assert after_fault["active"] is True and after_fault["history_retained"] is True, "确认已阅隐藏活动故障或删除历史"
        if args.mode == "enospc":
            assert after_fault["affected_count"] is not None and after_fault["affected_count"] >= 1, "确认已阅删除确定受影响次数"
        else:
            # 只读期间的新告警只在内存；先让同一宿主在rw下移交并持久化pending，再验证正常重启。
            # 若在彻底不可写时直接停机，无法保证该告警跨进程保存；这里不声称验证了这种保证。
            subprocess.run(["mount", "-t", "tmpfs", "-o", f"remount,rw,uid={user.pw_uid},gid={user.pw_gid}", "tmpfs", str(mount)], check=True)
            def pending_persisted():
                """等待同一宿主通过公开status恢复并同步pending历史；返回状态，缺口被删除或复活为活动故障时失败。"""
                repaired = public_status(address)
                if not repaired["available"]:
                    return None
                repaired_inner = repaired["state"]
                repaired_item = gap_item(repaired_inner["gap_alerts"], "storage_unavailable")
                assert repaired_item["history_retained"] is True and repaired_item["active"] is False, "恢复采集未保留只读历史或仍报告活动故障"
                return repaired if repaired_inner["diagnostics_persisted"] is True else None
            wait_for(pending_persisted, "恢复可写后pending历史未在同一宿主持久化")
            stop(process)
            process = start_host()
            wait_for(ready, "恢复可写后的正常重启未完成")
            restarted_fault = gap_item(public_status(address)["state"]["gap_alerts"], "storage_unavailable")
            assert restarted_fault["history_retained"] is True and restarted_fault["active"] is False, "正常重启丢失已持久化的只读历史或误报活动故障"
        code, records = request(address, API + "/records/search", "POST", {})
        assert code == 200 and records["total"] == 1 and records["records"][0]["id"] == prior["id"], "故障保存了新输入或损坏旧历史"
        code, detail = request(address, API + "/detail?id=" + prior["id"])
        assert code == 200 and detail["text"] == TEXT[:65536], "故障改变了旧输入正文"
        runtime_fault_visible = inner.get("reason") or state.get("reason")
        if args.mode == "enospc":
            stop(process)
            # 仅释放本轮独立tmpfs填充文件的内容，不删除文件或修改根文件系统。
            with (mount / "external-filler.bin").open("r+b") as filler:
                filler.truncate(0)
            process = start_host()
            wait_for(ready, "释放独立卷后宿主未恢复")
        recovered_alerts = public_status(address)["state"]["gap_alerts"]
        recovered_gap = any(item["history_retained"] and (item["reason"] == fault_reason or (item["reason"] in ("unclean_shutdown", "legacy_unknown", "recovery_unavailable") and item["affected_count"] is None and item["count_accuracy"] == "unknown")) for item in recovered_alerts["items"])
        assert recovered_gap, "恢复可写与正常重启抹掉了公开缺口历史或未知损失信号"
        assert request(address, API + "/detail?id=" + prior["id"])[1]["text"] == TEXT[:65536]
        # 恢复后再发同一模型输入，区分故障期fail-open与修复后模型/采集真正可用。
        assert request(address, "/v1/chat/completions", "POST", payload, CLIENT)[0] == 200
        assert received == [True, True, True, True], "发现/修复与重启改变或遗漏了实际模型输入"
        def repaired_saved():
            """等待修复后的第三次模型提交保存；返回记录列表，公开查询失败或失败副本被保存时终止验收。"""
            code, current = request(address, API + "/records/search", "POST", {})
            assert code == 200, "修复后记录查询失败"
            assert current["total"] <= 2, "故障期失败副本被冒充已保存"
            return current if current["total"] == 2 else None
        repaired_state = public_status(address)
        if repaired_state["state"].get("state") == "active":
            records = wait_for(repaired_saved, "修复后的模型输入没有恢复保存")
            capture_resumed = True
        else:
            # ENOSPC可能已写入部分尾行；释放空间不等于修复损坏，禁止夹具要求应用静默截断历史。
            assert args.mode == "enospc" and repaired_state["state"]["reason"] == "storage_error", "恢复后未给出真实停采原因"
            code, records = request(address, API + "/records/search", "POST", {})
            assert code == 200 and records["total"] == 1, "损坏尾部使失败副本冒充保存或损坏旧输入"
            capture_resumed = False
        result = {"runtime_fault_visible": runtime_fault_visible, "recovery_loss_signal": recovered_gap, "result": "passed", "mode": args.mode, "errno": failure_errno, "volume_bytes": fs.f_blocks * fs.f_frsize, "external_fill_bytes": written, "host_uid": user.pw_uid, "model_statuses": [200, 200, 200, 200], "discovery_requests": 1, "discovery_saved": False, "upstream_received": len(received), "upstream_invalid": sum(not entry for entry in received), "saved_records": records["total"], "old_detail_preserved": True, "capture_available": state["available"], "reason": inner.get("reason") or state.get("reason"), "private_mount_namespace": True, "readonly_scope": "restart" if args.mode == "readonly" else None, "old_detail_query_after_writable_restart": args.mode == "readonly"}
        # 新增证据仅为布尔摘要；不附公开响应全文、模型输入、认证材料或宿主日志。
        result.update({"gap_alerts_visible": True, "gap_fault_active_before_ack": True, "gap_fault_active_after_ack": True, "gap_ack_contract_valid": True, "gap_ack_succeeded": ack_code == 200, "gap_recovery_history_or_uncertainty_visible": recovered_gap, "model_input_unchanged_after_repair": True, "capture_resumed_after_repair": capture_resumed, "partial_tail_not_silently_repaired": not capture_resumed})
        if args.mode == "enospc":
            result["gap_storage_error_affected_count_positive"] = True
        else:
            result.update({"gap_readonly_ack_persistence_honest": True, "gap_readonly_pending_persisted_before_restart": True, "gap_readonly_history_survived_clean_restart": True, "unwritable_shutdown_persistence_guaranteed": False})
        return result
    finally:
        stop(process)
        log.close()
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)



def execute(args, user):
    """在新私有namespace中运行两项验收；参数指定源码、空证据目录及普通用户，无返回，挂载/验收/正常卸载失败抛错。"""
    for command in ("mount", "umount", "setpriv"):
        if shutil.which(command) is None:
            raise RuntimeError("missing_command: " + command)
    before = os.readlink("/proc/self/ns/mnt")
    libc = ctypes.CDLL(None, use_errno=True)
    # 只创建mount namespace，不改变其他宿主namespace；分离传播后才允许挂载。
    if libc.unshare(0x00020000) != 0:  # Linux CLONE_NEWNS，禁止在原namespace继续。
        number = ctypes.get_errno()
        raise OSError(number, os.strerror(number))
    subprocess.run(["mount", "--make-rprivate", "/"], check=True)
    assert os.readlink("/proc/self/ns/mnt") != before, "namespace_not_private"
    os.chmod(args.output, 0o700)
    os.chown(args.output, user.pw_uid, user.pw_gid)
    for mode in ("enospc", "readonly"):
        args.mode = mode
        work = pathlib.Path(tempfile.mkdtemp(prefix="disk-" + mode + "-", dir=args.output))
        os.chown(work, user.pw_uid, user.pw_gid)
        mount = work / "volume"
        mount.mkdir(mode=0o700)
        assert not mount.is_mount() and not any(mount.iterdir()), "mountpoint_not_new_empty"
        subprocess.run(["mount", "-t", "tmpfs", "-o", f"size=64m,mode=0700,uid={user.pw_uid},gid={user.pw_gid},nosuid,nodev,noexec", "tmpfs", str(mount)], check=True)
        result = None
        try:
            result = run_case(args, user, work)
        finally:
            # 仅保留有界合成状态，不复制填充文件；拷贝失败仍正常卸载并报告失败。
            try:
                audit = mount / "audit"
                if audit.is_dir():
                    files = list(audit.rglob("*"))
                    assert not any(p.is_symlink() for p in files), "unexpected_audit_symlink"
                    # 每场景至多两条64KiB正文；2MiB足够状态和损坏尾行，拒绝意外复制大量产物。
                    assert sum(p.stat().st_size for p in files if p.is_file()) <= 2 << 20, "evidence_exceeds_2MiB"
                    shutil.copytree(audit, work / "synthetic-audit-preserved")
            finally:
                subprocess.run(["umount", str(mount)], check=True)
        result["normal_unmount"] = True
        (args.output / ("disk-" + mode + "-result.json")).write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
        print(json.dumps(result, ensure_ascii=False), flush=True)


def main():
    """解析公开CLI参数；成功返回，无效参数以退出码2失败，不开始挂载。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", help="已成对构建正式宿主和插件的源码绝对路径")
    parser.add_argument("--output", help="本次专用、已新建且为空的证据目录绝对路径；运行后保留")
    parser.add_argument("--user", help="运行被测宿主的既有非root用户；控制器须显式以root运行")
    parser.add_argument("--validate-only", action="store_true", help="仅校验CLI、目录和产物存在性，不挂载、不启动宿主")
    args = parser.parse_args()
    for name in ("source", "output", "user"):
        if not getattr(args, name):
            parser.error("missing_" + name)
    try:
        user = pwd.getpwnam(args.user)
    except KeyError:
        parser.error("unknown_user")
    if user.pw_uid == 0:
        parser.error("root_application_forbidden")
    for name in ("source", "output"):
        value = pathlib.Path(getattr(args, name))
        if not value.is_absolute():
            parser.error(name + "_absolute")
        if any(part.is_symlink() for part in (value, *value.parents)):
            parser.error(name + "_symlink")
        if not value.is_dir():
            parser.error(name + "_directory_required")
        setattr(args, name, value.resolve())
    if any(args.output.iterdir()):
        parser.error("output_not_empty")
    host = args.source / ".build/bin/cliproxyapi"
    plugin = args.source / ".build/plugins/request-input-audit.so"
    if not host.is_file() or not os.access(host, os.X_OK) or not plugin.is_file():
        parser.error("missing_artifacts: run make build in --source first")
    if args.validate_only:
        print("validated: fixed 64MiB; no mount or application started")
        return
    if sys.platform != "linux" or os.geteuid() != 0:
        parser.error("linux_root_controller_required: invoke explicitly with sudo")
    execute(args, user)


def handle_termination(signum, frame):
    """将终止信号转换为异常；信号号决定退出码，确保finally停止本轮宿主并正常卸载。"""
    raise SystemExit(128 + signum)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, handle_termination)
    main()
