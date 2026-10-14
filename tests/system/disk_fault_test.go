// 本文件通过专用小型磁盘镜像制造真实 ENOSPC 和只读卷；不操作宿主物理磁盘，不修改生产代码。
package system

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

type testVolume struct {
	directory, image, mount string
	attached                bool
}

// createVolume 创建最多64MiB的新镜像并挂在项目专用目录；返回卷句柄，命令失败终止测试。
// 测试只卸载本次镜像，不删除镜像、验收数据或其他卷。
func createVolume(t *testing.T) *testVolume {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("此真实卷故障接缝仅在 macOS 使用 hdiutil；其他平台需相应外部卷夹具")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".test-data"), 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".test-data"), "disk-fault-")
	if err != nil {
		t.Fatal(err)
	}
	v := &testVolume{directory: dir, image: filepath.Join(dir, "isolated-64MiB.dmg"), mount: filepath.Join(dir, "volume")}
	if err := os.Mkdir(v.mount, 0700); err != nil {
		t.Fatal(err)
	}
	volumeCommand(t, "create", "-size", "64m", "-fs", "HFS+", "-volname", "CPA-Audit-Synthetic", "-type", "UDIF", "-nospotlight", v.image)
	v.attach(t, false)
	// 一旦挂载成功就注册卸载，后续安全检查失败也不遗留挂载；只涉及本次镜像。
	t.Cleanup(func() {
		if v.attached {
			out, err := exec.Command("/usr/bin/hdiutil", "detach", v.mount).CombinedOutput()
			if err != nil {
				t.Errorf("本次验收卷卸载失败: %v (%s)", err, out)
			} else {
				v.attached = false
			}
		}
	})
	mounted, err := os.Stat(v.mount)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.Stat(v.directory)
	if err != nil {
		t.Fatal(err)
	}
	mountStat, okMount := mounted.Sys().(*syscall.Stat_t)
	parentStat, okParent := parent.Sys().(*syscall.Stat_t)
	if !okMount || !okParent || mountStat.Dev == parentStat.Dev {
		t.Fatal("镜像未形成独立设备，禁止继续填充")
	}
	var capacity syscall.Statfs_t
	if err := syscall.Statfs(v.mount, &capacity); err != nil {
		t.Fatal(err)
	}
	if capacity.Blocks*uint64(capacity.Bsize) > 64<<20 {
		t.Fatal("镜像卷容量超过64MiB，禁止填充")
	}
	return v
}

// volumeCommand 仅执行测试拥有的镜像创建/挂载/卸载命令；args 是专用路径和固定选项，无返回值，失败终止测试。
func volumeCommand(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("/usr/bin/hdiutil", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("隔离镜像命令失败: %v (%s)", err, out)
	}
}

// attach 将本次镜像挂载到新建空目录；readonly 选择真实只读挂载，成功无返回，命令失败终止测试。
func (v *testVolume) attach(t *testing.T, readonly bool) {
	t.Helper()
	args := []string{"attach", "-mount", "required", "-nobrowse", "-noautoopen", "-owners", "on", "-mountpoint", v.mount}
	if readonly {
		args = append(args, "-readonly")
	}
	args = append(args, v.image)
	volumeCommand(t, args...)
	v.attached = true
}

