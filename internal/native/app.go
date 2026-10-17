// 本文件将官方 JSON RPC 适配到审计深模块；不保留管理头、认证 Key 或原协议输入。
package native

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/shenleg/cpa-request-info/internal/audit"
	"gopkg.in/yaml.v3"
)

//go:embed assets/index.html
var assets embed.FS

// HostCall 仅用于向宿主发送固定诊断日志；payload 不含身份或输入，返回宿主 JSON 结果。
// 宿主调用失败返回错误，不影响模型请求。
type HostCall func(method string, payload any) (json.RawMessage, error)

// App 管理插件生命周期与管理路由，不持有原始模型请求或认证头。
type App struct {
	mu            sync.Mutex
	host          HostCall
	collector     *audit.Collector
	observed      *observedCatalog
	dir           string
	available     bool
	reason        string
	configured    bool
	budget        int64
	loggedReason  string
	pendingAlerts audit.GapAlerts
	pendingReason string
	// 模型回调只读已发布的小状态，不等待管理查询或恢复扫描持有的锁。
	admission atomic.Pointer[admissionState]
}

type admissionState struct {
	collector *audit.Collector
	observed  *observedCatalog
	available bool
}

// New 创建默认未监控任何对象的插件；host 仅发送诊断日志，返回实例，不执行 I/O 或返回错误。
func New(host HostCall) *App {
	a := &App{host: host, reason: "not_initialized"}
	a.publishLocked()
	return a
}

// publishLocked 发布不可变的准入状态；调用者持有管理锁，新旧快照都不保留输入。
func (a *App) publishLocked() {
	a.admission.Store(&admissionState{collector: a.collector, observed: a.observed, available: a.available})
}

// Close 停止新采集并关闭当前写入者；宿主须先停止新回调，无返回值，失败只影响审计。
func (a *App) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.available = false
	a.configured = false
	a.admission.Store(&admissionState{})
	if a.collector != nil {
		a.collector.Close()
		a.collector = nil
		a.observed = nil
	}
}

// Handle 将方法及借用 JSON 输入映射到公开契约，返回可序列化结果；非法方法返回固定错误。
func (a *App) Handle(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		a.configure(raw)
		return map[string]any{"schema_version": 6, "metadata": map[string]any{"Name": "输入审计", "Version": "0.3.0", "Author": "cpa-request-info", "GitHubRepository": "https://github.com/shenleg/cpa-request-info"}, "capabilities": map[string]bool{"management_api": true, "request_interceptor": true}}, nil
	case "management.register":
		type route struct{ Method, Path string }
		type resource struct{ Path, Menu, Description string }
		return struct {
			Routes    []route    `json:"routes"`
			Resources []resource `json:"resources"`
		}{[]route{{"GET", "/plugins/request-input-audit/objects"}, {"GET", "/plugins/request-input-audit/models"}, {"POST", "/plugins/request-input-audit/monitor"}, {"POST", "/plugins/request-input-audit/remove"}, {"POST", "/plugins/request-input-audit/rename"}, {"POST", "/plugins/request-input-audit/pause"}, {"GET", "/plugins/request-input-audit/settings"}, {"POST", "/plugins/request-input-audit/settings"}, {"POST", "/plugins/request-input-audit/records/search"}, {"GET", "/plugins/request-input-audit/detail"}, {"GET", "/plugins/request-input-audit/status"}, {"POST", "/plugins/request-input-audit/gaps/acknowledge"}}, []resource{{"/index.html", "输入审计", "Chat Completions、Responses HTTP 与 Anthropic Messages 执行输入记录"}}}, nil
	case "management.handle":
		var req managementRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, errors.New("invalid_management_request")
		}
		return a.management(req), nil
	case "request.intercept_before":
		return a.capture(raw)
	case "request.intercept_after":
		// 官方空修改结果不设置 Body、Headers 或 Terminate；执行结果不决定输入是否保存。
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
	a.configured = false
	a.reason = "configuration_invalid"
	defer a.diagnoseLocked()
	defer a.publishLocked()
	var life struct {
		ConfigYAML    []byte `json:"config_yaml"`
		SchemaVersion uint32 `json:"schema_version"`
	}
	var cfg struct {
		Dir    string `yaml:"state-dir"`
		Budget int64  `yaml:"budget-bytes"`
	}
	if json.Unmarshal(raw, &life) != nil || life.SchemaVersion != 6 || yaml.Unmarshal(life.ConfigYAML, &cfg) != nil {
		return
	}
	if cfg.Budget == 0 {
		cfg.Budget = 1 << 30
	}
	if cfg.Budget < 4<<20 || cfg.Budget > 1<<30 {
		return
	}
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil || cfg.Dir == "" {
		return
	}
	// 每个采集器独占自己的发现集合；旧回调晚到时只能登记到旧集合，不能污染新目录。
	if a.collector != nil && a.dir != dir {
		a.admission.Store(&admissionState{})
		a.collector.Close()
		a.collector = nil
		a.observed = nil
	}
	a.dir, a.budget, a.configured = dir, cfg.Budget, true
	a.refreshLocked()
}

