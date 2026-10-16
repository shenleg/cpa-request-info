// 本文件从公开采集接口验证官方 HTTP 输入的身份、白名单、停采和安全初始化；测试目录保留供检查。
package audit_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shenleg/cpa-request-info/internal/audit"
)

const scopeSeedFile = "caller-scope-v1.seed"
const scopeSeedMagic = "cpa-request-info:caller-scope-seed:v1\n"

// newAuditDirectory 为测试创建0700空目录并返回路径；失败终止测试，不注册自动删除。
func newAuditDirectory(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cpa-audit-unit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("保留测试目录：%s", dir)
	return dir
}

// openAudit 打开 dir 的公开采集器并注册关闭；budget 为初始预算，打开失败终止测试，不删除文件。
func openAudit(t *testing.T, dir string, budget int64) *audit.Collector {
	t.Helper()
	c, err := audit.Open(dir, budget)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// readAuditFile 返回 dir 内 name 的完整测试文件；读取失败终止测试，无写入或删除操作。
func readAuditFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// writeAuditFile 将 data 以0600写入 dir 内 name；写入失败终止测试，无返回值。
func writeAuditFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// waitSaved 等待异步写入达到 count 条并返回；超时或停采终止测试，不以排队数代替保存数。
func waitSaved(t *testing.T, c *audit.Collector, count int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := c.Status()
		if status.Saved == count && status.Queued == 0 {
			return
		}
		if status.State != "active" {
			t.Fatalf("采集意外停止：%+v", status)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待保存%d条超时：%+v", count, c.Status())
}

// monitorScope 通过未监控的 CaptureExecution 发现 scope 并启用，返回匿名身份；失败终止测试。
func monitorScope(t *testing.T, c *audit.Collector, scope string) string {
	t.Helper()
	id := c.CaptureExecution("discovery", scope, "responses", []byte(`{"input":"not monitored"}`))
	if len(id) != 64 {
		t.Fatalf("身份无效：%q", id)
	}
	if err := c.Monitor(id, "测试对象"); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestCaptureExecutionIdentity 验证身份按私有持久种子和scope域分隔派生，重启稳定、不同scope及目录互不相同。
func TestCaptureExecutionIdentity(t *testing.T) {
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, 0)
	scope := strings.Repeat("a", 64)
	id := monitorScope(t, c, scope)
	if other := c.CaptureExecution("other", strings.Repeat("b", 64), "responses", nil); other == id {
		t.Fatal("不同scope共用身份")
	}
	seed := readAuditFile(t, dir, scopeSeedFile)
	if len(seed) != len(scopeSeedMagic)+32 || !bytes.HasPrefix(seed, []byte(scopeSeedMagic)) {
		t.Fatal("种子缺少版本魔数或32字节秘密")
	}
	info, err := os.Stat(filepath.Join(dir, scopeSeedFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("种子权限不安全：%v", err)
	}
	mac := hmac.New(sha256.New, seed[len(scopeSeedMagic):])
	mac.Write([]byte("cpa-request-info:object-id:caller-scope:v1\x00"))
	mac.Write([]byte(scope))
	if id != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("身份不是预期的域分隔HMAC-SHA256")
	}
	if second, err := audit.Open(dir, 0); err == nil {
		second.Close()
		t.Fatal("已有flock时允许另一个初始化者进入")
	}
	c.Close()
	c = openAudit(t, dir, 0)
	if got := c.CaptureExecution("reopened", scope, "responses", []byte(`{"input":"saved"}`)); got != id {
		t.Fatalf("重启轮换身份：%s != %s", got, id)
	}
	waitSaved(t, c, 1)
	if !bytes.Equal(seed, readAuditFile(t, dir, scopeSeedFile)) {
		t.Fatal("重启或写入改写了种子")
	}
	other := openAudit(t, newAuditDirectory(t), 0)
	if other.CaptureExecution("independent", scope, "responses", nil) == id {
		t.Fatal("独立目录共用身份")
	}
}

// TestCaptureExecutionUnmonitored 验证默认未监控只派生身份，不解析、入队或保存正文，也不登记原scope。
func TestCaptureExecutionUnmonitored(t *testing.T) {
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, 0)
	scope := "unmonitored-caller-scope-secret"
	body := []byte("invalid-json-private-body-marker")
	first := c.CaptureExecution("first", scope, "chat-completions", body)
	if len(first) != 64 || c.CaptureExecution("second", scope, "responses", body) != first {
		t.Fatal("未监控身份不稳定")
	}
	status := c.Status()
	if status.Saved != 0 || status.Queued != 0 || len(c.Objects()) != 0 {
		t.Fatalf("未监控输入进入存储或对象表：%+v", status)
	}
	for reason, count := range status.Gaps {
		if count != 0 {
			t.Fatalf("预期跳过产生缺口：%s=%d", reason, count)
		}
	}
	c.Close()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data := readAuditFile(t, dir, entry.Name())
		if strings.HasPrefix(entry.Name(), "records-") || bytes.Contains(data, body) || bytes.Contains(data, []byte(scope)) {
			t.Fatalf("未监控输入或scope被保存到%s", entry.Name())
		}
	}
}

