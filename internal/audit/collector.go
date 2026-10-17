// 本文件按接收快照提取末尾允许内容并有界异步采集；原请求正文只在 Observe 返回前借用。
package audit

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultBudget  int64 = 1 << 30
	minBudget      int64 = 4 << 20
	maxText              = 64 << 10
	maxLine              = 512 << 10
	segmentSize    int64 = 1 << 20
	maxQueuedBytes int64 = 8 << 20
	// 64 位平台的记录及队列计数136字节，另计思考对象40字节及可选预算8字节。
	queuedRecordOverhead = 184
	// 配置最多 1024 个对象、单份不足 512 KiB；预留两份原子替换峰值及目录/锁块空间。
	auxiliaryReserve int64 = 1 << 20
)

// Event 是宿主同步借用的只读入站事件；Body 不得在 Observe 返回后继续引用。
// 字段仅含无秘密身份、提交元数据及原协议正文，ReadError 表示宿主读取失败；Purpose 为有限用途分类。
type Event struct {
	ID         string         `json:"id"`
	ObjectID   string         `json:"object_id"`
	ReceivedAt string         `json:"received_at"`
	Protocol   string         `json:"protocol"`
	Purpose    RequestPurpose `json:"purpose"`
	Body       []byte         `json:"body"`
	ReadError  bool           `json:"read_error"`
	Snapshot   Snapshot       `json:"snapshot"`
}

// Record 是唯一允许持久化及查询的输入白名单；名称由管理层从已知对象目录关联。
// Purpose 只保存 main、turn、unknown；旧记录缺省时为空，不推断或补写历史用途。
// RequestReasoning 只描述 Before 请求声明；新空对象为未指定，旧 nil 为未记录。
type Record struct {
	ID                   string            `json:"id"`
	ObjectID             string            `json:"object_id"`
	ReceivedAt           string            `json:"received_at"`
	Protocol             string            `json:"protocol"`
	Purpose              RequestPurpose    `json:"purpose,omitempty"`
	Model                string            `json:"model"`
	RequestReasoning     *RequestReasoning `json:"request_reasoning,omitempty"`
	Text                 string            `json:"text"`
	Truncated            bool              `json:"truncated"`
	ExtractionIncomplete bool              `json:"extraction_incomplete,omitempty"`
}

// Recovery 描述启动发现的不确定损失；发现时刻不是损失时段，未知数量与边界始终以 null 输出。
type Recovery struct {
	Uncertain  bool    `json:"uncertain"`
	DetectedAt string  `json:"detected_at"`
	Reason     string  `json:"reason"`
	LostCount  *uint64 `json:"lost_count"`
	LostFrom   *string `json:"lost_from"`
	LostUntil  *string `json:"lost_until"`
}

// Status 是有界运行状态快照；Gaps 仅有固定原因码，不携带输入或底层错误上下文。
type Status struct {
	CapturePaused        bool              `json:"capture_paused"`
	Available            bool              `json:"available"`
	State                string            `json:"state"`
	Reason               string            `json:"reason"`
	Saved                int64             `json:"saved"`
	Queued               int64             `json:"queued"`
	Gaps                 map[string]uint64 `json:"gaps"`
	UsedBytes            int64             `json:"used_bytes"`
	BudgetBytes          int64             `json:"budget_bytes"`
	MaxTextBytes         int               `json:"max_text_bytes"`
	RetentionDays        int               `json:"retention_days"`
	ReservedBytes        int64             `json:"reserved_bytes"`
	ResumeRequiredBytes  int64             `json:"resume_required_bytes"`
	OverBudgetBytes      int64             `json:"over_budget_bytes"`
	CapacityGap          bool              `json:"capacity_gap"`
	DiagnosticsPersisted bool              `json:"diagnostics_persisted"`
	GapHistory           map[string]string `json:"gap_history"`
	Recovery             Recovery          `json:"recovery"`
	GapAlerts            GapAlerts         `json:"gap_alerts"`
}