// refreshLocked 在管理查询或重配置时尝试打开存储；可能执行磁盘 I/O，不能用于模型回调。
func (a *App) refreshLocked() {
	defer a.diagnoseLocked()
	defer a.publishLocked()
	if !a.configured {
		return
	}
	if a.collector == nil {
		c, err := audit.Open(a.dir, a.budget)
		if err != nil {
			a.available = false
			a.reason = "storage_unavailable"
			a.pendingFaultLocked(a.reason)
			return
		}
		a.collector = c
		a.observed = &observedCatalog{ids: make(map[string]struct{})}
		a.importPendingLocked()
	}
	a.reason = ""
	a.available = true
}

// diagnoseLocked 在故障状态变化时尽力通知宿主固定原因；不附带底层错误、身份、路径或正文。
func (a *App) diagnoseLocked() {
	reason := a.reason
	if reason == "" && a.collector != nil {
		reason = a.collector.Status().Reason
		if reason == "budget_exhausted" {
			reason = ""
		}
	}
	if reason == a.loggedReason {
		return
	}
	a.loggedReason = reason
	if reason != "" {
		_, _ = a.host("host.log", map[string]any{"level": "warn", "message": "input audit unavailable", "fields": map[string]string{"reason": reason}})
	}
}

type managementRequest struct {
	Method, Path string
	Body         []byte
	Query        map[string][]string
}

