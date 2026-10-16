// 本文件以真实宿主 HTTP、可控上游及受保护管理 API 验证队列满缺口、调用不变与排空后的保存一致性。
package system

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type queuePressureSnapshot struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	State     struct {
		Available   bool              `json:"available"`
		State       string            `json:"state"`
		Reason      string            `json:"reason"`
		Queued      int64             `json:"queued"`
		Saved       int64             `json:"saved"`
		Gaps        map[string]uint64 `json:"gaps"`
		UsedBytes   int64             `json:"used_bytes"`
		BudgetBytes int64             `json:"budget_bytes"`
	} `json:"state"`
}

// queuePressureText 为 index 生成恰好 64 KiB 的独立合成提交，返回带序号的纯文本；无 I/O 或异常。
func queuePressureText(index int) string {
	prefix := fmt.Sprintf("queue-pressure-%08d:", index)
	return prefix + strings.Repeat("q", (64<<10)-len(prefix))
}

// queuePressureIndex 从 text 的合成标记读取提交序号；limit 限定合法范围，返回序号或固定校验错误，不输出正文。
func queuePressureIndex(text string, limit int) (int, error) {
	prefix, _, ok := strings.Cut(text, ":")
	if !ok || !strings.HasPrefix(prefix, "queue-pressure-") {
		return 0, errors.New("缺少合成提交标记")
	}
	index, err := strconv.Atoi(strings.TrimPrefix(prefix, "queue-pressure-"))
	if err != nil || index < 0 || index >= limit {
		return 0, errors.New("合成提交序号越界")
	}
	return index, nil
}

