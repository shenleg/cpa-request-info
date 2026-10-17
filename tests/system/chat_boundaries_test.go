// 本文件通过真实 Chat HTTP、管理查询和可控上游验证内容边界；不测试私有提取器。
package system

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestChatTerminalMessage 验证连续 user 只取最后一条，已知非用户末尾均正常跳过且不回找历史。
func TestChatTerminalMessage(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	for _, role := range []string{"assistant", "tool", "function", "system", "developer"} {
		body := fmt.Sprintf(`{"model":"audit-test-model","messages":[{"role":"user","content":"FORBIDDEN_TERMINAL_HISTORY"},{"role":%q,"content":"FORBIDDEN_TERMINAL_CONTENT","tool_calls":[{"function":{"arguments":"FORBIDDEN_TERMINAL_TOOL"}}]}]}`, role)
		h.request(t, "POST", "/v1/chat/completions", keyA, []byte(body))
	}
	body := []byte(`{"model":"audit-test-model","messages":[{"role":"user","content":"FORBIDDEN_CONSECUTIVE_USER"},{"role":"user","content":"CHAT-LAST-USER-ONLY"}]}`)
	h.request(t, "POST", "/v1/chat/completions", keyA, body)
	waitSnapshotRecord(t, h, "CHAT-LAST-USER-ONLY")
	saved, queued, after := snapshotCounters(t, h)
	if saved != 1 || queued != 0 || !maps.Equal(before, after) {
		t.Fatal("末尾非用户消息被记录或误报为解析缺口")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_TERMINAL_HISTORY", "FORBIDDEN_TERMINAL_CONTENT", "FORBIDDEN_TERMINAL_TOOL", "FORBIDDEN_CONSECUTIVE_USER")
}

// assertChatPrivacy 检查合成实例全部审计持久化输出和宿主/插件诊断；markers 是本轮禁存夹具。
// 无返回值，读取或禁存内容检查失败终止测试；不打印输入、认证材料或日志原文。
func assertChatPrivacy(t *testing.T, h *instance, markers ...string) {
	t.Helper()
	forbidden := append([]string{keyA, keyB, adminKey, "synthetic-upstream-only"}, markers...)
	// check 只按 markers 检查禁存字节；raw 为合成输出，失败终止测试，无返回值。
	check := func(raw []byte) {
		t.Helper()
		for _, marker := range forbidden {
			if bytes.Contains(raw, []byte(marker)) {
				t.Fatal("审计持久化输出或诊断含禁存内容（不输出原文）")
			}
		}
	}
	if err := filepath.WalkDir(h.stateDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err == nil {
			check(raw)
			// 检查对外记录白名单，不依赖文件名、表名或私有存储查询。
			for _, line := range bytes.Split(raw, []byte{'\n'}) {
				var fields map[string]json.RawMessage
				if json.Unmarshal(line, &fields) != nil || fields["text"] == nil {
					continue
				}
				for key := range fields {
					switch key {
					case "id", "object_id", "received_at", "protocol", "purpose", "model", "text", "truncated", "extraction_incomplete":
					default:
						t.Fatal("持久化输入记录含白名单之外的字段")
					}
				}
			}
		}
		return err
	}); err != nil {
		t.Fatal("审计持久化输出检查不可用")
	}
	raw, err := os.ReadFile(filepath.Join(h.dir, "host.log"))
	if err != nil {
		t.Fatal("合成诊断输出检查不可用")
	}
	check(raw)
}

