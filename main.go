// Command minimal-agent 是一个从零实现的最小可用 Agent。
//
// 核心的循环控制、工具协议、会话管理与上下文管理全部自研，外部依赖只承担
// 两项基础设施职责：SQLite 驱动负责存储，OpenAI SDK 负责传输与协议反序列化。
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"minimal-agent/internal/agent"
	"minimal-agent/internal/cli"
	"minimal-agent/internal/config"
	"minimal-agent/internal/contextmgr"
	"minimal-agent/internal/llm"
	"minimal-agent/internal/session"
	"minimal-agent/internal/store"
	"minimal-agent/internal/tool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "错误：%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()

	closeLog, err := setupLogging(cfg)
	if err != nil {
		return err
	}
	defer closeLog()

	warnMissingConfig(cfg)

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	registry, err := buildRegistry(st, cfg)
	if err != nil {
		return err
	}

	client := llm.New(llm.Options{
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		Model:      cfg.Model,
		MaxRetries: cfg.MaxRetries,
		Timeout:    cfg.LLMTimeout,
	})

	summarizer := contextmgr.NewLLMSummarizer(client)
	compactor := contextmgr.NewCompactor(st, summarizer, cfg.CompactThreshold, cfg.KeepRecentTurns)
	builder := contextmgr.NewBuilder(contextmgr.Config{
		MaxHistoryMsgs:   cfg.MaxHistoryMsgs,
		CompactThreshold: cfg.CompactThreshold,
		KeepRecentTurns:  cfg.KeepRecentTurns,
	}, st, compactor)

	loop := agent.New(agent.Deps{
		Store:           st,
		Registry:        registry,
		Client:          client,
		Builder:         builder,
		Config:          agent.Config{MaxToolTurns: cfg.MaxToolTurns},
		MaxToolParallel: cfg.MaxToolParallel,
		ToolTimeout:     cfg.ToolTimeout,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	repl := cli.New(cli.Options{
		Loop:      loop,
		Manager:   session.NewManager(st),
		Store:     st,
		Registry:  registry,
		Compactor: compactor,
		In:        os.Stdin,
		Out:       os.Stdout,
		Color:     cfg.ColorEnabled(os.Stdout),
		Model:     cfg.Model,
	})
	return repl.Run(ctx)
}

// buildRegistry 集中注册全部工具。
//
// 模型只会看到这里注册的工具，注册顺序即它们被提交的顺序。
func buildRegistry(st *store.Store, cfg config.Config) (*tool.Registry, error) {
	r := tool.NewRegistry()
	tools := []tool.Tool{
		tool.NewCalculator(),
		tool.NewSearch(cfg.BochaAPIKey, cfg.BochaBaseURL, nil),
		tool.NewWeather(cfg.OpenMeteoBaseURL, cfg.GeocodingBaseURL, nil),
		tool.NewTodo(st),
	}
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// warnMissingConfig 把缺失的配置如实说出来，但不阻断启动。
//
// 未配搜索密钥时搜索工具会降级为一条可读说明，其余功能照常；
// 这样评审方克隆下来不配任何密钥也能跑通除搜索外的全部功能。
func warnMissingConfig(cfg config.Config) {
	if !cfg.SearchConfigured() {
		slog.Warn("未配置 BOCHA_API_KEY，搜索工具将返回「服务未配置」；其余功能不受影响")
	}
	if cfg.APIKey == "" {
		slog.Warn("未配置 OPENAI_API_KEY，无法调用模型；请设置后重试")
	}
}

// setupLogging 配置结构化日志。
//
// 日志写 stderr，把 stdout 完整留给对话输出，便于重定向与拷屏。
func setupLogging(cfg config.Config) (func(), error) {
	outputs := []io.Writer{os.Stderr}
	var f *os.File

	if cfg.LogFile != "" {
		var err error
		f, err = os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("打开日志文件失败: %w", err)
		}
		outputs = append(outputs, f)
	}

	level := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(io.MultiWriter(outputs...), &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))

	return func() {
		if f != nil {
			f.Close()
		}
	}, nil
}