// Collector 是审计深模块；所有存储、队列、解析和缺口诊断均由它管理。
// 须通过 Open 构造，关闭后仍可读取状态，但不能写入或查询已关闭的存储。
type Collector struct {
	mu          sync.Mutex
	ioMu        sync.Mutex
	root        *os.Root
	lock        *os.File
	directory   *os.File
	active      *os.File
	diagnostics *os.File
	// callerScopeSeed 只在持锁初始化并同步成功后发布，不向宿主暴露或缓存 caller_scope。
	callerScopeSeed []byte
	// diagnosticWake 只通知单写入者合并保存固定诊断，不在模型请求中等待磁盘。
	diagnosticWake       chan struct{}
	capacityLastAt       string
	sessionDirty         bool
	sessionStarted       bool
	diagnosticRevision   uint64
	bridgeFailures       uint64
	gapEntries           map[string]gapEntry
	alertRevision        uint64
	acknowledgedRevision uint64
	observerReason       string
	previousActive       map[string]gapEntry
	config               configuration
	configValid          bool
	generation           string
	segments             []segment
	// 只保留最近查询页（最多100条）的文件位置，不缓存正文或建立全量身份索引；由 ioMu 保护。
	pageHints   map[string]recordLocation
	queue       chan queuedRecord
	done        chan struct{}
	closeOnce   sync.Once
	closed      bool
	queuedBytes int64
	status      Status
}

type queuedRecord struct {
	record Record
	bytes  int64
}
type segment struct {
	name      string
	committed int64
	count     int
}

// Open 打开独占审计目录并扫描已提交记录；dir 必须不经过符号链接。
// budget 是缺少持久预算时的初始值，0 使用 1 GiB，合法范围 4 MiB 至 1 GiB；已保存设置优先。
// 返回可关闭的采集器；参数、随机代际来源、目录或锁不可用返回固定错误，身份或数据无效则返回停采实例。
func Open(dir string, budget int64) (*Collector, error) {
	if budget == 0 {
		budget = defaultBudget
	}
	if budget < minBudget || budget > defaultBudget {
		return nil, errors.New("invalid_budget")
	}
	// 非持久随机代际只区分重开的采集器，避免旧请求快照跨实例采集；不改变稳定审计对象身份。
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return nil, errors.New("generation_unavailable")
	}
	root, err := openDirectory(dir)
	if err != nil {
		return nil, errors.New("storage_unavailable")
	}
	c := &Collector{root: root, diagnosticWake: make(chan struct{}, 1), generation: hex.EncodeToString(generation[:]), config: configuration{Version: 4, BudgetBytes: budget, MaxTextBytes: maxText, RetentionDays: defaultRetentionDays, RetentionCaps: []retentionCap{}, Objects: make(map[string]Object)}, queue: make(chan queuedRecord, 64), done: make(chan struct{}), status: Status{Available: true, State: "active", BudgetBytes: budget, Gaps: map[string]uint64{}, GapHistory: map[string]string{}, Recovery: Recovery{Uncertain: true, DetectedAt: auditNow().UTC().Format(time.RFC3339Nano), Reason: "recovery_unavailable"}}}
	c.gapEntries = make(map[string]gapEntry)
	for _, key := range []string{"read_error", "invalid_event", "invalid_json", "unsupported_protocol", "unsupported_content", "metadata_too_large", "queue_full", "storage_error", "budget_exhausted", "observer_unavailable"} {
		c.status.Gaps[key] = 0
	}
	if err = c.initialize(); err != nil {
		c.releaseFiles()
		return nil, errors.New("storage_unavailable")
	}
	go c.writeLoop()
	return c, nil
}

// CaptureExecution 处理官方 Before 已收到的一次 HTTP 执行；id 为官方 RequestID，callerScope 为已认证 caller_scope 摘要。
// protocol 为 chat-completions、responses 或 anthropic-messages；不接受 WebSocket。
// purpose 为有限用途分类；skip 在身份派生和资格判断后正常跳过正文，不产生缺口。
// body 仅在调用期间借用，返回私有种子派生的匿名 ObjectID；未监控时不提取正文，身份不可用返回空串。
// 无返回错误或磁盘 I/O；无效输入、提取和入队失败只更新缺口，存储故障保持停采，不推断未收到的回调。
func (c *Collector) CaptureExecution(id, callerScope, protocol string, purpose RequestPurpose, body []byte) string {
	if len(c.callerScopeSeed) == 0 {
		return ""
	}
	if callerScope == "" {
		c.ReportInputFailure()
		return ""
	}
	mac := hmac.New(sha256.New, c.callerScopeSeed)
	mac.Write([]byte("cpa-request-info:object-id:caller-scope:v1\x00"))
	mac.Write([]byte(callerScope))
	objectID := hex.EncodeToString(mac.Sum(nil))
	event := Event{ID: id, ObjectID: objectID, ReceivedAt: auditNow().UTC().Format(time.RFC3339Nano), Protocol: protocol, Purpose: purpose}
	event.Snapshot = c.Prepare(event)
	if purpose == PurposeSkip {
		return objectID
	}
	// Prepare 已记录无效元数据；停采或未监控都不借用正文，不再把 unavailable 快照误报为无效回调。
	if event.Snapshot.Decision != "capture" {
		return objectID
	}
	if protocol != "chat-completions" && protocol != "responses" && protocol != "anthropic-messages" {
		c.gap("unsupported_protocol")
		return objectID
	}
	event.Body = body
	c.Observe(event)
	return objectID
}

