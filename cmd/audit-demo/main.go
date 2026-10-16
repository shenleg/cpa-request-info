// 本命令用合成凭据启动隔离宿主与可控上游，用于真实浏览器验收；停止进程但不删除验收目录。
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	jsonOutput := flag.Bool("json", false, "健康检查通过后输出机器可读 ready 事件")
	restartHost := flag.Bool("restart-host", false, "仅供故障验收：宿主异常退出后使用同目录重启")
	flag.Parse()
	if err := run(*jsonOutput, *restartHost); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run 启动隔离实例并等待停止；restartHost 只授权本测试命令在宿主异常退出后重用目录，返回启动或回收错误。
func run(jsonOutput, restartHost bool) (result error) {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(root, ".test-data"), 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".test-data"), "browser-")
	if err != nil {
		return err
	}
	panel, err := os.ReadFile(filepath.Join(root, ".build/assets/management-v1.26.2/management.html"))
	if err != nil {
		return fmt.Errorf("固定管理页面尚未取得")
	}
	sum := sha256.Sum256(panel)
	if hex.EncodeToString(sum[:]) != "808cfd05ce5766a0f3854f8e095e5c5ea6e4313399f2df0d52b7a40352be29df" {
		return fmt.Errorf("管理页面校验失败")
	}
	static := filepath.Join(dir, "static")
	if err = os.Mkdir(static, 0700); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(static, "management.html"), panel, 0600); err != nil {
		return err
	}
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	upstream := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "synthetic read failure", 400)
			return
		}
		// 同一合成上游支持 HTTP/SSE，也供宿主完成 WebSocket 不采集负例的真实模型执行。
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"synthetic-reply\",\"created\":1,\"object\":\"chat.completion.chunk\",\"model\":\"audit-test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"合成上游回复\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"synthetic-reply\",\"created\":1,\"object\":\"chat.completion.chunk\",\"model\":\"audit-test-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"synthetic-reply","object":"chat.completion","model":"audit-test-model","choices":[{"index":0,"message":{"role":"assistant","content":"合成上游回复"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	})}
	go func() { _ = upstream.Serve(up) }()
	defer upstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		return err
	}
	config := fmt.Sprintf(`config-version: 8
server:
  host: 127.0.0.1
  port: %d
management:
  secret-key: "synthetic-admin-audit-test-only"
  disable-control-panel: false
  disable-auto-update-panel: true
access:
  api-keys: ["synthetic-client-audit-a-only", "synthetic-client-audit-b-only"]
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
      budget-bytes: 1073741824
openai-compatibility:
  - name: controlled
    base-url: %q
    api-key-entries:
      - api-key: synthetic-upstream-only
    models:
      - name: audit-test-model
        alias: audit-test-model
      - name: audit-test-other-model
        alias: audit-test-other-model
`, port, filepath.Join(dir, "auths"), filepath.Join(root, ".build/plugins"), filepath.Join(dir, "audit-http-execution"), "http://"+up.Addr().String()+"/v1")
	configPath := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(configPath, []byte(config), 0600); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "host.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	// 信号只注册一次；重启只属于合成测试编排，不进入插件或生产管理 API。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	discovered := false
	for {
		cmd := exec.Command(filepath.Join(root, ".build/bin/cliproxyapi"), "--config", configPath, "--local-model")
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir, "MANAGEMENT_STATIC_PATH=" + static}
		cmd.Stdout = log
		cmd.Stderr = log
		if err = cmd.Start(); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		ended := false
		defer func() {
			if ended {
				return
			}
			_ = cmd.Process.Signal(os.Interrupt)
			// 五秒是本地验收停止期限，避免失效宿主让 runner 无限等候；仅终止本轮子进程。
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case exitErr := <-done:
				if exitErr != nil && result == nil {
					result = fmt.Errorf("宿主停止失败")
				}
			case <-timer.C:
				_ = cmd.Process.Kill()
				<-done
				if result == nil {
					result = fmt.Errorf("宿主未在停止期限内退出")
				}
			}
		}()
		base := fmt.Sprintf("http://127.0.0.1:%d", port)
		client := &http.Client{Timeout: time.Second}
		defer client.CloseIdleConnections()
		deadline := time.NewTimer(20 * time.Second) // 本地启动验收上限，不是上游模型超时。
		defer deadline.Stop()
		poll := time.NewTicker(50 * time.Millisecond)
		defer poll.Stop()
		ready := false
		for !ready {
			select {
			case <-stop:
				return nil
			case <-done:
				ended = true
				return fmt.Errorf("宿主在就绪前退出")
			case <-deadline.C:
				return fmt.Errorf("宿主就绪超时")
			case <-poll.C:
				req, errRequest := http.NewRequest(http.MethodGet, base+"/v0/management/plugins/request-input-audit/status", nil)
				if errRequest != nil {
					return fmt.Errorf("无法构造就绪检查")
				}
				req.Header.Set("Authorization", "Bearer synthetic-admin-audit-test-only")
				resp, errGet := client.Do(req)
				if errGet == nil {
					var state struct {
						Available          bool     `json:"available"`
						CaptureMode        string   `json:"capture_mode"`
						SupportedProtocols []string `json:"supported_protocols"`
						ResponsesWebSocket bool     `json:"responses_websocket"`
					}
					errDecode := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&state)
					errClose := resp.Body.Close()
					ready = resp.StatusCode == http.StatusOK && errDecode == nil && errClose == nil && state.CaptureMode == "http_execution" && len(state.SupportedProtocols) == 3 && !state.ResponsesWebSocket && (state.Available || restartHost)
				}
			}
		}
		// 故障实例可能多次重启；每次 ready 后停止本轮探测，不在等待浏览器操作时继续计时。
		deadline.Stop()
		poll.Stop()
		// 只在首次启动发现未监控对象，不命名、不启用；重启不能重采发现正文或覆盖浏览器配置。
		if !discovered {
			for _, key := range []string{"synthetic-client-audit-a-only", "synthetic-client-audit-b-only"} {
				req, errRequest := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", bytes.NewBufferString(`{"model":"audit-test-model","messages":[{"role":"user","content":"synthetic HTTP discovery only"}]}`))
				if errRequest != nil {
					return fmt.Errorf("无法构造合成发现请求")
				}
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Content-Type", "application/json")
				resp, errSubmit := client.Do(req)
				if errSubmit != nil {
					return fmt.Errorf("合成 HTTP 对象发现失败")
				}
				_, errRead := io.Copy(io.Discard, resp.Body)
				errClose := resp.Body.Close()
				if resp.StatusCode != http.StatusOK || errRead != nil || errClose != nil {
					return fmt.Errorf("合成 HTTP 对象发现未完成模型执行")
				}
			}
			discovered = true
		}
		client.CloseIdleConnections()
		if jsonOutput {
			if errEncode := json.NewEncoder(os.Stdout).Encode(map[string]any{"event": "ready", "url": base + "/management.html", "pid": cmd.Process.Pid, "directory": dir}); errEncode != nil {
				return fmt.Errorf("无法输出就绪事件")
			}
		} else {
			fmt.Printf("管理页面 %s/management.html\n合成凭据见本命令代码；不要使用真实 Key。\n验收目录 %s\n", base, dir)
		}
		select {
		case <-done:
			ended = true
			if restartHost {
				continue
			}
			return fmt.Errorf("宿主在验收期间意外退出")
		case <-stop:
			return nil
		}
	}
}
