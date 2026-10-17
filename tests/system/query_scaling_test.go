// 本文件经真实管理HTTP验证一页记录后的详情核对不会反复解析全量历史；不访问私有索引或队列。
package system

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestPagedDetailsRemainBounded 验证较早的一页100条64KiB记录可以快速逐条读取且正文/ID一致，锁定CI超时回归。
func TestPagedDetailsRemainBounded(t *testing.T) {
	h := startHostWithManagementAuth(t, 1<<30, true, "", true)
	h.monitorAll(t)
	// 256条形成约16MiB的 SQLite 正文，100条页面位于较早历史；仅短列表不能暴露重复全扫描的成本。
	const records = 256
	for i := 0; i < records; i++ {
		// 19字节序号前缀加65500字节正文，低于64KiB且接近压力用例大小。
		text := fmt.Sprintf("query-scaling-%04d:", i) + strings.Repeat("s", 65500)
		body, _ := json.Marshal(map[string]any{"model": "audit-test-model", "messages": []map[string]string{{"role": "user", "content": text}}})
		h.request(t, "POST", "/v1/chat/completions", keyA, body)
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, raw := h.request(t, "GET", api+"/status", adminKey, nil)
			var state struct {
				State struct {
					Saved int `json:"saved"`
				} `json:"state"`
			}
			if json.Unmarshal(raw, &state) != nil {
				t.Fatal("管理状态解析失败")
			}
			if state.State.Saved == i+1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("合成记录未保存")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	code, raw := h.search(t, adminKey, map[string]any{"offset": 156, "limit": 100})
	var page struct {
		Records []savedRecord `json:"records"`
	}
	if code != 200 || json.Unmarshal(raw, &page) != nil || len(page.Records) != 100 {
		t.Fatal("回归页面不完整")
	}
	started := time.Now()
	for _, item := range page.Records {
		code, raw := h.request(t, "GET", api+"/detail?id="+item.ID, adminKey, nil)
		var detail savedRecord
		if code != 200 || json.Unmarshal(raw, &detail) != nil || detail.ID != item.ID || !strings.HasPrefix(detail.Text, "query-scaling-") || len(detail.Text) != 65519 {
			t.Fatal("分页详情正文或ID不一致")
		}
	}
	elapsed := time.Since(started)
	t.Logf("100条较早历史详情核对耗时: %s", elapsed)
	// 该期限覆盖本地HTTP开销，不是生产吞吐保证；旧的每条全扫描实现会在此路径超限。
	if elapsed > 2*time.Second {
		t.Fatalf("详情逐条核对发生历史重复扫描: %s", elapsed)
	}
}
