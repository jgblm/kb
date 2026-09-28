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
		if queueErr := AppendOutbox(cfg.OutboxPath(), payload); queueErr != nil {
			return NotifyResult{Error: err.Error() + "；入队失败: " + queueErr.Error()}
		}
		return NotifyResult{Queued: true, Error: err.Error()}
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed Response
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return NotifyResult{Delivered: resp.StatusCode < 400, Error: string(raw)}
	}
	return NotifyResult{Delivered: resp.StatusCode < 400, Response: &parsed}
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