// ReportInputFailure 报告实际收到但无法构成有效事件的回调输入；无参数、返回值或异常。
// 只增加 invalid_event 并异步保存诊断，不声称知道未收到的回调数量或内容。
func (c *Collector) ReportInputFailure() {
	c.gap("invalid_event")
}

// Observe 按 event 的接收快照同步提取三协议 HTTP 或 Responses WebSocket 本次允许输入并非阻塞入队，不复核当前配置。
// event.Purpose 应为 main、turn、unknown，skip 已由 CaptureExecution 跳过；Body 仅在调用期间借用。
// 无返回值，解析及容量失败仅增加固定缺口诊断，不影响宿主请求；Body 不进入采集器。
func (c *Collector) Observe(event Event) {
	c.mu.Lock()
	accepting := !c.closed && c.status.State == "active"
	reason := c.status.Reason
	generation := c.generation
	c.mu.Unlock()
	if event.Snapshot.Version != 3 || event.Snapshot.CollectorGeneration != generation {
		c.gap("observer_unavailable")
		return
	}
	if event.Snapshot.Decision == "skip" {
		return
	}
	if event.Snapshot.Decision != "capture" || event.Snapshot.MaxTextBytes < 1 || event.Snapshot.MaxTextBytes > maxText {
		c.gap("invalid_event")
		return
	}
	// 名单和主动暂停只在 Prepare 决定；此处只能因真实存储故障/关闭拒绝旧副本。
	if !accepting && reason != "budget_exhausted" {
		c.gap("storage_error")
		return
	}
	// 容量暂停仍判断预期跳过；只有本应保存的新输入才在入队边界记容量缺口，不将工具续跑误报为丢失。
	if event.ReadError {
		c.gap("read_error")
		return
	}
	if !validEvent(event) {
		c.gap("invalid_event")
		return
	}
	if !supportedProtocol(event.Protocol) {
		c.gap("unsupported_protocol")
		return
	}
	// struct 解码会把 Role 等大小写别名覆盖 role；必须精确读取协议字段并拒绝重复键，避免工具/指令泄漏。
	request, err := protocolObject(event.Body)
	if err != nil {
		c.gap("invalid_json")
		return
	}
	var text string
	var truncated, incomplete bool
	if event.Protocol == "responses" || event.Protocol == "responses-websocket" {
		if event.Protocol == "responses-websocket" {
			var kind string
			if json.Unmarshal(request["type"], &kind) != nil {
				c.gap("unsupported_content")
				return
			}
			switch kind {
			case "response.create", "response.append", "response.steer":
				// 后续帧可能只依赖宿主已有上下文，不能把恢复内容当作本次新输入。
				if _, explicit := request["input"]; !explicit {
					return
				}
			case "response.interrupt", "response.cancel":
				return
			default:
				c.gap("unsupported_content")
				return
			}
		}
		text, truncated, incomplete = responsesContent(request["input"], event.Snapshot.MaxTextBytes)
	} else {
		var messages []json.RawMessage
		if json.Unmarshal(request["messages"], &messages) != nil || len(messages) == 0 {
			c.gap("unsupported_content")
			return
		}
		last, err := protocolObject(messages[len(messages)-1])
		if err != nil {
			c.gap("invalid_json")
			return
		}
		var role string
		if json.Unmarshal(last["role"], &role) != nil {
			c.gap("unsupported_content")
			return
		}
		if role == "assistant" || role == "tool" || role == "function" || role == "system" || role == "developer" {
			return
		}
		if role != "user" {
			c.gap("unsupported_content")
			return
		}
		if event.Protocol == "anthropic-messages" {
			var skip bool
			text, truncated, incomplete, skip = anthropicUserContent(messages, event.Purpose, event.Snapshot.MaxTextBytes)
			if skip {
				return
			}
		} else {
			// 此精确辅助指令只适用于未知用途 Chat；不能放进多协议共享的 chatContent。
			if event.Protocol == "chat-completions" && event.Purpose == PurposeUnknown {
				if value, ok := clientMessageText(last["content"]); ok && value == "follow system prompt" {
					return
				}
			}
			text, truncated, incomplete = chatContent(last["content"], event.Snapshot.MaxTextBytes)
		}
	}
	if incomplete {
		// 一次提交只计一次提取缺口；仍允许安全部分入队，不把未知内容归为空输入。
		c.gap("unsupported_content")
	}
	// 上限可能容不下首个UTF-8字符或占位符；空的截断前缀仍是非空提交，必须显示截断状态。
	if text == "" && !truncated {
		return
	}
	// 路由前的缺失/不可靠模型不妨碍记录新输入；空值由管理层显示“未知”，不从其他字段猜测。
	var model string
	if rawModel := request["model"]; len(rawModel) > 0 && rawModel[0] == '"' {
		if json.Unmarshal(rawModel, &model) != nil {
			model = ""
		}
	}
	if len(model) > 1024 {
		c.gap("metadata_too_large")
		return
	}
	reasoning, err := requestReasoning(event.Protocol, request)
	if err != nil {
		c.gap(err.Error())
		return
	}
	// Clone 切断与解析出的巨大字符串/原正文的关系，队列只保留受限白名单副本。
	record := Record{ID: strings.Clone(event.ID), ObjectID: strings.Clone(event.ObjectID), ReceivedAt: strings.Clone(event.ReceivedAt), Protocol: strings.Clone(event.Protocol), Purpose: event.Purpose, Model: strings.Clone(model), RequestReasoning: reasoning, Text: strings.Clone(text), Truncated: truncated, ExtractionIncomplete: incomplete}
	size := int64(len(record.ID) + len(record.ObjectID) + len(record.ReceivedAt) + len(record.Protocol) + len(record.Purpose) + len(record.Model) + len(record.Text) + len(reasoning.Effort) + len(reasoning.Type) + queuedRecordOverhead)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.status.State != "active" {
		reason := "storage_error"
		if c.status.Reason == "budget_exhausted" {
			reason = "budget_exhausted"
		}
		c.incrementGap(reason)
		return
	}
	if c.queuedBytes+size > maxQueuedBytes {
		c.incrementGap("queue_full")
		return
	}
	select {
	case c.queue <- queuedRecord{record, size}:
		c.queuedBytes += size
		c.status.Queued++
	default:
		c.incrementGap("queue_full")
	}
}

