package store_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"minimal-agent/internal/model"
	"minimal-agent/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpen_CreatesAllTables(t *testing.T) {
	s := newStore(t)
	for _, table := range []string{"sessions", "messages", "todos", "tool_traces"} {
		var name string
		err := s.DB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("表 %s 不存在: %v", table, err)
		}
	}
}

func TestOpen_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reopen.db")

	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	if _, err := s1.CreateSession("sess_1", "标题"); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	s1.Close()

	// 在已有库上重新打开，不应因建表语句而失败，数据也应保留。
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer s2.Close()

	sess, err := s2.GetSession("sess_1")
	if err != nil {
		t.Fatalf("重新打开后读取会话失败: %v", err)
	}
	if sess.Title != "标题" {
		t.Errorf("标题 = %q，期望「标题」", sess.Title)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := newStore(t)

	created, err := s.CreateSession("sess_a", "会话甲")
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if created.ID != "sess_a" || created.Title != "会话甲" {
		t.Errorf("返回的会话 = %+v", created)
	}

	got, err := s.GetSession("sess_a")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.ID != "sess_a" {
		t.Errorf("标识 = %q", got.ID)
	}

	if err := s.UpdateSessionSummary("sess_a", "这是摘要"); err != nil {
		t.Fatalf("更新摘要失败: %v", err)
	}
	got, _ = s.GetSession("sess_a")
	if got.Summary != "这是摘要" {
		t.Errorf("摘要 = %q，期望「这是摘要」", got.Summary)
	}
}

func TestGetSession_NotFound(t *testing.T) {
	s := newStore(t)
	_, err := s.GetSession("不存在")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("错误 = %v，期望 ErrNotFound", err)
	}
}

func TestUpdateSessionSummary_UnknownSession(t *testing.T) {
	s := newStore(t)
	if err := s.UpdateSessionSummary("不存在", "摘要"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("错误 = %v，期望 ErrNotFound", err)
	}
}

// 编号按创建顺序分配，与列表的排序无关。
//
// 这是编号能替代内部标识的前提：列表按最近活动倒序排，位置会变；
// 编号必须始终指向同一个会话，否则用户记下的编号隔天就指错了对象。
func TestSessions_NumberedByCreationOrderRegardlessOfListOrder(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"s1", "s2", "s3"} {
		if _, err := s.CreateSession(id, id); err != nil {
			t.Fatalf("创建失败: %v", err)
		}
	}
	// 刷新 s3 的活动时间，它会排到列表最前——但编号仍应是 3。
	if err := s.TouchSession("s3"); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}

	list, err := s.ListSessions()
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("会话数量 = %d", len(list))
	}
	if list[0].ID != "s3" {
		t.Fatalf("最近活动的会话应排在最前，实际 %q", list[0].ID)
	}

	want := map[string]int{"s1": 1, "s2": 2, "s3": 3}
	for _, sess := range list {
		if sess.Num != want[sess.ID] {
			t.Errorf("会话 %s 的编号 = %d，期望 %d", sess.ID, sess.Num, want[sess.ID])
		}
	}
}

func TestSessionByNum(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"s1", "s2", "s3"} {
		if _, err := s.CreateSession(id, id); err != nil {
			t.Fatalf("创建失败: %v", err)
		}
	}

	sess, err := s.SessionByNum(2)
	if err != nil {
		t.Fatalf("按编号读取失败: %v", err)
	}
	if sess.ID != "s2" {
		t.Errorf("编号 2 对应 %q，期望 s2", sess.ID)
	}
	if sess.Num != 2 {
		t.Errorf("返回的编号 = %d", sess.Num)
	}

	if _, err := s.SessionByNum(99); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("不存在的编号应返回 ErrNotFound，实际 %v", err)
	}
}

func TestCreateSession_ReturnsNumber(t *testing.T) {
	s := newStore(t)
	for i := range 3 {
		sess, err := s.CreateSession(fmt.Sprintf("s%d", i+1), "t")
		if err != nil {
			t.Fatalf("创建失败: %v", err)
		}
		if sess.Num != i+1 {
			t.Errorf("第 %d 个会话的编号 = %d，期望 %d", i+1, sess.Num, i+1)
		}
	}
}

