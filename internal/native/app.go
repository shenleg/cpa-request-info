// 本文件将官方 JSON RPC 适配到审计深模块；不保留管理头、认证 Key 或原协议输入。
package native

import (
	"embed"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/shenleg/cpa-request-info/internal/audit"
	"gopkg.in/yaml.v3"
)

//go:embed assets/index.html
var assets embed.FS

// HostCall 同步请求宿主只读契约；payload 不含认证 Key，返回已校验 envelope 的 JSON 结果。
// 宿主不可用时返回无敏感上下文的错误，不能将其解释成成功采集。
type HostCall func(method string, payload any) (json.RawMessage, error)

// App 管理插件生命周期与管理路由，不持有原始模型请求或认证头。
type App struct {
	mu        sync.Mutex
	host      HostCall
	collector *audit.Collector
	dir       string
	budget    int64
	available bool
	reason    string
}

// New 创建默认未监控任何对象的插件；host 提供可信对象目录，返回实例，不执行 I/O。
func New(host HostCall) *App { return &App{host: host, reason: "not_initialized"} }

// Close 停止新采集并关闭当前写入者；宿主须先停止新回调，无返回值，失败只影响审计。
func (a *App) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.available = false
	if a.collector != nil {
		a.collector.Close()
		a.collector = nil
	}
}

// Handle 将方法及借用 JSON 输入映射到公开契约，返回可序列化结果；非法方法返回固定错误。
func (a *App) Handle(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		a.configure(raw)
		return map[string]any{"schema_version": 6, "metadata": map[string]any{"Name": "输入审计", "Version": "0.1.0", "Author": "cpa-request-info", "GitHubRepository": "https://github.com/shenleg/cpa-request-info"}, "capabilities": map[string]bool{"management_api": true, "input_audit_observer": true, "input_audit_snapshot": true}}, nil
	case "management.register":
		type route struct{ Method, Path string }
		type resource struct{ Path, Menu, Description string }
		return struct {
			Routes    []route    `json:"routes"`
			Resources []resource `json:"resources"`
		}{[]route{{"GET", "/plugins/request-input-audit/objects"}, {"POST", "/plugins/request-input-audit/monitor"}, {"POST", "/plugins/request-input-audit/remove"}, {"POST", "/plugins/request-input-audit/rename"}, {"POST", "/plugins/request-input-audit/pause"}, {"GET", "/plugins/request-input-audit/records"}, {"GET", "/plugins/request-input-audit/detail"}, {"GET", "/plugins/request-input-audit/status"}}, []resource{{"/index.html", "输入审计", "最小安全 Chat Completions 审计闭环"}}}, nil
	case "management.handle":
		var req managementRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, errors.New("invalid_management_request")
		}
		return a.management(req), nil
	case "input_audit.prepare":
		var event audit.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, errors.New("invalid_preparation")
		}
		a.mu.Lock()
		c, available := a.collector, a.available
		a.mu.Unlock()
		if c == nil || !available {
			return audit.Snapshot{Version: 2, Decision: "unavailable"}, nil
		}
		return c.Prepare(event), nil
	case "input_audit.observe":
		var event audit.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, errors.New("invalid_observation")
		}
		a.mu.Lock()
		c, available := a.collector, a.available
		a.mu.Unlock()
		if c != nil && available {
			c.Observe(event)
		}
		return struct{}{}, nil
	case "plugin.quiesce", "plugin.shutdown":
		a.Close()
		return struct{}{}, nil
	default:
		return nil, errors.New("unsupported_method")
	}
}

func (a *App) configure(raw []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.available = false
	a.reason = "observer_unavailable"
	var life struct {
		ConfigYAML    []byte `json:"config_yaml"`
		SchemaVersion uint32 `json:"schema_version"`
	}
	var cfg struct {
		Dir    string `yaml:"state-dir"`
		Budget int64  `yaml:"budget-bytes"`
	}
	if json.Unmarshal(raw, &life) != nil || life.SchemaVersion != 6 || yaml.Unmarshal(life.ConfigYAML, &cfg) != nil {
		a.reason = "configuration_invalid"
		return
	}
	if cfg.Budget == 0 {
		cfg.Budget = 1 << 30
	}
	caps, err := a.host("host.input_audit.capabilities", struct{}{})
	var capability struct {
		ContractVersion int    `json:"contract_version"`
		Available       bool   `json:"available"`
		StateDir        string `json:"state_dir"`
	}
	if err != nil || json.Unmarshal(caps, &capability) != nil || capability.ContractVersion != 2 || !capability.Available {
		return
	}
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil || cfg.Dir == "" || dir != capability.StateDir {
		a.reason = "configuration_invalid"
		return
	}
	if a.collector != nil && a.dir == dir && a.budget == cfg.Budget {
		a.available = true
		a.reason = ""
		return
	}
	if a.collector != nil {
		a.collector.Close()
		a.collector = nil
	}
	c, err := audit.Open(dir, cfg.Budget)
	if err != nil {
		a.reason = "storage_unavailable"
		return
	}
	a.collector = c
	a.dir = dir
	a.budget = cfg.Budget
	a.available = true
	a.reason = ""
}