// Status 返回独立运行快照；不执行磁盘 I/O，无参数及异常，计数只代表已 fsync 或当前排队状态。
func (c *Collector) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	alerts := c.gapAlerts()
	result := c.status
	result.GapAlerts = alerts
	result.CapturePaused = c.config.CapturePaused
	result.MaxTextBytes = c.config.MaxTextBytes
	result.RetentionDays = c.config.RetentionDays
	result.ReservedBytes = auxiliaryReserve + maintenanceReserve
	result.ResumeRequiredBytes = resumeRequiredBytes
	result.OverBudgetBytes = max(0, result.UsedBytes-result.BudgetBytes)
	if result.CapacityGap && result.Reason != "budget_exhausted" {
		result.CapacityGap = !c.expired(Record{ReceivedAt: c.capacityLastAt}, auditNow())
	}
	result.GapHistory = make(map[string]string, len(c.status.GapHistory))
	for key, last := range c.status.GapHistory {
		if !c.expired(Record{ReceivedAt: last}, auditNow()) || result.Reason == key {
			result.GapHistory[key] = last
		}
	}
	if c.sessionStarted && result.Recovery.Uncertain && c.expired(Record{ReceivedAt: result.Recovery.DetectedAt}, auditNow()) {
		result.Recovery = Recovery{}
	}
	result.Gaps = make(map[string]uint64, len(c.status.Gaps))
	for key, value := range c.status.Gaps {
		result.Gaps[key] = value
	}
	return result
}

