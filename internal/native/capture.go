// 本文件适配官方 HTTP 执行前回调，瞬时分类用途、借用正文和 caller_scope；发现集合只保留有界匿名 ID。
package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

// 与持久对象配置同为 1024 上限，防止未选择的调用者让运行期目录无限增长。
const maxObservedObjects = 1024

type observedCatalog struct {
	mu   sync.Mutex
	ids  map[string]struct{}
	full bool
}

func (o *observedCatalog) add(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, exists := o.ids[id]; exists {
		return
	}
	if len(o.ids) == maxObservedObjects {
		o.full = true
		return
	}
	o.ids[id] = struct{}{}
	o.full = len(o.ids) == maxObservedObjects
}

func (o *observedCatalog) snapshot() (map[string]bool, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ids := make(map[string]bool, len(o.ids))
	for id := range o.ids {
		ids[id] = true
	}
	return ids, o.full
}

// capture 不持有 App 管理锁、不调用宿主或磁盘；无效外部输入报告实际 invalid_event，始终不返回请求修改。
func (a *App) capture(raw []byte) (any, error) {
	state := a.admission.Load()
	invalid := func() (any, error) {
		if state.collector != nil {
			state.collector.ReportInputFailure()
		}
		return nil, errors.New("invalid_interception")
	}
	fields, err := jsonObject(raw)
	if err != nil {
		return invalid()
	}
	metadata, err := jsonObject(fields["Metadata"])
	if err != nil {
		return invalid()
	}
	var source string
	if value, exists := metadata["source"]; exists {
		var decoded *string
		if json.Unmarshal(value, &decoded) != nil || decoded == nil {
			return invalid()
		}
		source = *decoded
	}
	// 插件模型调用继承的来源不是客户端 HTTP 输入；先排除，不能把其补回的上下文保存。
	if source == "plugin_host_model_callback" {
		return struct{}{}, nil
	}
	websocket, err := websocketHeaders(fields["Headers"])
	if err != nil {
		return invalid()
	}
	if websocket {
		return struct{}{}, nil
	}
	var path, format *string
	if json.Unmarshal(metadata["request_path"], &path) != nil || path == nil || json.Unmarshal(fields["SourceFormat"], &format) != nil || format == nil {
		return invalid()
	}
	protocol := httpProtocol(*path, *format)
	if protocol == "" {
		return struct{}{}, nil
	}
	var id, scope string
	var body []byte
	if json.Unmarshal(fields["RequestID"], &id) != nil || !validExecutionID(id) || json.Unmarshal(metadata["caller_scope"], &scope) != nil || strings.TrimSpace(scope) == "" || json.Unmarshal(fields["Body"], &body) != nil || !utf8.Valid(body) || !json.Valid(body) {
		return invalid()
	}
	if state.available && state.collector != nil {
		// CaptureExecution 返回后才登记；即便目录已满，已选对象的正文采集仍不受发现集合影响。
		objectID := state.collector.CaptureExecution(id, scope, protocol, requestPurpose(*path, fields["Headers"]), body)
		if objectID != "" {
			state.observed.add(objectID)
		}
	}
	return struct{}{}, nil
}

func validExecutionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func httpProtocol(path, format string) string {
	switch {
	case path == "/v1/chat/completions" && format == "openai":
		return "chat-completions"
	case path == "/v1/messages" && format == "claude":
		return "anthropic-messages"
	case format == "openai-response":
		switch path {
		case "/v1/responses", "/v1/responses/compact", "/backend-api/codex/responses", "/backend-api/codex/responses/compact":
			return "responses"
		}
	}
	return ""
}

// websocketHeaders 只读取升级相关字段；Connection 是逗号分隔且不区分大小写的 token，不查看任何认证值。
func websocketHeaders(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, nil
	}
	headers, err := jsonObject(raw)
	if err != nil {
		return false, err
	}
	var upgrade, connection bool
	for name, rawValue := range headers {
		if !strings.EqualFold(name, "Upgrade") && !strings.EqualFold(name, "Connection") {
			continue
		}
		var values []string
		if json.Unmarshal(rawValue, &values) != nil {
			return false, errors.New("invalid_headers")
		}
		for _, value := range values {
			for _, token := range strings.Split(value, ",") {
				if strings.EqualFold(name, "Upgrade") && strings.EqualFold(strings.TrimSpace(token), "websocket") {
					upgrade = true
				}
				if strings.EqualFold(name, "Connection") && strings.EqualFold(strings.TrimSpace(token), "upgrade") {
					connection = true
				}
			}
		}
	}
	return upgrade && connection, nil
}

// jsonObject 在外部 JSON 边界拒绝非对象、重复字段、非法 UTF-8 和尾随值；错误不携带输入。
func jsonObject(raw []byte) (map[string]json.RawMessage, error) {
	invalid := errors.New("invalid_json_object")
	if !utf8.Valid(raw) {
		return nil, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, invalid
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, invalid
		}
		if _, exists := fields[name]; exists {
			return nil, invalid
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, invalid
		}
		fields[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, invalid
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, invalid
	}
	return fields, nil
}
