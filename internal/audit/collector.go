// 本文件按接收快照提取末尾允许内容并有界异步采集；原请求正文只在 Observe 返回前借用。
package audit

import (
	"bytes"
	"crypto/rand"
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
	// 64 位平台的六个字符串头、bool 对齐和队列字节计数共约112字节，向上预留到128。
	queuedRecordOverhead = 128
	// 配置最多 1024 个对象、单份不足 512 KiB；预留两份原子替换峰值及目录/锁块空间。
	auxiliaryReserve int64 = 1 << 20
)

// Event 是宿主同步借用的只读入站事件；Body 不得在 Observe 返回后继续引用。
// 字段仅含无秘密身份、提交元数据及原协议正文，ReadError 表示宿主读取失败。
type Event struct {
	ID         string   `json:"id"`
	ObjectID   string   `json:"object_id"`
	ReceivedAt string   `json:"received_at"`
	Protocol   string   `json:"protocol"`
	Body       []byte   `json:"body"`
	ReadError  bool     `json:"read_error"`
	Snapshot   Snapshot `json:"snapshot"`
}

// Record 是唯一允许持久化及查询的输入白名单；名称由管理层从已知对象目录关联。
type Record struct {
	ID                   string `json:"id"`
	ObjectID             string `json:"object_id"`
	ReceivedAt           string `json:"received_at"`
	Protocol             string `json:"protocol"`
	Model                string `json:"model"`
	Text                 string `json:"text"`
	Truncated            bool   `json:"truncated"`
	ExtractionIncomplete bool   `json:"extraction_incomplete,omitempty"`
}

// Status 是有界运行状态快照；Gaps 仅有固定原因码，不携带输入或底层错误上下文。
type Status struct {
	CapturePaused bool              `json:"capture_paused"`
	Available     bool              `json:"available"`
	State         string            `json:"state"`
	Reason        string            `json:"reason"`
	Saved         int64             `json:"saved"`
	Queued        int64             `json:"queued"`
	Gaps          map[string]uint64 `json:"gaps"`
	UsedBytes     int64             `json:"used_bytes"`
	BudgetBytes   int64             `json:"budget_bytes"`
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
	config      configuration
	configValid bool
	generation  string
	segments    []segment
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
// budget 为总目录预算，0 使用 1 GiB，合法范围 4 MiB 至 1 GiB。
// 返回可关闭的采集器；参数、随机代际来源、目录或锁不可用返回固定错误，损坏数据则返回停采实例。
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
	c := &Collector{root: root, generation: hex.EncodeToString(generation[:]), config: configuration{Version: 2, Objects: make(map[string]Object)}, queue: make(chan queuedRecord, 64), done: make(chan struct{}), status: Status{Available: true, State: "active", BudgetBytes: budget, Gaps: map[string]uint64{}}}
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

// Observe 按 event 的接收快照同步提取 Chat/Responses 本次允许输入并非阻塞入队，不复核当前名单或主动暂停。
// 无返回值，解析及容量失败仅增加固定缺口诊断，不影响宿主请求；Body 不进入采集器。
func (c *Collector) Observe(event Event) {
	c.mu.Lock()
	accepting := !c.closed && c.status.State == "active"
	generation := c.generation
	c.mu.Unlock()
	if event.Snapshot.Version != 2 || event.Snapshot.CollectorGeneration != generation {
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
	if !accepting {
		c.gap("storage_error")
		return
	}
	if event.ReadError {
		c.gap("read_error")
		return
	}
	if !validEvent(event) {
		c.gap("invalid_event")
		return
	}
	if event.Protocol != "chat-completions" && event.Protocol != "responses" {
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
	if event.Protocol == "responses" {
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
		text, truncated, incomplete = chatContent(last["content"], event.Snapshot.MaxTextBytes)
	}
	if incomplete {
		// 一次提交只计一次提取缺口；仍允许安全部分入队，不把未知内容归为空输入。
		c.gap("unsupported_content")
	}
	if text == "" {
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
	// Clone 切断与解析出的巨大字符串/原正文的关系，队列只保留受限白名单副本。
	record := Record{ID: strings.Clone(event.ID), ObjectID: strings.Clone(event.ObjectID), ReceivedAt: strings.Clone(event.ReceivedAt), Protocol: strings.Clone(event.Protocol), Model: strings.Clone(model), Text: strings.Clone(text), Truncated: truncated, ExtractionIncomplete: incomplete}
	size := int64(len(record.ID) + len(record.ObjectID) + len(record.ReceivedAt) + len(record.Protocol) + len(record.Model) + len(record.Text) + queuedRecordOverhead)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.status.State != "active" {
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
	result := c.status
	result.CapturePaused = c.config.CapturePaused
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
}
func (c *Collector) pause(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.State = "paused"
	c.status.Reason = reason
	if _, exists := c.status.Gaps[reason]; exists {
		c.incrementGap(reason)
	}
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