// fillUntilENOSPC 在审计目录之外填充同一限定卷；仅在内部核实真实 ENOSPC 后返回实际写入字节。
// t 接收验收证据，其他错误或超过64MiB仍未满终止测试，不返回未经确认的成功。
func (v *testVolume) fillUntilENOSPC(t *testing.T) int64 {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(v.mount, "external-filler.bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	block := bytes.Repeat([]byte{0x5a}, 4096)
	var written int64
	for written < 64<<20 {
		n, err := f.Write(block)
		written += int64(n)
		if errors.Is(err, syscall.ENOSPC) {
			t.Logf("真实卷写入返回 ENOSPC；外部填充%d字节", written)
			return written
		}
		if err != nil {
			t.Fatalf("不是预期ENOSPC: %v", err)
		}
		if n != len(block) {
			t.Fatal("无errno的短写，不能声称真实ENOSPC")
		}
	}
	t.Fatal("限定64MiB镜像未产生ENOSPC")
	return 0
}

// controlledDiskUpstream 创建本地合成上游；expected 用于核对完整文本，返回服务器及逐次请求的校验结果通道。
// 不匹配返回400供公开客户端发现，读取错误记录测试失败；关闭由调用方负责。
func controlledDiskUpstream(t *testing.T, expected string) (*httptest.Server, chan bool) {
	t.Helper()
	received := make(chan bool, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			received <- false
			http.Error(w, "synthetic read failure", 400)
			return
		}
		var request struct {
			Messages []struct{ Role, Content string }
		}
		valid := json.Unmarshal(body, &request) == nil && len(request.Messages) > 0 && request.Messages[len(request.Messages)-1].Content == expected
		received <- valid
		if !valid {
			http.Error(w, "synthetic body mismatch", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-disk","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	return server, received
}

// sendDiskSubmission 提交完整合成请求并确认真正上游收到未修改文本；返回HTTP状态，网络或不匹配终止测试。
func sendDiskSubmission(t *testing.T, h *instance, body []byte, received <-chan bool) int {
	t.Helper()
	status, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
	select {
	case good := <-received:
		if !good {
			t.Fatal("审计故障修改了实际上游输入")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("故障后可控上游没有收到请求")
	}
	if status != 200 {
		t.Fatalf("审计故障不应改变成功模型响应，实际%d", status)
	}
	return status
}

// TestRealENOSPCFailOpen 验证真实小卷写满时拒绝保存失败副本、显示故障且模型请求与旧记录不受影响。
func TestRealENOSPCFailOpen(t *testing.T) {
	v := createVolume(t)
	text := strings.Repeat("disk-full-synthetic-", 4096)
	upstream, received := controlledDiskUpstream(t, text)
	defer upstream.Close()
	h := startHost(t, 1<<30, true, upstream.URL+"/v1", filepath.Join(v.mount, "audit"))
	h.monitorAll(t)
	body, _ := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": text}}})
	sendDiskSubmission(t, h, body, received)
	before := h.waitRecords(t, 1)[0]
	filled := v.fillUntilENOSPC(t)
	sendDiskSubmission(t, h, body, received)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, raw := h.request(t, "GET", api+"/status", adminKey, nil)
		var status struct {
			Available bool   `json:"available"`
			Reason    string `json:"reason"`
			State     struct {
				State, Reason string
				Saved         int64             `json:"saved"`
				Queued        int64             `json:"queued"`
				Gaps          map[string]uint64 `json:"gaps"`
			} `json:"state"`
		}
		if json.Unmarshal(raw, &status) != nil {
			t.Fatal("管理状态无法读取")
		}
		if status.State.State == "paused" && status.State.Queued == 0 {
			if status.State.Reason != "storage_error" || status.State.Gaps["storage_error"] == 0 || status.State.Saved != 1 {
				t.Fatal("真实写满未显示存储缺口，或失败副本被当作已保存")
			}
			t.Logf("ENOSPC验收：外部填充%d字节，模型HTTP200；saved=%d queued=%d storage_error=%d", filled, status.State.Saved, status.State.Queued, status.State.Gaps["storage_error"])
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("真实ENOSPC后未停采并产生可见诊断（available=%v reason=%s）", status.Available, status.Reason)
		}
		time.Sleep(20 * time.Millisecond)
	}
	after := h.waitRecords(t, 1)[0]
	if before.ID != after.ID {
		t.Fatal("真实写满挤掉原来已保存记录")
	}
}

// TestRealReadOnlyVolumeFailOpen 验证已有审计卷真实只读重挂后停止新采集并公开诊断，不影响模型调用。
func TestRealReadOnlyVolumeFailOpen(t *testing.T) {
	v := createVolume(t)
	text := "read-only-synthetic-input"
	upstream, received := controlledDiskUpstream(t, text)
	defer upstream.Close()
	stateDir := filepath.Join(v.mount, "audit")
	h := startHost(t, 1<<30, true, upstream.URL+"/v1", stateDir)
	h.monitorAll(t)
	body, _ := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": text}}})
	sendDiskSubmission(t, h, body, received)
	h.waitRecords(t, 1)
	h.stop()
	volumeCommand(t, "detach", v.mount)
	v.attached = false
	v.attach(t, true)
	probe, err := os.OpenFile(filepath.Join(v.mount, "readonly-probe"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if probe != nil {
		_ = probe.Close()
	}
	if !errors.Is(err, syscall.EROFS) {
		t.Fatalf("未取得真实只读卷 EROFS 证据: %v", err)
	}
	restarted := startHost(t, 1<<30, true, upstream.URL+"/v1", stateDir)
	sendDiskSubmission(t, restarted, body, received)
	_, raw := restarted.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		Available bool                           `json:"available"`
		Reason    string                         `json:"reason"`
		State     struct{ State, Reason string } `json:"state"`
	}
	if json.Unmarshal(raw, &status) != nil || (status.Available && status.State.State != "paused") || (status.Reason == "" && status.State.Reason == "") {
		t.Fatal("真实只读卷被伪装为采集正常")
	}
	t.Logf("EROFS验收：真实只读写入拒绝，模型HTTP200；available=%v reason=%s collector=%s", status.Available, status.Reason, status.State.State)
}
