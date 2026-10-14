// 本文件通过真实宿主 HTTP 和管理接口验收审计行为，不调用私有队列或存储方法。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const adminKey = "synthetic-admin-audit-test-only"
const keyA = "synthetic-client-audit-a-only"
const keyB = "synthetic-client-audit-b-only"
const api = "/v0/management/plugins/request-input-audit"

type instance struct {
	base   string
	dir    string
	log    *os.File
	cmd    *exec.Cmd
	client *http.Client
	stop   func()
}

// startHost 在独立目录启动真实宿主；budget 设置有限预算，plugin 控制加载，upstream 指向本地可控模型。
// stateDir 可选，重启验收时复用同一专用数据目录；返回 HTTP 入口，启动失败终止测试。
// 保留数据供隐私检查，不自动删除文件；测试结束只停止本测试创建的进程。
func startHost(t *testing.T, budget int64, plugin bool, upstream string, stateDir ...string) *instance {
	t.Helper()
	return startHostWithManagementAuth(t, budget, plugin, upstream, false, stateDir...)
}

// startHostWithManagementAuth 启动真实合成宿主；参数同 startHost，environmentAuth 选择宿主已有环境管理密码入口。
// 环境鉴权只用于大量管理查询的压力夹具，仍必须提供正确合成密码；普通权限验收使用配置 bcrypt。
// 返回HTTP入口，启动失败终止测试，不继承真实凭据或添加生产故障接口。
func startHostWithManagementAuth(t *testing.T, budget int64, plugin bool, upstream string, environmentAuth bool, stateDir ...string) *instance {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	host := os.Getenv("CPA_HOST")
	if host == "" {
		host = filepath.Join(root, ".build/bin/cliproxyapi")
	}
	if _, err := os.Stat(host); err != nil {
		t.Fatalf("真实宿主未构建: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".test-data"), 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".test-data"), "http-")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(root, ".build/plugins")
	if !plugin {
		pluginDir = filepath.Join(dir, "empty-plugins")
		if err := os.Mkdir(pluginDir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	auditDir := filepath.Join(dir, "audit")
	if len(stateDir) > 0 {
		auditDir = stateDir[0]
	}
	config := fmt.Sprintf(`config-version: 8
server:
  host: 127.0.0.1
  port: %d
management:
  secret-key: %q
  disable-control-panel: true
  disable-auto-update-panel: true
access:
  api-keys: [%q, %q]
auth-dir: %q
logging-to-file: false
request-log: false
plugins:
  enabled: true
  dir: %q
  configs:
    request-input-audit:
      enabled: true
      state-dir: %q
      budget-bytes: %d
`, port, adminKey, keyA, keyB, filepath.Join(dir, "auths"), pluginDir, auditDir, budget)
	if upstream != "" {
		config += fmt.Sprintf(`openai-compatibility:
  - name: controlled
    base-url: %q
    api-key-entries:
      - api-key: synthetic-upstream-only
    models:
      - name: audit-test-model
        alias: audit-test-model
`, upstream)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(dir, "host.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(host, "--config", configPath, "--local-model")
	cmd.Dir = dir
	// 不继承真实云凭据、代理、远程存储或插件 token，只提供运行所需的本地环境。
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir}
	if environmentAuth {
		// 宿主原生管理入口使用恒定时间比较；避免每条详情都做 bcrypt 掩盖队列/落盘验收的成本。
		cmd.Env = append(cmd.Env, "MANAGEMENT_PASSWORD="+adminKey)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &instance{base: fmt.Sprintf("http://127.0.0.1:%d", port), dir: dir, log: log, cmd: cmd, client: &http.Client{Timeout: 5 * time.Second}}
	var stopOnce sync.Once
	h.stop = func() {
		stopOnce.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(os.Interrupt)
				done := make(chan struct{})
				go func() { _ = cmd.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(8 * time.Second):
					_ = cmd.Process.Kill()
					<-done
				}
			}
			if err := log.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(h.stop)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := h.client.Get(h.base + "/")
		if err == nil {
			_ = resp.Body.Close()
			return h
		}
		time.Sleep(25 * time.Millisecond)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "host.log"))
	t.Fatalf("宿主未启动（%s/%s）: %s", runtime.GOOS, runtime.GOARCH, raw)
	return nil
}

// request 执行公开 HTTP 操作，返回状态与响应；网络/读取错误终止当前测试，不保存凭据。
func (h *instance) request(t *testing.T, method, path, key string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, h.base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

type savedRecord struct {
	ID                   string    `json:"id"`
	ObjectID             string    `json:"object_id"`
	Text                 string    `json:"text"`
	Model                string    `json:"model"`
	Name                 string    `json:"name"`
	Protocol             string    `json:"protocol"`
	ReceivedAt           time.Time `json:"received_at"`
	Truncated            bool      `json:"truncated"`
	ExtractionIncomplete bool      `json:"extraction_incomplete"`
}

// monitorAll 通过受保护目录选择全部合成对象并命名，返回 ID；管理操作失败终止测试。
func (h *instance) monitorAll(t *testing.T) []string {
	t.Helper()
	code, raw := h.request(t, "GET", api+"/objects", adminKey, nil)
	if code != 200 {
		t.Fatalf("对象目录: %d", code)
	}
	var catalog struct {
		Objects []struct {
			ID string `json:"id"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(catalog.Objects))
	for i, o := range catalog.Objects {
		body, _ := json.Marshal(map[string]string{"object_id": o.ID, "name": fmt.Sprintf("合成对象%d", i)})
		status, resp := h.request(t, "POST", api+"/monitor", adminKey, body)
		if status != 200 {
			t.Fatalf("监控选择预期200，实际%d: %s", status, resp)
		}
		ids = append(ids, o.ID)
	}
	return ids
}

// waitRecords 等待公开接口出现明确已保存结果，返回记录；期限内未满足条件终止测试。
func (h *instance) waitRecords(t *testing.T, count int) []savedRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, raw := h.request(t, "GET", api+"/records", adminKey, nil)
		if status != 200 {
			t.Fatalf("记录列表: %d", status)
		}
		var list struct {
			Records []savedRecord `json:"records"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Records) >= count {
			return list.Records
		}
		if time.Now().After(deadline) {
			t.Fatal("在期限内没有看到已保存记录")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSavedLatestInput 验证选择命名后从真实路由前失败提交保存末尾纯文本，列表与单条详情一致。
func TestSavedLatestInput(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	ids := h.monitorAll(t)
	body := []byte(`{"model":"not-a-real-model","messages":[{"role":"system","content":"FORBIDDEN_SYSTEM"},{"role":"user","content":"FORBIDDEN_HISTORY"},{"role":"assistant","content":"FORBIDDEN_REPLY"},{"role":"user","content":"末尾新文本"}]}`)
	status, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
	if status != 400 {
		t.Fatalf("应保留宿主原模型路由错误400，实际%d", status)
	}
	list := h.waitRecords(t, 1)
	if len(list) != 1 {
		t.Fatalf("一次提交应只有一条，实际%d", len(list))
	}
	code, raw := h.request(t, "GET", api+"/detail?id="+list[0].ID, adminKey, nil)
	if code != 200 {
		t.Fatalf("已保存详情: %d", code)
	}
	var record savedRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.Text != "末尾新文本" || record.Model != "not-a-real-model" || record.Protocol != "chat-completions" || record.ReceivedAt.IsZero() || record.Truncated {
		t.Fatalf("正文或白名单元数据不符合规范: %+v", record)
	}
	found := false
	for _, id := range ids {
		if id == record.ObjectID {
			found = true
		}
	}
	if !found || record.Name == "" {
		t.Fatal("可信对象关联或名称缺失")
	}
}

// TestMissingModelStillRecorded 验证缺失模型的可提取已认证提交在路由前失败时仍记录，模型显示未知而不是猜测。
func TestMissingModelStillRecorded(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	status, _ := h.request(t, "POST", "/v1/chat/completions", keyA, []byte(`{"messages":[{"role":"user","content":"无模型也属于新提交"}]}`))
	if status != 400 {
		t.Fatalf("缺失模型应保持宿主错误，实际%d", status)
	}
	list := h.waitRecords(t, 1)
	code, raw := h.request(t, "GET", api+"/detail?id="+list[0].ID, adminKey, nil)
	var record savedRecord
	if code != 200 || json.Unmarshal(raw, &record) != nil || record.Text != "无模型也属于新提交" || record.Model != "" {
		t.Fatal("缺失模型没有保存为未知")
	}
}

// TestTailsAndUnsupported 验证工具续跑不回找、未知块不兜底、身份分离及禁存内容不进入新增数据或诊断。
func TestTailsAndUnsupported(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	for _, key := range []string{keyA, keyB} {
		h.request(t, "POST", "/v1/chat/completions", key, []byte(`{"model":"missing","messages":[{"role":"user","content":"独立合成提交"}]}`))
	}
	list := h.waitRecords(t, 2)
	if len(list) != 2 || list[0].ObjectID == list[1].ObjectID || list[0].ID == list[1].ID {
		t.Fatal("两个Key应独立关联，两次提交不得去重")
	}
	for _, body := range []string{
		`{"model":"missing","messages":[{"role":"user","content":"FORBIDDEN_OLD_USER"},{"role":"tool","content":"FORBIDDEN_TOOL_RESULT"}]}`,
		`{"model":"missing","messages":[{"role":"user","content":"FORBIDDEN_OLD_USER"},{"role":"assistant","content":"FORBIDDEN_ASSISTANT"}]}`,
		`{"model":"missing","messages":[{"role":"user","content":[{"type":"unknown","payload":"FORBIDDEN_NON_TEXT"}]}]}`,
		`{"model":"missing","messages":[{"role":"user","content":""}]}`,
		`{"model":"missing","messages":[`,
	} {
		h.request(t, "POST", "/v1/chat/completions", keyA, []byte(body))
	}
	h.request(t, "POST", "/v1/chat/completions", "synthetic-invalid-client", []byte(`{"model":"missing","messages":[{"role":"user","content":"FORBIDDEN_UNAUTHENTICATED"}]}`))
	code, raw := h.request(t, "GET", api+"/records", adminKey, nil)
	var records struct {
		Records []savedRecord `json:"records"`
	}
	if code != 200 || json.Unmarshal(raw, &records) != nil || len(records.Records) != 2 {
		t.Fatal("续跑/未知/空输入/失败鉴权错误生成记录")
	}
	_, raw = h.request(t, "GET", api+"/status", adminKey, nil)
	var state struct {
		State struct {
			Gaps map[string]uint64 `json:"gaps"`
		} `json:"state"`
	}
	if json.Unmarshal(raw, &state) != nil || state.State.Gaps["unsupported_content"] == 0 || state.State.Gaps["invalid_json"] == 0 {
		t.Fatal("未支持内容或解析失败被伪装为完整/正常跳过")
	}
	forbidden := []string{keyA, keyB, adminKey, "FORBIDDEN_OLD_USER", "FORBIDDEN_TOOL_RESULT", "FORBIDDEN_ASSISTANT", "FORBIDDEN_NON_TEXT", "FORBIDDEN_UNAUTHENTICATED"}
	if err := filepath.WalkDir(filepath.Join(h.dir, "audit"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0600)
		if entry.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			return fmt.Errorf("持久化权限不符合限制: %s", entry.Name())
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, marker := range forbidden {
			if bytes.Contains(content, []byte(marker)) {
				return fmt.Errorf("新增存储出现禁存标记（不输出内容）")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(h.dir, "host.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range forbidden {
		if bytes.Contains(log, []byte(marker)) {
			t.Fatal("诊断出现禁存内容（不输出内容）")
		}
	}
}

// TestUpstreamUnchangedAndTextLimit 验证真实可控上游请求不因审计改变，恰好64KiB、字符越界和空白遵守正文上限。
func TestUpstreamUnchangedAndTextLimit(t *testing.T) {
	received := make(chan []byte, 16)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "synthetic read failure", 400)
			return
		}
		received <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic","object":"chat.completion","model":"audit-test-model","choices":[{"index":0,"message":{"role":"assistant","content":"FORBIDDEN_UPSTREAM_REPLY"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()
	audited := startHost(t, 1<<30, true, upstream.URL+"/v1")
	off := startHost(t, 1<<30, false, upstream.URL+"/v1")
	audited.monitorAll(t)
	cases := []struct {
		input, want string
		truncated   bool
	}{{strings.Repeat("a", 65536), strings.Repeat("a", 65536), false}, {strings.Repeat("b", 65535) + "🙂FORBIDDEN_TRUNCATED_TAIL", strings.Repeat("b", 65535), true}, {"  \n\t", "  \n\t", false}}
	for i, tc := range cases {
		body, err := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "system", "content": "FORBIDDEN_SYSTEM_UPSTREAM"}, {"role": "user", "content": "FORBIDDEN_HISTORY_UPSTREAM"}, {"role": "user", "content": tc.input}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range []*instance{off, audited} {
			code, raw := h.request(t, "POST", "/v1/chat/completions", keyA, body)
			if code != 200 {
				t.Fatalf("可控模型请求预期200，实际%d: %s", code, raw)
			}
		}
		var plain, observed []byte
		select {
		case plain = <-received:
		case <-time.After(3 * time.Second):
			t.Fatal("关闭审计的上游未收到请求")
		}
		select {
		case observed = <-received:
		case <-time.After(3 * time.Second):
			t.Fatal("启用审计的上游未收到请求")
		}
		if !bytes.Equal(plain, observed) {
			t.Fatal("启用审计改变了实际上游请求")
		}
		if !bytes.Contains(observed, []byte("FORBIDDEN_HISTORY_UPSTREAM")) {
			t.Fatal("上游历史被审计移除")
		}
		if tc.truncated && !bytes.Contains(observed, []byte("FORBIDDEN_TRUNCATED_TAIL")) {
			t.Fatal("只应截审计副本，不应截模型请求")
		}
		list := audited.waitRecords(t, i+1)
		_, raw := audited.request(t, "GET", api+"/detail?id="+list[0].ID, adminKey, nil)
		var record savedRecord
		if json.Unmarshal(raw, &record) != nil || record.Text != tc.want || record.Truncated != tc.truncated {
			t.Fatalf("案例%d的UTF-8正文边界不正确（不输出正文）", i)
		}
	}
	if err := filepath.WalkDir(filepath.Join(audited.dir, "audit"), func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range []string{"FORBIDDEN_SYSTEM_UPSTREAM", "FORBIDDEN_HISTORY_UPSTREAM", "FORBIDDEN_UPSTREAM_REPLY", "FORBIDDEN_TRUNCATED_TAIL"} {
			if bytes.Contains(raw, []byte(forbidden)) {
				return fmt.Errorf("新增存储包含禁止内容")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestBudgetStopsWithoutEvicting 验证合法小预算从第一条起限制整个目录，满额停采但不删除旧输入或改模型错误。
func TestBudgetStopsWithoutEvicting(t *testing.T) {
	h := startHost(t, 4<<20, true, "")
	h.monitorAll(t)
	body, _ := json.Marshal(map[string]any{"model": "missing", "messages": []map[string]string{{"role": "user", "content": strings.Repeat("x", 65536)}}})
	paused := false
	for i := 0; i < 128; i++ {
		code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
		if code != 400 {
			t.Fatalf("满额不应改变模型路由返回: %d", code)
		}
		_, raw := h.request(t, "GET", api+"/status", adminKey, nil)
		var state struct {
			State struct {
				State, Reason string
				UsedBytes     int64 `json:"used_bytes"`
			} `json:"state"`
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if state.State.State == "paused" {
			if state.State.Reason != "budget_exhausted" || state.State.UsedBytes > 4<<20 {
				t.Fatal("容量原因或占用不准确")
			}
			paused = true
			break
		}
	}
	if !paused {
		t.Fatal("超过合法预算仍未停止采集")
	}
	_, raw := h.request(t, "GET", api+"/records?limit=100", adminKey, nil)
	var before struct {
		Records []savedRecord `json:"records"`
		Total   int           `json:"total"`
	}
	if json.Unmarshal(raw, &before) != nil || before.Total == 0 {
		t.Fatal("满额前的历史丢失")
	}
	for i := 0; i < 3; i++ {
		code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
		if code != 400 {
			t.Fatal("容量停采改变宿主模型调用")
		}
	}
	_, raw = h.request(t, "GET", api+"/records?limit=100", adminKey, nil)
	var after struct {
		Records []savedRecord `json:"records"`
		Total   int           `json:"total"`
	}
	if json.Unmarshal(raw, &after) != nil || after.Total != before.Total || after.Records[0].ID != before.Records[0].ID {
		t.Fatal("满额挤掉未过期历史或保存了更多输入")
	}
	var used int64
	if err := filepath.WalkDir(filepath.Join(h.dir, "audit"), func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			info, err := e.Info()
			if err != nil {
				return err
			}
			used += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if used > 4<<20 {
		t.Fatal("新增辅助文件绕过总预算")
	}
}

// TestStorageFaultFailOpen 验证外部文件系统故障使采集停下并可见，但不拒绝实际可控模型请求。
func TestStorageFaultFailOpen(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	h := startHost(t, 1<<30, true, upstream.URL+"/v1")
	h.monitorAll(t)
	body := []byte(`{"model":"audit-test-model","messages":[{"role":"user","content":"合成持久化故障验收"}]}`)
	code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
	if code != 200 {
		t.Fatal("合成模型未正常运行")
	}
	h.waitRecords(t, 1)
	// 不操作私有记录结构或增加故障 API；外部加入权限不合法的辅助文件，触发专用卷的安全保护。
	if err := os.WriteFile(filepath.Join(h.dir, "audit", "external-volume-fault"), []byte("synthetic fault only"), 0400); err != nil {
		t.Fatal(err)
	}
	code, _ = h.request(t, "POST", "/v1/chat/completions", keyA, body)
	if code != 200 {
		t.Fatal("审计存储故障改变或拒绝了模型调用")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, raw := h.request(t, "GET", api+"/status", adminKey, nil)
		var status struct {
			State struct {
				State, Reason string
				Gaps          map[string]uint64 `json:"gaps"`
			} `json:"state"`
		}
		if json.Unmarshal(raw, &status) != nil {
			t.Fatal("状态解析失败")
		}
		if status.State.State == "paused" {
			if status.State.Reason != "storage_error" || status.State.Gaps["storage_error"] == 0 {
				t.Fatal("故障被伪装为无记录")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("存储故障没有可见诊断")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, raw := h.request(t, "GET", api+"/records", adminKey, nil)
	var list struct {
		Total int `json:"total"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Total != 1 {
		t.Fatal("失败的排队副本被当作已保存")
	}
}

// TestMonitorRequiresName 验证监控对象必须填写有限的可读名称，不能把空名称保存为有效配置。
func TestMonitorRequiresName(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	_, raw := h.request(t, "GET", api+"/objects", adminKey, nil)
	var catalog struct {
		Objects []struct {
			ID string `json:"id"`
		} `json:"objects"`
	}
	if json.Unmarshal(raw, &catalog) != nil || len(catalog.Objects) == 0 {
		t.Fatal("对象目录缺失")
	}
	body, _ := json.Marshal(map[string]string{"object_id": catalog.Objects[0].ID, "name": "   "})
	code, _ := h.request(t, "POST", api+"/monitor", adminKey, body)
	if code != 400 {
		t.Fatalf("空白名称应被拒绝，实际%d", code)
	}
}

// TestIdentityUnavailableIsVisible 验证外部破坏身份目录权限后停止采集并显示不可用，但原模型路由错误保持不变。
func TestIdentityUnavailableIsVisible(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	if err := os.Chmod(filepath.Join(h.dir, "audit"), 0500); err != nil {
		t.Fatal(err)
	}
	code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, []byte(`{"model":"missing","messages":[{"role":"user","content":"权限故障合成提交"}]}`))
	if code != 400 {
		t.Fatal("身份故障改变了模型响应")
	}
	_, raw := h.request(t, "GET", api+"/status", adminKey, nil)
	var status struct {
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	}
	if json.Unmarshal(raw, &status) != nil || status.Available || status.Reason == "" {
		t.Fatal("观察身份不可用仍显示采集正常")
	}
}

// TestStableIdentityAcrossRestart 验证宿主正常重启不轮换身份，已保存记录仍关联原对象。
func TestStableIdentityAcrossRestart(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	ids := h.monitorAll(t)
	h.request(t, "POST", "/v1/chat/completions", keyA, []byte(`{"model":"missing","messages":[{"role":"user","content":"重启前合成提交"}]}`))
	before := h.waitRecords(t, 1)[0]
	stateDir := filepath.Join(h.dir, "audit")
	h.stop()
	restarted := startHost(t, 1<<30, true, "", stateDir)
	_, raw := restarted.request(t, "GET", api+"/objects", adminKey, nil)
	var catalog struct {
		Objects []struct {
			ID        string `json:"id"`
			Monitored bool   `json:"monitored"`
		} `json:"objects"`
	}
	if json.Unmarshal(raw, &catalog) != nil || len(catalog.Objects) != len(ids) {
		t.Fatal("重启对象目录缺失")
	}
	for i, obj := range catalog.Objects {
		if obj.ID != ids[i] || !obj.Monitored {
			t.Fatal("重启轮换身份或丢失选择")
		}
	}
	after := restarted.waitRecords(t, 1)[0]
	if before.ID != after.ID || before.ObjectID != after.ObjectID {
		t.Fatal("重启历史关联错误")
	}
}

// TestProtocolFieldAmbiguityDoesNotLeak 验证大小写别名或重复字段不能把真实工具结果改判为新用户输入。
func TestProtocolFieldAmbiguityDoesNotLeak(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	for _, body := range []string{
		`{"model":"missing","messages":[{"role":"tool","Role":"user","content":"FORBIDDEN_CASE_ALIAS_TOOL"}]}`,
		`{"model":"missing","messages":[{"role":"tool","role":"user","content":"FORBIDDEN_DUPLICATE_ROLE_TOOL"}]}`,
		`{"model":"missing","Messages":[{"role":"user","content":"FORBIDDEN_UPPERCASE_HISTORY"}],"messages":[{"role":"tool","content":"FORBIDDEN_TOOL"}]}`,
	} {
		h.request(t, "POST", "/v1/chat/completions", keyA, []byte(body))
	}
	_, raw := h.request(t, "GET", api+"/records", adminKey, nil)
	var list struct {
		Total int `json:"total"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Total != 0 {
		t.Fatal("歧义协议字段泄漏工具结果或历史")
	}
}

// TestDefaultAndAuthorization 验证真实 native 对象目录、默认空名单与宿主管理授权，凭据不在输出中。
func TestDefaultAndAuthorization(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	status, raw := h.request(t, "GET", api+"/objects", adminKey, nil)
	if status != 200 {
		t.Fatalf("管理员对象目录预期200，实际%d: %s", status, raw)
	}
	var catalog struct {
		Objects []struct {
			ID        string `json:"id"`
			Monitored bool   `json:"monitored"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Objects) != 2 {
		t.Fatalf("预期两个独立对象，实际%d", len(catalog.Objects))
	}
	if catalog.Objects[0].ID == catalog.Objects[1].ID {
		t.Fatal("不同Key被合并")
	}
	for _, o := range catalog.Objects {
		if o.Monitored || o.ID == "" {
			t.Fatal("默认名单不为空或对象标识为空")
		}
	}
	h.request(t, "POST", "/v1/chat/completions", keyA, []byte(`{"model":"missing","messages":[{"role":"user","content":"默认未监控，不得保存"}]}`))
	_, emptyRaw := h.request(t, "GET", api+"/records", adminKey, nil)
	var empty struct {
		Total int `json:"total"`
	}
	if json.Unmarshal(emptyRaw, &empty) != nil || empty.Total != 0 {
		t.Fatal("默认空名单仍保存了输入")
	}
	for _, key := range []string{"", keyA, catalog.Objects[0].ID} {
		for _, path := range []string{"/objects", "/records", "/status", "/detail?id=not-real"} {
			code, _ := h.request(t, "GET", api+path, key, nil)
			if code != 401 && code != 403 {
				t.Fatalf("非管理凭据可读取%s: %d", path, code)
			}
		}
	}
	for _, secret := range []string{keyA, keyB, adminKey} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("对象目录暴露认证Key")
		}
	}
	code, raw := h.request(t, "GET", "/v0/resource/plugins/request-input-audit/index.html", "", nil)
	if code != 200 || !bytes.Contains(raw, []byte("输入审计")) {
		t.Fatalf("无数据静态页面不可用: %d", code)
	}
	for _, o := range catalog.Objects {
		if bytes.Contains(raw, []byte(o.ID)) {
			t.Fatal("公开资源内嵌对象数据")
		}
	}
}