// TestCaptureExecutionHTTPWhitelist 验证三协议保留官方ID和允许内容，正文及scope不跨调用保存，时间由audit生成。
func TestCaptureExecutionHTTPWhitelist(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		body     string
	}{
		{"chat-completions", `{"model":"client-model","messages":[{"role":"system","content":"excluded-private-marker"},{"role":"user","content":"allowed"}],"ignored":"excluded-private-marker"}`},
		{"responses", `{"model":"client-model","input":"allowed","instructions":"excluded-private-marker"}`},
		{"anthropic-messages", `{"model":"client-model","messages":[{"role":"user","content":[{"type":"text","text":"allowed"}]}],"system":"excluded-private-marker"}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			dir := newAuditDirectory(t)
			c := openAudit(t, dir, 0)
			scope := "authenticated-scope-private-marker"
			objectID := monitorScope(t, c, scope)
			body := []byte(tc.body)
			before := time.Now()
			got := c.CaptureExecution("official-request-id", scope, tc.protocol, body)
			after := time.Now()
			for i := range body {
				body[i] = 'x'
			}
			waitSaved(t, c, 1)
			record, err := c.Detail("official-request-id")
			if err != nil || got != objectID || record.ObjectID != objectID || record.Protocol != tc.protocol || record.Text != "allowed" || record.Model != "client-model" {
				t.Fatalf("白名单记录不符：%+v，err=%v", record, err)
			}
			received, err := time.Parse(time.RFC3339Nano, record.ReceivedAt)
			if err != nil || received.Before(before) || received.After(after) {
				t.Fatalf("不是采集调用时刻：%s，err=%v", record.ReceivedAt, err)
			}
			c.Close()
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				data := readAuditFile(t, dir, entry.Name())
				if bytes.Contains(data, []byte(scope)) || bytes.Contains(data, []byte("excluded-private-marker")) || bytes.Contains(data, []byte(tc.body)) {
					t.Fatalf("原scope或非白名单正文被保存到%s", entry.Name())
				}
			}
		})
	}
}

// TestCallerScopeRejectsLegacyDirectory 验证旧种子、已有数据或未知文件不能触发新身份创建，不自动迁移或改写文件。
func TestCallerScopeRejectsLegacyDirectory(t *testing.T) {
	for _, name := range []string{"identity.seed", "records-000001.jsonl", "unknown-state"} {
		t.Run(name, func(t *testing.T) {
			dir := newAuditDirectory(t)
			original := bytes.Repeat([]byte{'s'}, 32)
			writeAuditFile(t, dir, name, original)
			c := openAudit(t, dir, 0)
			if status := c.Status(); status.State != "paused" || status.Reason != "identity_unavailable" {
				t.Fatalf("旧目录未停采：%+v", status)
			}
			if c.CaptureExecution("legacy", "scope", "responses", []byte(`{"input":"must not save"}`)) != "" {
				t.Fatal("无新种子仍返回身份")
			}
			c.Close()
			if !bytes.Equal(original, readAuditFile(t, dir, name)) {
				t.Fatal("旧文件被改写")
			}
			if _, err := os.Lstat(filepath.Join(dir, scopeSeedFile)); !os.IsNotExist(err) {
				t.Fatalf("旧目录创建了新种子：%v", err)
			}
		})
	}
}

// TestCallerScopeSeedFailureDoesNotRotate 验证种子丢失、损坏、不安全权限、链接及新旧种子共存均停采并保留现场。
func TestCallerScopeSeedFailureDoesNotRotate(t *testing.T) {
	for _, kind := range []string{"missing", "truncated", "magic", "oversized", "permissions", "symlink", "legacy-coexists"} {
		t.Run(kind, func(t *testing.T) {
			dir := newAuditDirectory(t)
			c := openAudit(t, dir, 0)
			monitorScope(t, c, "scope")
			c.Close()
			original := readAuditFile(t, dir, scopeSeedFile)
			path := filepath.Join(dir, scopeSeedFile)
			switch kind {
			case "missing", "symlink":
				if err := os.Rename(path, filepath.Join(dir, "preserved-seed")); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink("preserved-seed", path); err != nil {
						t.Fatal(err)
					}
				}
			case "truncated":
				writeAuditFile(t, dir, scopeSeedFile, original[:len(original)-1])
			case "magic":
				original[0] ^= 1
				writeAuditFile(t, dir, scopeSeedFile, original)
			case "oversized":
				writeAuditFile(t, dir, scopeSeedFile, append(original, 'x'))
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "legacy-coexists":
				writeAuditFile(t, dir, "identity.seed", bytes.Repeat([]byte{'l'}, 32))
			}
			before := readAuditFile(t, dir, "capacity.json")
			var seedBefore []byte
			if kind != "missing" {
				seedBefore = readAuditFile(t, dir, scopeSeedFile)
			}
			c = openAudit(t, dir, 0)
			if status := c.Status(); status.State != "paused" || status.Reason != "identity_unavailable" {
				t.Fatalf("种子失败未停采：%+v", status)
			}
			if c.CaptureExecution("failed", "scope", "responses", nil) != "" {
				t.Fatal("种子失败后仍派生身份")
			}
			c.Close()
			if !bytes.Equal(before, readAuditFile(t, dir, "capacity.json")) {
				t.Fatal("身份失败改写了既有诊断")
			}
			if kind == "missing" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("丢失种子被重新创建：%v", err)
				}
			} else if !bytes.Equal(seedBefore, readAuditFile(t, dir, scopeSeedFile)) {
				t.Fatal("失败种子被替换或改写")
			}
		})
	}
}

// TestCallerScopeUnsafeDirectory 验证目录权限不是0700时明确拒绝且不创建种子。
func TestCallerScopeUnsafeDirectory(t *testing.T) {
	dir := newAuditDirectory(t)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if c, err := audit.Open(dir, 0); err == nil {
		c.Close()
		t.Fatal("接受了不安全目录权限")
	}
	if _, err := os.Lstat(filepath.Join(dir, scopeSeedFile)); !os.IsNotExist(err) {
		t.Fatalf("不安全目录被初始化：%v", err)
	}
}

// TestReportInputFailure 验证只报告实际无效输入，未收到的回调不产生桥接缺口或未知损失。
func TestReportInputFailure(t *testing.T) {
	c := openAudit(t, newAuditDirectory(t), 0)
	monitorScope(t, c, "scope")
	c.ReportInputFailure()
	c.ReportInputFailure()
	c.CaptureExecution("invalid request id", "scope", "responses", nil)
	c.CaptureExecution("empty-scope", "", "responses", nil)
	c.CaptureExecution("websocket-not-http", "scope", "responses-websocket", []byte(`{"type":"response.create","input":"excluded"}`))
	status := c.Status()
	if status.Gaps["invalid_event"] != 4 || status.Gaps["unsupported_protocol"] != 1 || status.Recovery.Uncertain {
		t.Fatalf("无效回调诊断不符：%+v", status)
	}
	for reason, count := range status.Gaps {
		if reason != "invalid_event" && reason != "unsupported_protocol" && count != 0 {
			t.Fatalf("伪造未收到的回调损失：%s=%d", reason, count)
		}
	}
}

// directoryCharge 按公开预算规则计算dir及其普通文件的保守占用并返回；读取失败终止测试。
func directoryCharge(t *testing.T, dir string) int64 {
	t.Helper()
	paths := []string{dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	var total int64
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		charge := max(int64(4096), (info.Size()+4095)/4096*4096)
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			charge = max(charge, stat.Blocks*512)
		}
		total += charge
	}
	return total
}

// TestCallerScopeSeedBudget 验证新种子计入全部目录占用，并使仅差一块的恢复预算正确保持暂停。
func TestCallerScopeSeedBudget(t *testing.T) {
	const budget int64 = 4 << 20
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, budget)
	status := c.Status()
	if got := directoryCharge(t, dir); status.UsedBytes != got {
		t.Fatalf("预算未统计完整目录（含种子）：status=%d actual=%d", status.UsedBytes, got)
	}
	seed := readAuditFile(t, dir, scopeSeedFile)
	c.Close()
	// 正好比恢复所需少一块；漏计新种子的一块空间将错误恢复采集。
	padding := budget - directoryCharge(t, dir) - status.ResumeRequiredBytes + 4096
	writeAuditFile(t, dir, "budget-padding", bytes.Repeat([]byte{'p'}, int(padding)))
	c = openAudit(t, dir, budget)
	status = c.Status()
	if status.State != "paused" || status.Reason != "budget_exhausted" || status.UsedBytes != directoryCharge(t, dir) {
		t.Fatalf("含种子的恢复预算不符：%+v", status)
	}
	if free := budget - status.UsedBytes; free >= status.ResumeRequiredBytes || free+4096 < status.ResumeRequiredBytes {
		t.Fatalf("预算夹具没有落在新种子的一块边界：free=%d required=%d", free, status.ResumeRequiredBytes)
	}
	if !bytes.Equal(seed, readAuditFile(t, dir, scopeSeedFile)) {
		t.Fatal("预算暂停轮换了种子")
	}
}

// TestCaptureExecutionBudgetContinuation 验证容量暂停仍解析工具续跑，只将本应保存的新输入记为容量缺口。
func TestCaptureExecutionBudgetContinuation(t *testing.T) {
	dir := newAuditDirectory(t)
	c := openAudit(t, dir, 0)
	scope := "budget-caller-scope"
	id := monitorScope(t, c, scope)
	c.Close()
	writeAuditFile(t, dir, "budget-padding", bytes.Repeat([]byte{'p'}, 2<<20))
	c = openAudit(t, dir, 0)
	budget := int64(4 << 20)
	if err := c.SetSettings(audit.SettingsChange{BudgetBytes: &budget}); err != nil {
		t.Fatal(err)
	}
	if c.Status().Reason != "budget_exhausted" {
		t.Fatalf("夹具未触发预算暂停：%+v", c.Status())
	}
	for i, tc := range []struct {
		protocol string
		body     string
	}{
		{"chat-completions", `{"messages":[{"role":"tool","content":"result"}]}`},
		{"responses", `{"input":[{"type":"function_call_output","call_id":"call","output":"result"}]}`},
		{"anthropic-messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":"result"}]}]}`},
	} {
		if got := c.CaptureExecution(fmt.Sprintf("tool-%d", i), scope, tc.protocol, []byte(tc.body)); got != id {
			t.Fatal("暂停改变了身份")
		}
	}
	if count := c.Status().Gaps["budget_exhausted"]; count != 0 {
		t.Fatalf("工具续跑被计为容量丢失：%d", count)
	}
	c.CaptureExecution("new-input", scope, "responses", []byte(`{"input":"must be a budget gap"}`))
	status := c.Status()
	if status.Gaps["budget_exhausted"] != 1 || status.Gaps["observer_unavailable"] != 0 || status.Saved != 0 || status.Queued != 0 {
		t.Fatalf("暂停时没有正确判断新输入：%+v", status)
	}
}