// Close 停止新入队并等待已有队列处理，释放目录和进程锁；可并发重复调用，无返回值。
// I/O 失败以状态原因码保留，不抛出宿主可见异常。
func (c *Collector) Close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		close(c.queue)
		c.mu.Unlock()
		<-c.done
		c.ioMu.Lock()
		if c.diagnostics != nil {
			c.mu.Lock()
			// 存储同步曾失败时不宣称运行完整结束；正常容量拒收已有摘要，不属于I/O失败。
			if c.sessionStarted && (c.status.Reason == "" || c.status.Reason == "budget_exhausted") {
				c.sessionDirty = false
			}
			c.mu.Unlock()
			if err := c.saveDiagnostics(); err != nil {
				c.pauseStorage(err)
			}
		}
		c.releaseFiles()
		c.ioMu.Unlock()
		c.mu.Lock()
		if c.status.State == "active" {
			c.status.State = "closed"
		}
		c.status.Available = false
		c.mu.Unlock()
	})
}

func (c *Collector) gap(key string) { c.mu.Lock(); c.incrementGap(key); c.mu.Unlock() }
func (c *Collector) incrementGap(key string) {
	// 饱和计数避免长期运行后溢出；键只能由内部固定原因码提供。
	if c.status.Gaps[key] != ^uint64(0) {
		c.status.Gaps[key]++
	}
	c.recordGap(key, 1, true, auditNow())
	c.markGapHistory(key)
}

// markGapHistory 更新兼容的基础摘要；新提醒和持久化通知统一由recordGap管理。
func (c *Collector) markGapHistory(key string) {
	last := auditNow().UTC().Format(time.RFC3339Nano)
	c.status.GapHistory[key] = last
	if key == "budget_exhausted" {
		c.status.CapacityGap = true
		c.capacityLastAt = last
	}
}

// ReportBridgeFailures 合并宿主本次运行的累计桥接缺口；total不含身份或输入，重复查询不重复计数。
// 无返回值或异常；新增缺口仅异步保存，宿主重置累计时保留既有历史。
func (c *Collector) ReportBridgeFailures(total uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if total <= c.bridgeFailures {
		return
	}
	delta := total - c.bridgeFailures
	c.bridgeFailures = total
	old := c.status.Gaps["observer_unavailable"]
	if delta > ^uint64(0)-old {
		c.status.Gaps["observer_unavailable"] = ^uint64(0)
	} else {
		c.status.Gaps["observer_unavailable"] = old + delta
	}
	c.recordGap("observer_unavailable", delta, true, auditNow())
	c.markGapHistory("observer_unavailable")
}

// ReportBridgeUncertainty 在关闭前无法读取宿主最终累计时记录未知损失；不把未知伪造成一次确定丢失。
// 无参数、返回值或异常，不执行I/O；Close将尽力保存摘要，数量和损失时段保持未知。
func (c *Collector) ReportBridgeUncertainty() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Recovery = Recovery{Uncertain: true, DetectedAt: auditNow().UTC().Format(time.RFC3339Nano), Reason: "observer_unavailable"}
	c.recordGap("observer_unavailable", 0, false, auditNow())
	c.markGapHistory("observer_unavailable")
}

func (c *Collector) pause(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 容量配置失败不能覆盖尚未核实的I/O或完整性故障，否则调高预算会错误地恢复采集。
	if reason == "budget_exhausted" && c.status.State == "paused" && c.status.Reason != "budget_exhausted" {
		return
	}
	if c.status.State != "paused" || c.status.Reason != reason {
		if old := c.status.Reason; old != "" && old != c.observerReason {
			entry := c.gapEntries[old]
			entry.Flags &^= gapActive
			c.gapEntries[old] = entry
		}
		c.beginFault(reason)
		c.markGapHistory(reason)
	}
	entry := c.gapEntries[reason]
	entry.Flags |= gapActive
	c.gapEntries[reason] = entry
	c.status.State = "paused"
	c.status.Reason = reason
}
func protocolObject(raw []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid_json")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid_json")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errors.New("invalid_json")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errors.New("ambiguous_json")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, errors.New("invalid_json")
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid_json")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, errors.New("invalid_json")
	}
	return fields, nil
}

// supportedProtocol 同时限定入站与持久化读取，避免新增协议能采集却在重启时被判为损坏。
func supportedProtocol(protocol string) bool {
	return protocol == "chat-completions" || protocol == "responses" || protocol == "anthropic-messages" || protocol == "responses-websocket"
}

func validObjectID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func validEvent(e Event) bool {
	if len(e.ID) == 0 || len(e.ID) > 128 || !validObjectID(e.ObjectID) || len(e.ReceivedAt) > 64 || len(e.Protocol) > 64 {
		return false
	}
	for _, r := range e.ID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	_, err := time.Parse(time.RFC3339Nano, e.ReceivedAt)
	return err == nil
}
