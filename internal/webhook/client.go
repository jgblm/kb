package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stone/kb/internal/config"
)

// NotifyResult 一次上报的结果。
type NotifyResult struct {
	Delivered bool
	Queued    bool
	Response  *Response
	Error     string
}

// Endpoint 拼出 webhook 地址。
func Endpoint(cfg *config.Config) string {
	host := cfg.Server.Host
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/webhook", host, cfg.Server.WebhookPort)
}

// Notify 上报变更。服务不可达时把事件写入 outbox，等下次 kb serve 补投。
func Notify(cfg *config.Config, payload Payload) NotifyResult {
	if payload.Timestamp == 0 {
		payload.Timestamp = time.Now().Unix()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return NotifyResult{Error: err.Error()}
	}

	request, err := http.NewRequest(http.MethodPost, Endpoint(cfg), bytes.NewReader(body))
	if err != nil {
		return NotifyResult{Error: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	if cfg.Server.Token != "" {
		request.Header.Set("X-KB-Token", cfg.Server.Token)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(request)
	if err != nil {
		// 服务没起：排队，不丢事件
		return queue(cfg, payload, err.Error())
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed Response
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// 端口上蹲着的不是 kb（或版本不兼容）：同样排队而不是丢掉
		return queue(cfg, payload, fmt.Sprintf("响应不是 kb 的格式（HTTP %d）", resp.StatusCode))
	}
	if !parsed.OK {
		// 端口上确实有 kb，但它不认识这个仓库 —— 通常是另一个知识库占了这个端口，
		// 或者仓库还没登记。事件排队等下次 kb serve 补投，好过静默丢失。
		return queue(cfg, payload, parsed.Error)
	}
	return NotifyResult{Delivered: true, Response: &parsed}
}

// queue 把事件写入 outbox，并保留失败原因供调用方展示。
func queue(cfg *config.Config, payload Payload, reason string) NotifyResult {
	if err := AppendOutbox(cfg.OutboxPath(), payload); err != nil {
		return NotifyResult{Error: reason + "；入队失败: " + err.Error()}
	}
	return NotifyResult{Queued: true, Error: reason}
}

// Drain 把 outbox 里积压的事件补投一次。
func Drain(cfg *config.Config, handler *Handler) (int, error) {
	payloads, err := ReadOutbox(cfg.OutboxPath())
	if err != nil || len(payloads) == 0 {
		return 0, err
	}
	processed := 0
	for _, payload := range payloads {
		handler.Handle(payload)
		processed++
	}
	if err := ClearOutbox(cfg.OutboxPath()); err != nil {
		return processed, err
	}
	return processed, nil
}