func (a *App) management(req managementRequest) any {
	path := strings.TrimPrefix(req.Path, "/v0/management")
	allowed := map[string]string{
		"/objects": "GET", "/models": "GET", "/monitor": "POST", "/remove": "POST", "/rename": "POST", "/pause": "POST",
		"/settings": "GET,POST", "/records/search": "POST", "/detail": "GET", "/status": "GET", "/gaps/acknowledge": "POST",
	}
	if strings.HasSuffix(path, "/index.html") {
		if req.Method != "GET" {
			return responseJSON(405, map[string]string{"error": "method_not_allowed"})
		}
		html, err := assets.ReadFile("assets/index.html")
		if err != nil {
			return responseJSON(500, map[string]string{"error": "asset_unavailable"})
		}
		return response(200, "text/html; charset=utf-8", html)
	}
	methods, exists := allowed[strings.TrimPrefix(path, "/plugins/request-input-audit")]
	if !strings.HasPrefix(path, "/plugins/request-input-audit/") || !exists {
		return responseJSON(404, map[string]string{"error": "not_found"})
	}
	if req.Method != methods && !(methods == "GET,POST" && (req.Method == "GET" || req.Method == "POST")) {
		return responseJSON(405, map[string]string{"error": "method_not_allowed"})
	}
	a.mu.Lock()
	if strings.HasSuffix(req.Path, "/status") {
		a.refreshLocked()
	}
	c, observed, available, reason := a.collector, a.observed, a.available, a.reason
	pending := a.pendingAlerts
	pending.Items = append([]audit.GapSummary{}, pending.Items...)
	if c == nil && strings.HasSuffix(req.Path, "/gaps/acknowledge") {
		var snapshot struct {
			Generation string  `json:"generation"`
			Revision   *uint64 `json:"revision"`
		}
		if len(req.Body) > 256 || json.Unmarshal(req.Body, &snapshot) != nil || snapshot.Revision == nil {
			a.mu.Unlock()
			return responseJSON(400, map[string]string{"error": "invalid_gap_snapshot"})
		}
		if snapshot.Generation != pending.Generation || *snapshot.Revision != pending.Revision {
			a.mu.Unlock()
			return responseJSON(409, map[string]string{"error": "gap_snapshot_changed"})
		}
		a.pendingAlerts.AcknowledgedRevision = pending.Revision
		a.pendingAlerts.Unread = false
		for i := range a.pendingAlerts.Items {
			a.pendingAlerts.Items[i].Unread = false
		}
		a.mu.Unlock()
		return responseJSON(200, map[string]bool{"acknowledged": true, "diagnostics_persisted": false})
	}
	a.mu.Unlock()
	if strings.HasSuffix(req.Path, "/status") {
		var state any = map[string]any{"available": false, "reason": reason, "diagnostics_persisted": false, "gap_alerts": pending}
		if c != nil {
			snapshot := c.Status()
			state = snapshot
		}
		return responseJSON(200, map[string]any{"available": available, "reason": reason, "capture_mode": "http_execution", "supported_protocols": []string{"chat-completions", "responses", "anthropic-messages"}, "responses_websocket": false, "coverage": "intercept_before_only_excludes_pre_route_failures_and_websocket", "time_source": "intercept_before", "model_source": "execution_body_may_be_rewritten", "catalog": "observed", "retention": "before_time_expiry_periodic_cleanup", "state": state})
	}
	if strings.HasSuffix(req.Path, "/objects") {
		catalog, full := observedObjects(observed)
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
		// 已选对象从配置恢复；Valid 只表示可以选择该匿名对象，不推断宿主 Key 当前有效性。
		union := make(map[string]object, len(known)+len(catalog))
		for id, meta := range known {
			union[id] = object{ID: id, Name: meta.Name, Monitored: meta.Monitored, Valid: true, HasHistory: history[id]}
		}
		for id := range history {
			if _, exists := union[id]; !exists {
				union[id] = object{ID: id, HasHistory: true}
			}
		}
		for id := range catalog {
			entry := union[id]
			entry.ID, entry.Valid = id, true
			union[id] = entry
		}
		objects := make([]object, 0, len(union))
		for _, entry := range union {
			objects = append(objects, entry)
		}
		sort.Slice(objects, func(i, j int) bool { return objects[i].ID < objects[j].ID })
		return responseJSON(200, map[string]any{"objects": objects, "available": available, "catalog": "observed", "catalog_full": full})
	}
	if c == nil {
		return responseJSON(503, map[string]string{"error": reason})
	}
	if strings.HasSuffix(req.Path, "/gaps/acknowledge") {
		var snapshot struct {
			Generation string  `json:"generation"`
			Revision   *uint64 `json:"revision"`
		}
		if len(req.Body) > 256 || json.Unmarshal(req.Body, &snapshot) != nil || snapshot.Revision == nil || snapshot.Generation == "" {
			return responseJSON(400, map[string]string{"error": "invalid_gap_snapshot"})
		}
		persisted, err := c.AcknowledgeGaps(snapshot.Generation, *snapshot.Revision)
		if err != nil {
			code := 503
			if err.Error() == "gap_snapshot_changed" {
				code = 409
			}
			return responseJSON(code, map[string]string{"error": err.Error()})
		}
		return responseJSON(200, map[string]bool{"acknowledged": true, "diagnostics_persisted": persisted})
	}
	if strings.HasSuffix(req.Path, "/settings") {
		if req.Method == "GET" {
			state := c.Status()
			return responseJSON(200, map[string]any{"max_text_bytes": state.MaxTextBytes, "retention_days": state.RetentionDays, "budget_bytes": state.BudgetBytes})
		}
		if len(req.Body) > 2048 {
			return responseJSON(400, map[string]string{"error": "invalid_settings"})
		}
		change, err := settingsChange(req.Body)
		if err != nil {
			return responseJSON(400, map[string]string{"error": "invalid_settings"})
		}
		if err := c.SetSettings(change); err != nil {
			if err.Error() == "retention_confirmation_required" {
				return responseJSON(409, map[string]string{"error": err.Error()})
			}
			return responseJSON(400, map[string]string{"error": "settings_rejected"})
		}
		return responseJSON(200, map[string]bool{"saved": true})
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
		objects, _ := observedObjects(observed)
		_, persisted := c.Objects()[selection.ObjectID]
		found := objects[selection.ObjectID] || persisted
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
	if strings.HasSuffix(req.Path, "/models") {
		if len(req.Query) != 0 {
			return responseJSON(400, map[string]string{"error": "invalid_query"})
		}
		models, err := c.Models()
		if err != nil {
			return responseJSON(503, map[string]string{"error": "storage_unavailable"})
		}
		return responseJSON(200, map[string]any{"models": models})
	}
	if strings.HasSuffix(req.Path, "/records/search") {
		if len(req.Query) != 0 {
			return responseJSON(400, map[string]string{"error": "invalid_query"})
		}
		offset, limit, filter, err := searchQuery(req.Body)
		if err != nil {
			return responseJSON(400, map[string]string{"error": err.Error()})
		}
		records, total, err := c.List(offset, limit, filter)
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
			items = append(items, map[string]any{"id": r.ID, "object_id": r.ObjectID, "name": names[r.ObjectID], "received_at": r.ReceivedAt, "protocol": r.Protocol, "model": r.Model, "request_reasoning": r.RequestReasoning, "purpose": r.Purpose, "truncated": r.Truncated, "extraction_incomplete": r.ExtractionIncomplete, "preview": preview})
		}
		return responseJSON(200, map[string]any{"records": items, "total": total, "offset": offset, "limit": limit})
	}
	if strings.HasSuffix(req.Path, "/detail") {
		id := queryValue(req.Query, "id")
		if len(req.Query) != 1 || len(req.Query["id"]) != 1 || !validExecutionID(id) {
			return responseJSON(400, map[string]string{"error": "invalid_id"})
		}
		r, err := c.Detail(id)
		if err != nil {
			return responseJSON(404, map[string]string{"error": "not_found"})
		}
		return responseJSON(200, map[string]any{"id": r.ID, "object_id": r.ObjectID, "name": c.Monitors()[r.ObjectID], "received_at": r.ReceivedAt, "protocol": r.Protocol, "model": r.Model, "request_reasoning": r.RequestReasoning, "purpose": r.Purpose, "truncated": r.Truncated, "extraction_incomplete": r.ExtractionIncomplete, "text": r.Text})
	}
	return responseJSON(404, map[string]string{"error": "not_found"})
}