// TestChatOrderedContent 验证同一消息文本和非文本占位符按序组合，工具嵌套内容不提取，普通文本不脱敏。
func TestChatOrderedContent(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	_, _, before := snapshotCounters(t, h)
	cases := []struct {
		content string
		want    string
	}{
		{`[{"type":"text","text":"CHAT-MIXED-前"},{"type":"image_url","image_url":{"url":"https://invalid.example/FORBIDDEN_IMAGE_URL"}},{"type":"input_audio","input_audio":{"data":"FORBIDDEN_AUDIO_BASE64","format":"wav"}},{"type":"video_url","video_url":{"url":"https://invalid.example/FORBIDDEN_VIDEO_URL"}},{"type":"file","file":{"filename":"FORBIDDEN_FILENAME","file_data":"FORBIDDEN_FILE_DATA","file_id":"FORBIDDEN_FILE_ID"}},{"type":"text","text":"后"}]`, "CHAT-MIXED-前[图片][音频][视频][文件]后"},
		{`[{"type":"image_url","image_url":{"url":"data:image/png;base64,FORBIDDEN_IMAGE_BASE64"}}]`, "[图片]"},
		{`[{"type":"text","text":"CHAT-TOOLS-前"},{"type":"tool_result","content":[{"type":"text","text":"FORBIDDEN_NESTED_TOOL_TEXT"},{"type":"image_url","image_url":{"url":"FORBIDDEN_NESTED_TOOL_IMAGE"}}]},{"type":"tool_call","arguments":"FORBIDDEN_TOOL_ARGUMENTS"},{"type":"tool_use","input":{"text":"FORBIDDEN_TOOL_INPUT"}},{"type":"text","text":"后"}]`, "CHAT-TOOLS-前后"},
		{`[{"type":"text","text":"CHAT-LITERAL-https://example.invalid/picture.png data:image/png;base64,U1lOVEhFVElD synthetic-text-secret"}]`, "CHAT-LITERAL-https://example.invalid/picture.png data:image/png;base64,U1lOVEhFVElD synthetic-text-secret"},
	}
	for _, tc := range cases {
		body := []byte(`{"model":"audit-test-model","messages":[{"role":"system","content":"FORBIDDEN_MIXED_SYSTEM"},{"role":"developer","content":"FORBIDDEN_MIXED_DEVELOPER"},{"role":"user","content":"FORBIDDEN_MIXED_HISTORY"},{"role":"assistant","content":"FORBIDDEN_MIXED_REPLY"},{"role":"user","tool_calls":[{"arguments":"FORBIDDEN_MESSAGE_TOOL"}],"content":` + tc.content + `}]}`)
		code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
		if code != 200 {
			t.Fatal("审计不应改变合成模型调用")
		}
		waitSnapshotRecord(t, h, tc.want)
	}
	_, _, after := snapshotCounters(t, h)
	if !maps.Equal(before, after) {
		t.Fatal("已识别混合内容误报缺口")
	}
	assertChatPrivacy(t, h, "FORBIDDEN_IMAGE_URL", "FORBIDDEN_AUDIO_BASE64", "FORBIDDEN_VIDEO_URL", "FORBIDDEN_FILENAME", "FORBIDDEN_FILE_DATA", "FORBIDDEN_FILE_ID", "FORBIDDEN_IMAGE_BASE64", "FORBIDDEN_NESTED_TOOL_TEXT", "FORBIDDEN_NESTED_TOOL_IMAGE", "FORBIDDEN_TOOL_ARGUMENTS", "FORBIDDEN_TOOL_INPUT", "FORBIDDEN_MIXED_SYSTEM", "FORBIDDEN_MIXED_DEVELOPER", "FORBIDDEN_MIXED_HISTORY", "FORBIDDEN_MIXED_REPLY", "FORBIDDEN_MESSAGE_TOOL")
}

