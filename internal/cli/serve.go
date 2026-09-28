package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/stone/kb/internal/mcp"
	"github.com/stone/kb/internal/webhook"
	"github.com/stone/kb/internal/webui"
)

func newServeCommand() *cobra.Command {
	var (
		webhookPort int
		uiPort      int
		host        string
		webhookOnly bool
		uiOnly      bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动 webhook 接收器与 Web UI",
		Long: strings.TrimSpace(`
两个监听端口（需求 §2.1）：

    7777  POST /webhook   接收 git hook 的变更通知，跑哈希比对、标记 stale
    7778  GET  /           Web UI，浏览 / 检索 / 在线编辑

两者可合并到同一端口（--ui-port 与 --port 相同）。
启动时会补投 .kb/outbox.jsonl 里积压的事件（服务未启动期间 hook 的上报）。
`),
		RunE: func(cmd *cobra.Command, args []string) error {
			// 参数自相矛盾先拦下来，不必等打开知识库
			if webhookOnly && uiOnly {
				return fmt.Errorf("--webhook-only 与 --ui-only 不能同时使用（想合并到同一端口，请让 --port 与 --ui-port 相同）")
			}

			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			if host == "" {
				host = knowledgeBase.Config.Server.Host
			}
			if webhookPort == 0 {
				webhookPort = knowledgeBase.Config.Server.WebhookPort
			}
			if uiPort == 0 {
				uiPort = knowledgeBase.Config.Server.UIPort
			}

			handler := webhook.NewHandler(knowledgeBase)

			// 补投积压事件
			if drained, drainErr := webhook.Drain(knowledgeBase.Config, handler); drainErr != nil {
				warnf("补投 outbox 失败: %v", drainErr)
			} else if drained > 0 {
				fmt.Printf("已补投 %d 条积压的变更事件\n", drained)
			}

			ui := webui.New(knowledgeBase)
			uiMux := ui.Mux()

			// 知识库是私有的：绑到非回环地址等于把个人笔记暴露到局域网
			if !isLoopbackHost(host) {
				warnf("正在监听 %s（非回环地址），知识库内容将对同网络的其他机器可见", host)
			}

			type listener struct {
				name    string
				port    int
				handler http.Handler
			}
			var listeners []listener

			switch {
			case webhookPort == uiPort:
				mux := http.NewServeMux()
				mux.Handle("/webhook", handler)
				mux.Handle("/", uiMux)
				listeners = append(listeners, listener{name: "webhook + web UI", port: webhookPort, handler: mux})
			case webhookOnly:
				listeners = append(listeners, listener{name: "webhook", port: webhookPort, handler: handler})
			case uiOnly:
				listeners = append(listeners, listener{name: "web UI", port: uiPort, handler: uiMux})
			default:
				listeners = append(listeners,
					listener{name: "webhook", port: webhookPort, handler: handler},
					listener{name: "web UI", port: uiPort, handler: uiMux},
				)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			servers := make([]*http.Server, 0, len(listeners))
			errorChannel := make(chan error, len(listeners))

			for _, item := range listeners {
				address := net.JoinHostPort(host, fmt.Sprintf("%d", item.port))
				probe, listenErr := net.Listen("tcp", address)
				if listenErr != nil {
					return fmt.Errorf("监听 %s（%s）失败: %w", address, item.name, listenErr)
				}
				server := &http.Server{
					Handler:           item.handler,
					ReadHeaderTimeout: 10 * time.Second,
				}
				servers = append(servers, server)

				if item.name == "web UI" {
					fmt.Printf("Web UI    http://%s\n", address)
				} else if item.name == "webhook" {
					fmt.Printf("Webhook   http://%s/webhook\n", address)
				} else {
					fmt.Printf("服务      http://%s  （/webhook 与 Web UI 合并）\n", address)
				}

				go func(server *http.Server, listener net.Listener) {
					if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
						errorChannel <- serveErr
					}
				}(server, probe)
			}

			fmt.Printf("\n知识库 %s\n", knowledgeBase.Config.Root)
			fmt.Println("Ctrl-C 退出")

			select {
			case <-ctx.Done():
				fmt.Println("\n正在关闭…")
			case serveErr := <-errorChannel:
				return serveErr
			}

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, server := range servers {
				_ = server.Shutdown(shutdownCtx)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&webhookPort, "port", 0, "webhook 监听端口（默认读配置，7777）")
	cmd.Flags().IntVar(&uiPort, "ui-port", 0, "Web UI 监听端口（默认读配置，7778）")
	cmd.Flags().StringVar(&host, "host", "", "监听地址（默认 127.0.0.1）")
	cmd.Flags().BoolVar(&webhookOnly, "webhook-only", false, "只启 webhook")
	cmd.Flags().BoolVar(&uiOnly, "ui-only", false, "只启 Web UI")
	return cmd
}

// isLoopbackHost 判断监听地址是否只对本机可见。
//
// 空串、"localhost"、"127.x.x.x"、"::1" 视为回环；其余（含 0.0.0.0 与具体网卡地址）
// 都会把知识库暴露给同网络的其他机器，调用方应给出告警。
func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "", "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func newMCPCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "以 stdio 方式运行 MCP Server（供 agent 接入）",
		Long: strings.TrimSpace(`
提供 5 个工具：

    kb_add      记录一条知识
    kb_search   按症状检索
    kb_at       按代码路径查
    kb_stale    列出过期条目
    kb_status   全局统计

接入示例见 docs/mcp-setup.md。协议走 newline-delimited JSON-RPC 2.0。
`),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			server := mcp.New(knowledgeBase, Version)
			return server.Run()
		},
	}
	return cmd
}