// queuePressureHTTP 通过 client 向 h 的公开 path 执行 method；key 为合成认证，body 为合成正文。
// 返回 HTTP 状态及有限响应；构造、网络、读取、关闭失败或超过响应上限返回错误，不记录请求正文。
func queuePressureHTTP(client *http.Client, h *instance, method, path, key string, body []byte) (int, []byte, error) {
	req, err := http.NewRequest(method, h.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	// 一页最多 100 个短预览；详情仅 64 KiB。限制错误响应，避免压力测试无界保留诊断。
	const responseLimit = 1 << 20
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	closeErr := resp.Body.Close()
	if len(raw) > responseLimit {
		return resp.StatusCode, nil, errors.New("公开接口响应超过测试上限")
	}
	return resp.StatusCode, raw, errors.Join(readErr, closeErr)
}

// queuePressureStatus 以 client 查询 h 的受保护状态，返回公开计数；t 在 HTTP、解析或观察不可用时终止测试。
func queuePressureStatus(t *testing.T, client *http.Client, h *instance) queuePressureSnapshot {
	t.Helper()
	code, raw, err := queuePressureHTTP(client, h, "GET", api+"/status", adminKey, nil)
	if err != nil || code != http.StatusOK {
		t.Fatalf("压力状态查询失败: HTTP=%d err=%v", code, err)
	}
	var snapshot queuePressureSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Available || !snapshot.State.Available || snapshot.State.State != "active" || snapshot.Reason != "" || snapshot.State.Reason != "" {
		t.Fatalf("压力实例不是正常活动状态: available=%v state=%s reason=%s/%s", snapshot.Available, snapshot.State.State, snapshot.Reason, snapshot.State.Reason)
	}
	if snapshot.State.Queued < 0 || snapshot.State.Saved < 0 || snapshot.State.UsedBytes > snapshot.State.BudgetBytes {
		t.Fatal("公开计数或预算不合法")
	}
	for reason, count := range snapshot.State.Gaps {
		if reason != "queue_full" && count != 0 {
			t.Fatalf("压力场景出现非队列满缺口: %s=%d", reason, count)
		}
	}
	return snapshot
}

// TestQueueFullPreservesUpstreamAndDrains 验证真实并发压满队列仍全部正常调用，排队不冒充保存，排空后缺口及完整正文一致。
func TestQueueFullPreservesUpstreamAndDrains(t *testing.T) {
	// 分批施压避免无界负载；即使宿主身份目录同步串行，也必须实际观察 queue_full，不能以高并发代替证据。
	const concurrency, batchSize, maxRequests = 128, 1024, 4096
	expected := make([][sha256.Size]byte, maxRequests)
	for i := range expected {
		expected[i] = sha256.Sum256([]byte(queuePressureText(i)))
	}
	var upstreamMu sync.Mutex
	upstreamSeen := make([]bool, maxRequests)
	var upstreamCount, upstreamInvalid int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		// 上游只保留合成期望的摘要和接收计数；正文仅在本次校验时借用，不写日志或记录真实输入。
		raw, err := io.ReadAll(io.LimitReader(r.Body, 128<<10))
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		}
		valid := err == nil && r.Method == "POST" && r.URL.Path == "/v1/chat/completions" && json.Unmarshal(raw, &payload) == nil && payload.Model == "audit-test-model" && len(payload.Messages) == 1
		index := -1
		if valid {
			index, err = queuePressureIndex(payload.Messages[0].Content, maxRequests)
			valid = err == nil && payload.Messages[0].Role == "user" && len(payload.Messages[0].Content) == 64<<10 && sha256.Sum256([]byte(payload.Messages[0].Content)) == expected[index]
		}
		upstreamMu.Lock()
		upstreamCount++
		if !valid || upstreamSeen[index] {
			upstreamInvalid++
		} else {
			upstreamSeen[index] = true
		}
		upstreamMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !valid {
			http.Error(w, "synthetic upstream verification failure", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"id":"synthetic-queue-pressure","object":"chat.completion","model":"audit-test-model","choices":[{"index":0,"message":{"role":"assistant","content":"queue-pressure-ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	h := startHostWithManagementAuth(t, 1<<30, true, upstream.URL+"/v1", true)
	// 换用宿主原生环境密码入口仍有真实鉴权，不能以关闭授权换取压力测试速度。
	for _, credential := range []string{"", keyA, "synthetic-wrong-admin"} {
		code, _ := h.request(t, "GET", api+"/objects", credential, nil)
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatal("压力夹具管理授权被绕过")
		}
	}
	monitored := h.monitorAll(t)
	if len(monitored) == 0 {
		t.Fatal("没有监控合成对象")
	}
	// 不使用现有 5 秒客户端：压力下的身份同步排队是宿主开销，不能将其超时误报为审计拒绝。
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext, MaxConnsPerHost: concurrency, MaxIdleConns: concurrency, MaxIdleConnsPerHost: concurrency, ResponseHeaderTimeout: 90 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 100 * time.Second}
	adminTransport := &http.Transport{DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext}
	adminClient := &http.Client{Transport: adminTransport, Timeout: 30 * time.Second}
	defer transport.CloseIdleConnections()
	defer adminTransport.CloseIdleConnections()
	initial := queuePressureStatus(t, adminClient, h)
	if initial.State.Saved != 0 || initial.State.Queued != 0 || initial.State.Gaps["queue_full"] != 0 {
		t.Fatal("合成实例不是空的初始状态")
	}
	t.Logf("合成实例目录（保留、不删除）: %s", h.dir)
	type result struct {
		code int
		err  error
		ok   bool
	}
	var attempted, completed, modelOK int
	var peakQueued int64
	var queueNotSaved bool
	observe := func() queuePressureSnapshot {
		snapshot := queuePressureStatus(t, adminClient, h)
		if snapshot.State.Queued > peakQueued {
			peakQueued = snapshot.State.Queued
		}
		if snapshot.State.Queued > 0 && !queueNotSaved {
			code, raw, err := queuePressureHTTP(adminClient, h, "POST", api+"/records/search", adminKey, []byte(`{"limit":1}`))
			var list struct {
				Total int64 `json:"total"`
			}
			if err != nil || code != 200 || json.Unmarshal(raw, &list) != nil {
				t.Fatalf("排队期间公开列表失败: HTTP=%d err=%v", code, err)
			}
			after := queuePressureStatus(t, adminClient, h)
			if after.State.Queued > peakQueued {
				peakQueued = after.State.Queued
			}
			// 三次查询不是原子快照；用前后 saved 夹住列表 total，避免将正常写入推进误判为错误。
			if list.Total < snapshot.State.Saved || list.Total > after.State.Saved {
				t.Fatal("公开列表把尚未保存的队列副本计入记录，或丢失已保存记录")
			}
			if after.State.Queued > 0 {
				queueNotSaved = true
				t.Logf("排队不冒充保存（管理 API）: saved_before=%d records_total=%d saved_after=%d queued_after=%d", snapshot.State.Saved, list.Total, after.State.Saved, after.State.Queued)
			}
			snapshot = after
		}
		return snapshot
	}
	for attempted < maxRequests {
		jobs := make(chan int, batchSize)
		results := make(chan result, batchSize)
		gate := make(chan struct{})
		var workers sync.WaitGroup
		for worker := 0; worker < concurrency; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-gate
				for index := range jobs {
					body, err := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": queuePressureText(index)}}})
					if err != nil {
						results <- result{err: err}
						continue
					}
					code, raw, err := queuePressureHTTP(client, h, "POST", "/v1/chat/completions", keyA, body)
					var response struct {
						Choices []struct {
							Message struct{ Content string } `json:"message"`
						} `json:"choices"`
					}
					ok := code == 200 && err == nil && json.Unmarshal(raw, &response) == nil && len(response.Choices) == 1 && response.Choices[0].Message.Content == "queue-pressure-ok"
					results <- result{code: code, err: err, ok: ok}
				}
			}()
		}
		for i := attempted; i < attempted+batchSize; i++ {
			jobs <- i
		}
		close(jobs)
		attempted += batchSize
		close(gate)
		ticker := time.NewTicker(50 * time.Millisecond)
		var failed int
		for completed < attempted {
			select {
			case res := <-results:
				completed++
				if res.ok {
					modelOK++
				} else {
					failed++
					if failed <= 3 {
						t.Logf("模型 HTTP 失败（不输出正文）: code=%d err=%v", res.code, res.err)
					}
				}
			case <-ticker.C:
				observe()
			}
		}
		ticker.Stop()
		workers.Wait()
		state := observe()
		t.Logf("本轮真实 HTTP=%d model_200=%d；管理 API queued=%d queued_peak=%d saved=%d queue_full=%d", attempted, modelOK, state.State.Queued, peakQueued, state.State.Saved, state.State.Gaps["queue_full"])
		if failed != 0 {
			t.Fatalf("%d 个正常模型调用失败；不能把超时或其他拒绝算作 queue_full 验收通过", failed)
		}
		if state.State.Gaps["queue_full"] > 0 {
			break
		}
	}
	deadline := time.Now().Add(60 * time.Second)
	final := observe()
	for final.State.Queued != 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		final = observe()
	}
	upstreamMu.Lock()
	received, invalid := upstreamCount, upstreamInvalid
	allReceived := true
	for _, seen := range upstreamSeen[:attempted] {
		allReceived = allReceived && seen
	}
	upstreamMu.Unlock()
	t.Logf("运行证据: HTTP requests=%d model_200=%d upstream_received=%d upstream_invalid=%d；管理 API queued_peak=%d queued_final=%d saved=%d gaps.queue_full=%d used_bytes=%d budget_bytes=%d", attempted, modelOK, received, invalid, peakQueued, final.State.Queued, final.State.Saved, final.State.Gaps["queue_full"], final.State.UsedBytes, final.State.BudgetBytes)
	if received != attempted || invalid != 0 || !allReceived || modelOK != attempted {
		t.Fatal("可控上游没有逐次收到完整、不重复且不损坏的合成原文本，或模型调用不正常")
	}
	if final.State.Gaps["queue_full"] == 0 {
		t.Fatal("普通有界 burst 未实际触发 queue_full；宿主身份 mutex/directorySync 可能限制入队速率，需要隔离卷可控外部 I/O 延迟，不能用生产后门或暂停宿主伪造通过")
	}
	if final.State.Queued != 0 || final.State.Saved <= 0 || peakQueued == 0 || !queueNotSaved {
		t.Fatal("队列未排空、无已保存记录或没有证实排队与保存分离")
	}
	if uint64(final.State.Saved)+final.State.Gaps["queue_full"] != uint64(attempted) {
		t.Fatal("排空后已保存与队列满缺口不能精确解释每次合成提交；排队数不能冒充保存数")
	}
	seenIDs := make(map[string]bool)
	seenInputs := make(map[int]bool)
	for offset := 0; int64(offset) < final.State.Saved; {
		code, raw, err := queuePressureHTTP(adminClient, h, "POST", api+"/records/search", adminKey, []byte(fmt.Sprintf(`{"offset":%d,"limit":100}`, offset)))
		var list struct {
			Total   int64         `json:"total"`
			Records []savedRecord `json:"records"`
		}
		if err != nil || code != 200 || json.Unmarshal(raw, &list) != nil || list.Total != final.State.Saved || len(list.Records) == 0 {
			t.Fatalf("排空后已保存列表不一致: HTTP=%d err=%v offset=%d", code, err, offset)
		}
		for _, item := range list.Records {
			if item.ID == "" || seenIDs[item.ID] {
				t.Fatal("已保存列表存在空 ID 或重复记录")
			}
			seenIDs[item.ID] = true
			code, raw, err := queuePressureHTTP(adminClient, h, "GET", api+"/detail?id="+url.QueryEscape(item.ID), adminKey, nil)
			var record savedRecord
			if err != nil || code != 200 || json.Unmarshal(raw, &record) != nil {
				t.Fatalf("已保存详情读取失败: HTTP=%d err=%v", code, err)
			}
			index, err := queuePressureIndex(record.Text, attempted)
			if err != nil || seenInputs[index] || len(record.Text) != 64<<10 || sha256.Sum256([]byte(record.Text)) != expected[index] {
				t.Fatal("已保存正文出现损坏、截断、重复或非合成提交（不输出正文）")
			}
			seenInputs[index] = true
			objectKnown := false
			for _, id := range monitored {
				objectKnown = objectKnown || record.ObjectID == id
			}
			if record.ID != item.ID || record.ObjectID != item.ObjectID || !objectKnown || record.Name == "" || record.ReceivedAt.IsZero() || record.Protocol != "chat-completions" || record.Model != "audit-test-model" || record.Truncated {
				t.Fatal("已保存白名单元数据与对象关联不一致")
			}
		}
		offset += len(list.Records)
	}
	if int64(len(seenInputs)) != final.State.Saved {
		t.Fatal("管理 API 返回的完整已保存记录数量不匹配")
	}
	if item := gapItem(t, readGapAlerts(t, h), "queue_full"); item.AffectedCount == nil || *item.AffectedCount != final.State.Gaps["queue_full"] || item.CountAccuracy != "exact" || item.Active {
		t.Fatal("队列排空后缺口历史次数不准确或被误报为持续故障")
	}
	if restartStatus(t, h).State.GapHistory["queue_full"] == "" {
		t.Fatal("真实队列满没有保留基础缺口信号")
	}
	assertChatPrivacy(t, h)
	afterQueries := queuePressureStatus(t, adminClient, h)
	if afterQueries.State.Saved != final.State.Saved || afterQueries.State.Queued != 0 || afterQueries.State.Gaps["queue_full"] != final.State.Gaps["queue_full"] {
		t.Fatal("排空后查询改变了保存或缺口计数")
	}
	t.Logf("全部已保存详情完整摘要校验通过: records_total=%d verified_details=%d；captured 不另造计数，HTTP提交=%d=已保存%d+queue_full缺口%d", len(seenIDs), len(seenInputs), attempted, final.State.Saved, final.State.Gaps["queue_full"])
	h.stop()
	restarted := startHostWithManagementAuth(t, 1<<30, true, upstream.URL+"/v1", true, h.stateDir)
	if restartStatus(t, restarted).State.GapHistory["queue_full"] == "" {
		t.Fatal("正常排空重启抹掉了队列满缺口")
	}
}