// waitChatDetail 等待本轮 prefix 对应的已保存记录并核对列表/详情标记；返回详情。
// prefix 必须唯一且在预览内；HTTP、状态不一致或等待超时终止测试，不打印正文。
func waitChatDetail(t *testing.T, h *instance, prefix string) savedRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, raw := h.search(t, adminKey, map[string]any{"limit": 100})
		var list struct {
			Records []struct {
				savedRecord
				Preview string `json:"preview"`
			} `json:"records"`
		}
		if code != 200 || json.Unmarshal(raw, &list) != nil {
			t.Fatal("内容边界列表不可用")
		}
		for _, item := range list.Records {
			if strings.HasPrefix(item.Preview, prefix) {
				code, raw = h.request(t, "GET", api+"/detail?id="+item.ID, adminKey, nil)
				var detail savedRecord
				if code != 200 || json.Unmarshal(raw, &detail) != nil || detail.ID != item.ID || detail.Truncated != item.Truncated || detail.ExtractionIncomplete != item.ExtractionIncomplete {
					t.Fatal("本轮详情和列表的提取状态不一致")
				}
				return detail
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("本轮内容边界记录未保存")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestChatUnknownContent 验证安全相邻块可形成不完整记录，完全未知/畸形只报缺口，真正空内容不报缺口。
func TestChatUnknownContent(t *testing.T) {
	h := startHost(t, 1<<30, true, "")
	h.monitorAll(t)
	cases := []struct {
		name, content, want string
		gap                 bool
	}{
		{"partial", `[{"type":"text","text":"CHAT-PARTIAL-前"},{"type":"unknown","payload":"FORBIDDEN_UNKNOWN_PAYLOAD"},{"type":"text","text":"后"}]`, "CHAT-PARTIAL-前后", true},
		{"ambiguous_type", `[{"type":"text","text":"CHAT-AMBIGUOUS-TYPE"},{"type":"tool_result","type":"text","text":"FORBIDDEN_AMBIGUOUS_TYPE"}]`, "CHAT-AMBIGUOUS-TYPE", true},
		{"ambiguous_text", `[{"type":"text","text":"FORBIDDEN_DUPLICATE_TEXT","text":"FORBIDDEN_DUPLICATE_TEXT_2"},{"type":"text","text":"CHAT-AMBIGUOUS-TEXT"}]`, "CHAT-AMBIGUOUS-TEXT", true},
		{"unknown_only", `[{"type":"future","text":"FORBIDDEN_UNKNOWN_TEXT"}]`, "", true},
		{"missing", "", "", true},
		{"wrong_shape", `{"text":"FORBIDDEN_OBJECT_TEXT"}`, "", true},
		{"null_block", `[null]`, "", true},
		{"string_block", `["FORBIDDEN_STRING_BLOCK"]`, "", true},
		{"missing_type", `[{"text":"FORBIDDEN_MISSING_TYPE"}]`, "", true},
		{"type_alias", `[{"Type":"text","text":"FORBIDDEN_TYPE_ALIAS"}]`, "", true},
		{"text_alias", `[{"type":"text","Text":"FORBIDDEN_TEXT_ALIAS"}]`, "", true},
		{"null_text", `[{"type":"text","text":null}]`, "", true},
		{"object_text", `[{"type":"text","text":{"payload":"FORBIDDEN_OBJECT_TEXT"}}]`, "", true},
		{"malformed_image", `[{"type":"image_url","image_url":null}]`, "", true},
		{"wrong_audio", `[{"type":"input_audio","input_audio":"FORBIDDEN_WRONG_AUDIO"}]`, "", true},
		{"empty_string", `""`, "", false},
		{"empty_null", `null`, "", false},
		{"empty_array", `[]`, "", false},
		{"empty_text", `[{"type":"text","text":""}]`, "", false},
		{"tool_only", `[{"type":"tool_result","content":[{"type":"text","text":"FORBIDDEN_TOOL_ONLY"}]}]`, "", false},
		{"whitespace", `[{"type":"text","text":" \n\t "}]`, " \n\t ", false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			savedBefore, _, gapsBefore := snapshotCounters(t, h)
			field := ""
			if tc.content != "" {
				field = `,"content":` + tc.content
			}
			body := []byte(`{"model":"audit-test-model","messages":[{"role":"user"` + field + `}]}`)
			code, _ := h.request(t, "POST", "/v1/chat/completions", keyA, body)
			if code != 200 {
				t.Fatal("审计改变了合成模型响应")
			}
			// 后续唯一提交的实际落盘作为 FIFO 屏障，不能用固定 sleep 判断之前没有记录。
			barrier := fmt.Sprintf("CHAT-UNKNOWN-BARRIER-%d", i)
			h.request(t, "POST", "/v1/chat/completions", keyA, []byte(fmt.Sprintf(`{"model":"audit-test-model","messages":[{"role":"user","content":%q}]}`, barrier)))
			waitSnapshotRecord(t, h, barrier)
			saved, queued, gaps := snapshotCounters(t, h)
			wantSaved := savedBefore + 1
			if tc.want != "" {
				wantSaved++
				r := waitChatDetail(t, h, tc.want)
				if r.Text != tc.want || r.ExtractionIncomplete != tc.gap || r.Truncated {
					t.Fatal("安全部分、空白或提取状态不符合规范")
				}
			}
			wantGaps := maps.Clone(gapsBefore)
			if tc.gap {
				wantGaps["unsupported_content"]++
			}
			if saved != wantSaved || queued != 0 || !maps.Equal(gaps, wantGaps) {
				t.Fatal("空输入和解析缺口混淆，或同一提交重复计缺口")
			}
		})
	}
	assertChatPrivacy(t, h, "FORBIDDEN_UNKNOWN_PAYLOAD", "FORBIDDEN_AMBIGUOUS_TYPE", "FORBIDDEN_DUPLICATE_TEXT", "FORBIDDEN_UNKNOWN_TEXT", "FORBIDDEN_OBJECT_TEXT", "FORBIDDEN_STRING_BLOCK", "FORBIDDEN_MISSING_TYPE", "FORBIDDEN_TYPE_ALIAS", "FORBIDDEN_TEXT_ALIAS", "FORBIDDEN_WRONG_AUDIO", "FORBIDDEN_TOOL_ONLY")
	oldID := waitChatDetail(t, h, "CHAT-UNKNOWN-BARRIER-0").ID
	partialID := waitChatDetail(t, h, "CHAT-PARTIAL-前后").ID
	h.stop()
	restarted := startHost(t, 1<<30, true, "", filepath.Join(h.dir, "audit"))
	for id, incomplete := range map[string]bool{oldID: false, partialID: true} {
		code, raw := restarted.request(t, "GET", api+"/detail?id="+id, adminKey, nil)
		var record savedRecord
		if code != 200 || json.Unmarshal(raw, &record) != nil || record.ExtractionIncomplete != incomplete {
			t.Fatal("重启没有保留历史记录的提取状态")
		}
	}
}

// TestChatUTF8PrefixAndFailOpen 验证跨块 UTF-8 前缀与双状态，审计开启/关闭时实际上游字节和宿主响应一致。
func TestChatUTF8PrefixAndFailOpen(t *testing.T) {
	received := make(chan []byte, 32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "synthetic read failure", 400)
			return
		}
		received <- raw
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-chat-boundary","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"FORBIDDEN_CHAT_UPSTREAM_REPLY"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	off := startHost(t, 1<<30, false, upstream.URL+"/v1")
	h := startHost(t, 1<<30, true, upstream.URL+"/v1")
	h.monitorAll(t)
	image := map[string]any{"type": "image_url", "image_url": map[string]string{"url": "FORBIDDEN_LIMIT_IMAGE"}}
	unknown := map[string]string{"type": "future", "payload": "FORBIDDEN_LIMIT_UNKNOWN"}
	// 上限为65536字节；[图片]含两个ASCII括号与两个三字节汉字，共8字节。
	// 65528留8字节恰好容纳占位符；65529留7字节只保留“[图片”，不能丢掉合法前缀。
	// 65535只剩1字节，汉字(3字节)/emoji(4字节)均不应切开或跳过后继续塞短块。
	// 65532留4字节：恰好容纳emoji，或仅容纳“中文”中的“中”；65536测试未知尾部不冒充容量截断。
	cases := []struct {
		name                  string
		asciiBytes            int
		blocks                []any
		wantSuffix            string
		truncated, incomplete bool
	}{
		{"exact_image", 65528, []any{image}, "[图片]", false, false},
		{"partial_placeholder", 65529, []any{image}, "[图片", true, false},
		{"split_chinese", 65535, []any{map[string]string{"type": "text", "text": "中FORBIDDEN_CHINESE_TAIL"}, map[string]string{"type": "text", "text": "FORBIDDEN_LATER_SHORT_BLOCK"}}, "", true, false},
		{"split_emoji_and_unknown", 65535, []any{map[string]string{"type": "text", "text": "🙂FORBIDDEN_EMOJI_TAIL"}, unknown, map[string]string{"type": "text", "text": "z"}}, "", true, true},
		{"exact_emoji", 65532, []any{map[string]string{"type": "text", "text": "🙂"}}, "🙂", false, false},
		{"split_second_chinese", 65532, []any{map[string]string{"type": "text", "text": "中文"}}, "中", true, false},
		{"unknown_not_capacity", 65536, []any{unknown, map[string]string{"type": "tool_result", "content": "FORBIDDEN_LIMIT_TOOL"}, map[string]string{"type": "text", "text": ""}}, "", false, true},
	}
	var dualID string
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 期望由明确的字节边界与字面后缀给出，不调用提取器或重算其截断算法。
			marker := fmt.Sprintf("CHAT-UTF8-%d-", i)
			ascii := marker + strings.Repeat("a", tc.asciiBytes-len(marker))
			blocks := append([]any{map[string]string{"type": "text", "text": ascii}}, tc.blocks...)
			body, err := json.Marshal(map[string]any{"model": "audit-test-model", "user": "FORBIDDEN_CLIENT_ID", "metadata": map[string]string{"session_id": "FORBIDDEN_SESSION_ID"}, "messages": []any{map[string]string{"role": "system", "content": "FORBIDDEN_LIMIT_SYSTEM"}, map[string]string{"role": "user", "content": "FORBIDDEN_LIMIT_HISTORY"}, map[string]any{"role": "user", "content": blocks}}})
			if err != nil {
				t.Fatal("容量夹具编码失败")
			}
			var plain []byte
			for _, host := range []*instance{off, h} {
				code, _ := host.request(t, "POST", "/v1/chat/completions", keyA, body)
				if code != 200 {
					t.Fatal("审计不应拒绝或修改可控上游调用")
				}
				select {
				case actual := <-received:
					if plain == nil {
						plain = actual
					} else if !bytes.Equal(plain, actual) {
						t.Fatal("审计改变了实际上游请求字节")
					}
				case <-time.After(3 * time.Second):
					t.Fatal("可控上游没有收到本轮提交")
				}
			}
			record := waitChatDetail(t, h, marker)
			if record.Text != ascii+tc.wantSuffix || record.Truncated != tc.truncated || record.ExtractionIncomplete != tc.incomplete {
				t.Fatal("跨块前缀、占位符字节合计或双状态不符合规范")
			}
			if tc.truncated && tc.incomplete {
				dualID = record.ID
			}
		})
	}
	for _, body := range []string{
		`{"model":"audit-test-model","messages":[{"role":"user","content":[{"type":"future","payload":"FORBIDDEN_ALL_UNKNOWN_UPSTREAM"}]}]}`,
		`{"model":"audit-test-model","messages":[`,
	} {
		plainCode, plainResponse := off.request(t, "POST", "/v1/chat/completions", keyA, []byte(body))
		code, response := h.request(t, "POST", "/v1/chat/completions", keyA, []byte(body))
		if code != plainCode || !bytes.Equal(response, plainResponse) {
			t.Fatal("未知块或解析失败改变了原宿主响应")
		}
		if code == 200 {
			plain := <-received
			actual := <-received
			if !bytes.Equal(plain, actual) {
				t.Fatal("解析缺口改变了实际转发请求")
			}
		}
	}
	// 在查询层只可得到实际保存前缀；队列排空屏障后检查全部输出，排除未保存尾部/元数据。
	h.request(t, "POST", "/v1/chat/completions", keyA, snapshotInput(t, "CHAT-UTF8-DRAIN"))
	waitSnapshotRecord(t, h, "CHAT-UTF8-DRAIN")
	assertChatPrivacy(t, h, "FORBIDDEN_LIMIT_IMAGE", "FORBIDDEN_LIMIT_UNKNOWN", "FORBIDDEN_CHINESE_TAIL", "FORBIDDEN_LATER_SHORT_BLOCK", "FORBIDDEN_EMOJI_TAIL", "FORBIDDEN_LIMIT_TOOL", "FORBIDDEN_LIMIT_SYSTEM", "FORBIDDEN_LIMIT_HISTORY", "FORBIDDEN_CHAT_UPSTREAM_REPLY", "FORBIDDEN_CLIENT_ID", "FORBIDDEN_SESSION_ID", "FORBIDDEN_ALL_UNKNOWN_UPSTREAM")
	h.stop()
	restarted := startHost(t, 1<<30, true, "", filepath.Join(h.dir, "audit"))
	code, raw := restarted.request(t, "GET", api+"/detail?id="+dualID, adminKey, nil)
	var record savedRecord
	if dualID == "" || code != 200 || json.Unmarshal(raw, &record) != nil || !record.Truncated || !record.ExtractionIncomplete {
		t.Fatal("正常重启后截断和不完整标记没有同时保留")
	}
}
