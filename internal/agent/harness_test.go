package agent_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"minimal-agent/internal/agent"
	"minimal-agent/internal/contextmgr"
	"minimal-agent/internal/llm"
	"minimal-agent/internal/model"
	"minimal-agent/internal/store"
	"minimal-agent/internal/testsupport"
	"minimal-agent/internal/tool"
)

const testSession = "sess_test"

// harness 把一次循环测试所需的全部依赖装配起来。
type harness struct {
	t        *testing.T
	store    *store.Store
	registry *tool.Registry
	fake     *testsupport.FakeLLM
	loop     *agent.Loop
}

type harnessOpts struct {
	maxTurns     int
	maxParallel  int
	toolTimeout  time.Duration
	compactAfter int
	keepTurns    int
}

func newHarness(t *testing.T, scripts []testsupport.Script, opts harnessOpts) *harness {
	t.Helper()

	fake := testsupport.NewFakeLLM(scripts...)
	t.Cleanup(fake.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	if _, err := st.CreateSession(testSession, "测试会话"); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	registry := tool.NewRegistry()
	// 默认工具集。搜索故意不配密钥、外部地址指向不可达端口，
	// 使「未配置」与「调用失败」两条降级路径都能被测到。
	for _, tl := range []tool.Tool{
		tool.NewCalculator(),
		tool.NewWeather("http://127.0.0.1:1", "http://127.0.0.1:1", nil),
		tool.NewSearch("", "http://127.0.0.1:1", nil),
		tool.NewTodo(st),
	} {
		if err := registry.Register(tl); err != nil {
			t.Fatalf("注册工具失败: %v", err)
		}
	}

	maxTurns := opts.maxTurns
	if maxTurns == 0 {
		maxTurns = 3
	}
	maxParallel := opts.maxParallel
	if maxParallel == 0 {
		maxParallel = 4
	}
	toolTimeout := opts.toolTimeout
	if toolTimeout == 0 {
		toolTimeout = 5 * time.Second
	}
	compactAfter := opts.compactAfter
	if compactAfter == 0 {
		// 默认把阈值设得极高，使自动压缩不干扰循环测试。
		compactAfter = 10_000_000
	}
	keepTurns := opts.keepTurns
	if keepTurns == 0 {
		keepTurns = 6
	}

	client := llm.New(llm.Options{
		BaseURL:    fake.URL(),
		APIKey:     "test-key",
		Model:      "mock-model",
		MaxRetries: 0, // 测试要求确定性：重试行为由 llm 包自己的用例覆盖
		Timeout:    10 * time.Second,
	})

	builder := contextmgr.NewBuilder(contextmgr.Config{
		MaxHistoryMsgs:   40,
		CompactThreshold: compactAfter,
		KeepRecentTurns:  keepTurns,
	}, st)

	return &harness{
		t:        t,
		store:    st,
		registry: registry,
		fake:     fake,
		loop: agent.New(agent.Deps{
			Store:           st,
			Registry:        registry,
			Client:          client,
			Builder:         builder,
			Config:          agent.Config{MaxToolTurns: maxTurns},
			MaxToolParallel: maxParallel,
			ToolTimeout:     toolTimeout,
		}),
	}
}

func (h *harness) run(input string, sink llm.Sink) *agent.Exchange {
	h.t.Helper()
	ex, err := h.loop.Run(context.Background(), testSession, input, sink)
	if err != nil {
		h.t.Fatalf("循环执行失败: %v", err)
	}
	return ex
}

func (h *harness) messages() []model.Message {
	h.t.Helper()
	msgs, err := h.store.Messages(testSession, 0)
	if err != nil {
		h.t.Fatalf("读取消息失败: %v", err)
	}
	return msgs
}

func (h *harness) traces() []model.Trace {
	h.t.Helper()
	tr, err := h.store.Traces(testSession, 0)
	if err != nil {
		h.t.Fatalf("读取 trace 失败: %v", err)
	}
	return tr
}

// delayTool 是一个可控制耗时的工具，用于验证并发与定序。
type delayTool struct {
	name    string
	delay   time.Duration
	output  string
	started chan<- string
}

func (d *delayTool) Name() string            { return d.name }
func (d *delayTool) Description() string     { return "测试用延时工具 " + d.name }
func (d *delayTool) Aliases() []string       { return nil }
func (d *delayTool) Parameters() tool.Schema { return tool.Schema{Type: "object"} }

func (d *delayTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	if d.started != nil {
		d.started <- d.name
	}
	time.Sleep(d.delay)
	return d.output, nil
}

// register 把一个延时工具加入注册表。
func (h *harness) register(t tool.Tool) {
	h.t.Helper()
	if err := h.registry.Register(t); err != nil {
		h.t.Fatalf("注册工具失败: %v", err)
	}
}