// settingsChange 精确读取部分更新字段；拒绝大小写别名、重复键、null及无设置的确认请求。
func settingsChange(raw []byte) (audit.SettingsChange, error) {
	var change audit.SettingsChange
	invalid := errors.New("invalid_settings")
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return change, invalid
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return change, invalid
		}
		seen[name] = true
		switch name {
		case "max_text_bytes", "retention_days":
			var value *int
			if decoder.Decode(&value) != nil || value == nil {
				return change, invalid
			}
			if name == "max_text_bytes" {
				if *value < 1 || *value > 65536 {
					return change, invalid
				}
				change.MaxTextBytes = value
			} else {
				if *value < 1 || *value > 30 {
					return change, invalid
				}
				change.RetentionDays = value
			}
		case "budget_bytes":
			var value *int64
			if decoder.Decode(&value) != nil || value == nil || *value < 4<<20 || *value > 1<<30 {
				return change, invalid
			}
			change.BudgetBytes = value
		case "confirm_retention_shortening":
			var confirmed *bool
			if decoder.Decode(&confirmed) != nil || confirmed == nil {
				return change, invalid
			}
			change.ConfirmRetentionShortening = *confirmed
		default:
			return change, invalid
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return change, invalid
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || change.MaxTextBytes == nil && change.RetentionDays == nil && change.BudgetBytes == nil {
		return change, invalid
	}
	return change, nil
}

