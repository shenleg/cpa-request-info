// 本命令用合成凭据启动隔离宿主与可控上游，用于真实浏览器验收；停止进程但不删除验收目录。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
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
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, "synthetic read failure", 400)
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
`, port, filepath.Join(dir, "auths"), filepath.Join(root, ".build/plugins"), filepath.Join(dir, "audit"), "http://"+up.Addr().String()+"/v1")
	configPath := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(configPath, []byte(config), 0600); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "host.log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(filepath.Join(root, ".build/bin/cliproxyapi"), "--config", configPath, "--local-model")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir, "MANAGEMENT_STATIC_PATH=" + static}
	cmd.Stdout = log
	cmd.Stderr = log
	if err = cmd.Start(); err != nil {
		return err
	}
	fmt.Printf("管理页面 http://127.0.0.1:%d/management.html\n合成凭据见本命令代码；不要使用真实 Key。\n验收目录 %s\n", port, dir)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-stop:
		_ = cmd.Process.Signal(os.Interrupt)
		<-done
		return nil
	}
}