type managementRequest struct {
	Method, Path string
	Body         []byte
	Query        map[string][]string
}

func (a *App) management(req managementRequest) any {
	if strings.HasSuffix(req.Path, "/index.html") {
		html, err := assets.ReadFile("assets/index.html")
		if err != nil {
			return responseJSON(500, map[string]string{"error": "asset_unavailable"})
		}
		return response(200, "text/html; charset=utf-8", html)
	}
	a.mu.Lock()
	c, available, reason, dir := a.collector, a.available, a.reason, a.dir
	a.mu.Unlock()
	if strings.HasSuffix(req.Path, "/status") {
		// 身份文件/目录可能在启动后损坏，不能只展示曾经成功的握手。
		caps, err := a.host("host.input_audit.capabilities", struct{}{})
		var live struct {
			Available       bool   `json:"available"`
			ContractVersion int    `json:"contract_version"`
			StateDir        string `json:"state_dir"`
			BridgeFailures  uint64 `json:"bridge_failures"`
		}
		if err != nil || json.Unmarshal(caps, &live) != nil || !live.Available || live.ContractVersion != 2 || (dir != "" && live.StateDir != dir) {
			available = false
			reason = "observer_unavailable"
		}
		var state any = map[string]any{"available": false, "reason": reason}
		collectorReason := ""
		if c != nil {
			snapshot := c.Status()
			collectorReason = snapshot.Reason
			// 桥接发生在Collector之前；只合并响应副本，避免每次刷新重复增加同一缺口。
			old := snapshot.Gaps["observer_unavailable"]
			if live.BridgeFailures > ^uint64(0)-old {
				snapshot.Gaps["observer_unavailable"] = ^uint64(0)
			} else {
				snapshot.Gaps["observer_unavailable"] = old + live.BridgeFailures
			}
			state = snapshot
		}
		if live.BridgeFailures > 0 && reason == "" && collectorReason == "" {
			reason = "observer_bridge_error"
		}
		return responseJSON(200, map[string]any{"available": available, "reason": reason, "supported": "chat-completions-last-user-string", "retention": "not_implemented", "state": state})
	}
	if strings.HasSuffix(req.Path, "/objects") {
		catalog, catalogErr := a.catalog()
		known := map[string]audit.Object{}
		history := map[string]bool{}
		if c != nil {
			known = c.Objects()
			var err error
			history, err = c.HistoryObjects()
			if err != nil {
				return responseJSON(503, map[string]string{"error": "storage_unavailable"})
			}
		}
		// 目录失效不妨碍已知历史查询，但不能把缓存对象当作当前可认证Key。
		union := make(map[string]object, len(known)+len(catalog))
		for id, meta := range known {
			union[id] = object{ID: id, Name: meta.Name, Monitored: meta.Monitored, HasHistory: history[id]}
		}
		for id := range history {
			if _, exists := union[id]; !exists {
				union[id] = object{ID: id, HasHistory: true}
			}
		}
		if catalogErr == nil {
			for _, current := range catalog {
				entry := union[current.ID]
				entry.ID, entry.Valid = current.ID, true
				union[current.ID] = entry
			}
		}
		objects := make([]object, 0, len(union))
		for _, entry := range union {
			objects = append(objects, entry)
		}
		sort.Slice(objects, func(i, j int) bool { return objects[i].ID < objects[j].ID })
		return responseJSON(200, map[string]any{"objects": objects, "available": available && catalogErr == nil})
	}
	if c == nil {
		return responseJSON(503, map[string]string{"error": reason})
	}
	if strings.HasSuffix(req.Path, "/monitor") {
		if !available {
			return responseJSON(503, map[string]string{"error": reason})
		}
		if len(req.Body) > 2048 {
			return responseJSON(400, map[string]string{"error": "invalid_monitor"})
		}
		var selection struct {
			ObjectID string `json:"object_id"`
			Name     string `json:"name"`
		}
		if json.Unmarshal(req.Body, &selection) != nil {
			return responseJSON(400, map[string]string{"error": "invalid_monitor"})
		}
		objects, err := a.catalog()
		if err != nil {
			return responseJSON(503, map[string]string{"error": "observer_unavailable"})
		}
		found := false
		for _, obj := range objects {
			if obj.ID == selection.ObjectID {
				found = true
			}
		}
		if !found || c.Monitor(selection.ObjectID, selection.Name) != nil {
			return responseJSON(400, map[string]string{"error": "monitor_rejected"})
		}
		return responseJSON(200, map[string]bool{"saved": true})
	}
	if strings.HasSuffix(req.Path, "/rename") {
		var selection struct {
			ObjectID string `json:"object_id"`
			Name     string `json:"name"`
		}
		if len(req.Body) > 2048 || json.Unmarshal(req.Body, &selection) != nil || c.Rename(selection.ObjectID, selection.Name) != nil {
			return responseJSON(400, map[string]string{"error": "rename_rejected"})
		}
		return responseJSON(200, map[string]bool{"saved": true})
	}
	if strings.HasSuffix(req.Path, "/pause") {
		var selection struct {
			Paused *bool `json:"paused"`
		}
		if len(req.Body) > 2048 || json.Unmarshal(req.Body, &selection) != nil || selection.Paused == nil || c.SetPaused(*selection.Paused) != nil {
			return responseJSON(400, map[string]string{"error": "pause_rejected"})
		}
		return responseJSON(200, map[string]bool{"saved": true})
	}
	if strings.HasSuffix(req.Path, "/remove") {
		var selection struct {
			ObjectID string `json:"object_id"`
		}
		if len(req.Body) > 2048 || json.Unmarshal(req.Body, &selection) != nil || c.Remove(selection.ObjectID) != nil {
			return responseJSON(400, map[string]string{"error": "remove_rejected"})
		}
		return responseJSON(200, map[string]bool{"saved": true})
	}
	if strings.HasSuffix(req.Path, "/records") {
		offset, err := queryNumber(req.Query, "offset", 0, 0, 10000000)
		if err != nil {
			return responseJSON(400, map[string]string{"error": "invalid_pagination"})
		}
		limit, err := queryNumber(req.Query, "limit", 20, 1, 100)
		if err != nil {
			return responseJSON(400, map[string]string{"error": "invalid_pagination"})
		}
		objectID := queryValue(req.Query, "object_id")
		if objectID != "" {
			if len(objectID) != 64 || strings.Trim(objectID, "0123456789abcdefABCDEF") != "" {
				return responseJSON(400, map[string]string{"error": "invalid_object"})
			}
		}
		records, total, err := c.List(offset, limit, objectID)
		if err != nil {
			return responseJSON(503, map[string]string{"error": "storage_unavailable"})
		}
		names := c.Monitors()
		items := make([]any, 0, len(records))
		for _, r := range records {
			preview := r.Text
			if len(preview) > 512 {
				preview = preview[:512]
				for !utf8.ValidString(preview) {
					preview = preview[:len(preview)-1]
				}
			}
			items = append(items, map[string]any{"id": r.ID, "object_id": r.ObjectID, "name": names[r.ObjectID], "received_at": r.ReceivedAt, "protocol": r.Protocol, "model": r.Model, "truncated": r.Truncated, "preview": preview})
		}
		return responseJSON(200, map[string]any{"records": items, "total": total, "offset": offset, "limit": limit})
	}
	if strings.HasSuffix(req.Path, "/detail") {
		id := queryValue(req.Query, "id")
		if len(id) > 128 || id == "" {
			return responseJSON(400, map[string]string{"error": "invalid_id"})
		}
		r, err := c.Detail(id)
		if err != nil {
			return responseJSON(404, map[string]string{"error": "not_found"})
		}
		return responseJSON(200, map[string]any{"id": r.ID, "object_id": r.ObjectID, "name": c.Monitors()[r.ObjectID], "received_at": r.ReceivedAt, "protocol": r.Protocol, "model": r.Model, "truncated": r.Truncated, "text": r.Text})
	}
	return responseJSON(404, map[string]string{"error": "not_found"})
}