type object struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Monitored  bool   `json:"monitored"`
	Valid      bool   `json:"valid"`
	HasHistory bool   `json:"has_history"`
}

// observedObjects 允许尚未打开存储的管理状态返回空目录，不制造宿主 Key 全量目录。
func observedObjects(observed *observedCatalog) (map[string]bool, bool) {
	if observed == nil {
		return map[string]bool{}, false
	}
	return observed.snapshot()
}

// searchQuery 只从 POST JSON 读取搜索条件；拒绝未知、重复、null、类型和范围错误，不回显搜索值。
func searchQuery(raw []byte) (int, int, audit.Query, error) {
	invalid := errors.New("invalid_query")
	// 关键词最多64KiB；JSON逐字符转义最多六倍，再留出模型和日期字段的空间。
	if len(raw) > 512<<10 {
		return 0, 0, audit.Query{}, invalid
	}
	fields, err := jsonObject(raw)
	if err != nil {
		return 0, 0, audit.Query{}, invalid
	}
	query := make(map[string][]string, len(fields))
	for name, value := range fields {
		switch name {
		case "offset", "limit":
			var number *int
			if json.Unmarshal(value, &number) != nil || number == nil {
				return 0, 0, audit.Query{}, errors.New("invalid_pagination")
			}
			query[name] = []string{strconv.Itoa(*number)}
		case "object_id", "from", "to", "model", "q":
			var text *string
			if json.Unmarshal(value, &text) != nil || text == nil {
				return 0, 0, audit.Query{}, invalid
			}
			query[name] = []string{*text}
		default:
			return 0, 0, audit.Query{}, invalid
		}
	}
	offset, err := queryNumber(query, "offset", 0, 0, 10000000)
	if err != nil {
		return 0, 0, audit.Query{}, err
	}
	limit, err := queryNumber(query, "limit", 20, 1, 100)
	if err != nil {
		return 0, 0, audit.Query{}, err
	}
	filter, err := recordQuery(query)
	return offset, limit, filter, err
}

// recordQuery 在管理 HTTP 边界解析已保存记录的交集条件；错误只含固定原因，不回显不可信查询值。
func recordQuery(query map[string][]string) (audit.Query, error) {
	filter := audit.Query{ObjectID: queryValue(query, "object_id"), Model: queryValue(query, "model"), Keyword: queryValue(query, "q")}
	if filter.ObjectID != "" && (len(filter.ObjectID) != 64 || strings.Trim(filter.ObjectID, "0123456789abcdefABCDEF") != "") {
		return audit.Query{}, errors.New("invalid_object")
	}
	if len(filter.Model) > 1024 || len(filter.Keyword) > 65536 {
		return audit.Query{}, errors.New("invalid_query")
	}
	for _, bound := range []struct {
		name   string
		target **time.Time
	}{{"from", &filter.From}, {"to", &filter.To}} {
		if _, exists := query[bound.name]; exists {
			parsed, err := time.Parse(time.RFC3339Nano, queryValue(query, bound.name))
			if err != nil {
				return audit.Query{}, errors.New("invalid_time_range")
			}
			*bound.target = &parsed
		}
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return audit.Query{}, errors.New("invalid_time_range")
	}
	return filter, nil
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