// 会话列表要能让人认出这是哪段对话，因此带上首条用户消息的片段。
func TestSessions_PreviewIsFirstUserMessage(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("with", "有对话"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if _, err := s.CreateSession("empty", "空会话"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	if err := s.AppendMessages(
		model.Message{SessionID: "with", Role: model.RoleUser, Content: "北京今天天气怎么样？"},
		model.Message{SessionID: "with", Role: model.RoleAssistant, Content: "局部多云"},
		model.Message{SessionID: "with", Role: model.RoleUser, Content: "谢谢"},
	); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	list, err := s.ListSessions()
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	byID := map[string]model.Session{}
	for _, sess := range list {
		byID[sess.ID] = sess
	}

	// 取的是**首条**用户消息而非最后一条：首句最能说明这段对话是关于什么的。
	if got := byID["with"].Preview; got != "北京今天天气怎么样？" {
		t.Errorf("片段 = %q，期望首条用户消息", got)
	}
	if got := byID["empty"].Preview; got != "" {
		t.Errorf("空会话的片段应为空，实际 %q", got)
	}
}

func TestGetSession_IncludesNumberAndPreview(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("s1", "甲"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := s.AppendMessages(model.Message{
		SessionID: "s1", Role: model.RoleUser, Content: "第一句",
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	sess, err := s.GetSession("s1")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if sess.Num != 1 {
		t.Errorf("编号 = %d，期望 1", sess.Num)
	}
	if sess.Preview != "第一句" {
		t.Errorf("片段 = %q，期望「第一句」", sess.Preview)
	}
}

func TestListSessions_MostRecentFirst(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"s1", "s2", "s3"} {
		if _, err := s.CreateSession(id, id); err != nil {
			t.Fatalf("创建失败: %v", err)
		}
	}
	// 刷新 s1 的活动时间，它应当排到最前。
	if err := s.TouchSession("s1"); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}

	list, err := s.ListSessions()
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("会话数量 = %d，期望 3", len(list))
	}
	if list[0].ID != "s1" {
		t.Errorf("最近活动的会话 = %q，期望 s1", list[0].ID)
	}
}

func TestAppendMessages_AssignsIncreasingSeq(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	for i, content := range []string{"一", "二", "三"} {
		if err := s.AppendMessages(model.Message{
			SessionID: "sess", Role: model.RoleUser, Content: content,
		}); err != nil {
			t.Fatalf("写入第 %d 条失败: %v", i, err)
		}
	}

	msgs, err := s.Messages("sess", 0)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("消息数量 = %d，期望 3", len(msgs))
	}
	for i, m := range msgs {
		if m.Seq != i+1 {
			t.Errorf("第 %d 条消息的序号 = %d，期望 %d", i, m.Seq, i+1)
		}
	}
}

// tool_calls 以 JSON 原样存取，读取时结构应与写入完全一致。
func TestAppendMessages_ToolCallsRoundTrip(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	calls := []model.ToolCall{
		{ID: "call_1", Name: "weather", Args: `{"city":"北京","unit":"c"}`},
		{ID: "call_2", Name: "calculator", Args: `{"expression":"(1+2)*3"}`},
	}
	if err := s.AppendMessages(model.Message{
		SessionID: "sess", Role: model.RoleAssistant,
		Content: "我来查一下。", Reasoning: "先看天气", ToolCalls: calls,
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if err := s.AppendMessages(model.Message{
		SessionID: "sess", Role: model.RoleTool,
		Content: "北京晴", ToolCallID: "call_1",
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	msgs, err := s.Messages("sess", 0)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("消息数量 = %d", len(msgs))
	}
	got := msgs[0]
	if len(got.ToolCalls) != len(calls) {
		t.Fatalf("工具调用数量 = %d，期望 %d", len(got.ToolCalls), len(calls))
	}
	for i := range calls {
		if got.ToolCalls[i] != calls[i] {
			t.Errorf("第 %d 个调用 = %+v，期望 %+v", i, got.ToolCalls[i], calls[i])
		}
	}
	if got.Reasoning != "先看天气" {
		t.Errorf("思维链 = %q，应当落库", got.Reasoning)
	}
	if msgs[1].ToolCallID != "call_1" {
		t.Errorf("工具结果的消息关联标识 = %q", msgs[1].ToolCallID)
	}
}

func TestMessages_LimitReturnsMostRecentInOrder(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	for _, c := range []string{"一", "二", "三", "四", "五"} {
		if err := s.AppendMessages(model.Message{
			SessionID: "sess", Role: model.RoleUser, Content: c,
		}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}

	msgs, err := s.Messages("sess", 2)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("消息数量 = %d，期望 2", len(msgs))
	}
	// 取最近两条，但仍按时间升序返回。
	if msgs[0].Content != "四" || msgs[1].Content != "五" {
		t.Errorf("最近两条 = [%q, %q]，期望 [四, 五]", msgs[0].Content, msgs[1].Content)
	}
}

func TestCountMessages(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := s.AppendMessages(
		model.Message{SessionID: "sess", Role: model.RoleUser, Content: "a"},
		model.Message{SessionID: "sess", Role: model.RoleAssistant, Content: "b"},
	); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	n, err := s.CountMessages("sess")
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if n != 2 {
		t.Errorf("消息数量 = %d，期望 2", n)
	}
}

// 会话隔离的核心断言：跨会话读取绝不能串数据。
func TestDataIsIsolatedBetweenSessions(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"s1", "s2"} {
		if _, err := s.CreateSession(id, id); err != nil {
			t.Fatalf("创建失败: %v", err)
		}
	}

	if err := s.AppendMessages(
		model.Message{SessionID: "s1", Role: model.RoleUser, Content: "会话一的消息"},
		model.Message{SessionID: "s2", Role: model.RoleUser, Content: "会话二的消息"},
	); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if _, err := s.AddTodo("s1", "会话一的待办"); err != nil {
		t.Fatalf("写入待办失败: %v", err)
	}
	if err := s.AppendTrace(model.Trace{
		SessionID: "s1", ToolName: "weather", Status: model.TraceOK,
	}); err != nil {
		t.Fatalf("写入 trace 失败: %v", err)
	}

	msgs1, _ := s.Messages("s1", 0)
	if len(msgs1) != 1 || msgs1[0].Content != "会话一的消息" {
		t.Errorf("会话一的消息 = %+v", msgs1)
	}
	msgs2, _ := s.Messages("s2", 0)
	if len(msgs2) != 1 || msgs2[0].Content != "会话二的消息" {
		t.Errorf("会话二的消息 = %+v", msgs2)
	}

	todos2, _ := s.Todos("s2")
	if len(todos2) != 0 {
		t.Errorf("会话二不应看到会话一的待办，实际 %+v", todos2)
	}
	traces2, _ := s.Traces("s2", 0)
	if len(traces2) != 0 {
		t.Errorf("会话二不应看到会话一的 trace，实际 %+v", traces2)
	}
}

func TestTodos(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	first, err := s.AddTodo("sess", "买牛奶")
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if first.ID == 0 {
		t.Error("应返回自增编号")
	}
	if _, err := s.AddTodo("sess", "写周报"); err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	items, err := s.Todos("sess")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("待办数量 = %d，期望 2", len(items))
	}
	if items[0].Content != "买牛奶" || items[1].Content != "写周报" {
		t.Errorf("待办顺序或内容不对: %+v", items)
	}
	if items[0].Done {
		t.Error("新建待办不应标记为已完成")
	}
}

func TestTraces_RoundTrip(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSession("sess", "t"); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	in := model.Trace{
		SessionID:  "sess",
		ToolName:   "weather",
		Args:       `{"city":"北京"}`,
		Result:     "北京晴，23.3℃",
		Status:     model.TraceError,
		Error:      "模拟的错误",
		DurationMS: 123,
		Alias:      true,
		Repeated:   true,
	}
	if err := s.AppendTrace(in); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	out, err := s.Traces("sess", 0)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("trace 数量 = %d", len(out))
	}
	got := out[0]
	if got.ToolName != in.ToolName || got.Args != in.Args || got.Result != in.Result {
		t.Errorf("内容不一致: %+v", got)
	}
	if got.Status != model.TraceError || got.Error != in.Error {
		t.Errorf("状态或错误信息不一致: %+v", got)
	}
	if got.DurationMS != in.DurationMS {
		t.Errorf("耗时 = %d，期望 %d", got.DurationMS, in.DurationMS)
	}
	if !got.Alias || !got.Repeated {
		t.Errorf("诊断标记丢失: alias=%v repeated=%v", got.Alias, got.Repeated)
	}
}