type object struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Monitored  bool   `json:"monitored"`
	Valid      bool   `json:"valid"`
	HasHistory bool   `json:"has_history"`
}

func (a *App) catalog() ([]object, error) {
	raw, err := a.host("host.input_audit.catalog", struct{}{})
	if err != nil {
		return nil, err
	}
	var catalog struct {
		Available bool     `json:"available"`
		Objects   []object `json:"objects"`
	}
	if json.Unmarshal(raw, &catalog) != nil || !catalog.Available {
		return nil, errors.New("observer_unavailable")
	}
	return catalog.Objects, nil
}
func queryValue(query map[string][]string, key string) string {
	if len(query[key]) > 0 {
		return query[key][0]
	}
	return ""
}
func queryNumber(query map[string][]string, key string, fallback, min, max int) (int, error) {
	s := queryValue(query, key)
	if s == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, errors.New("invalid_pagination")
	}
	return n, nil
}
func response(code int, contentType string, body []byte) any {
	return struct {
		StatusCode int
		Headers    http.Header
		Body       []byte
	}{code, http.Header{"Content-Type": {contentType}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}, body}
}
func responseJSON(code int, value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return response(500, "application/json", []byte(`{"error":"serialization_failed"}`))
	}
	return response(code, "application/json", raw)
}
