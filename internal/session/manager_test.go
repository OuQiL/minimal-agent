package session_test

import (
	"path/filepath"
	"strings"
	"testing"

	"minimal-agent/internal/model"
	"minimal-agent/internal/session"
	"minimal-agent/internal/store"
)

func newManager(t *testing.T) (*session.Manager, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return session.NewManager(s), s
}

func TestNew_CreatesAndSwitches(t *testing.T) {
	m, _ := newManager(t)

	if m.CurrentID() != "" {
		t.Errorf("初始不应有当前会话，实际 %q", m.CurrentID())
	}

	sess, err := m.New("第一个会话")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	if m.CurrentID() != sess.ID {
		t.Errorf("新建后当前会话 = %q，期望 %q", m.CurrentID(), sess.ID)
	}
	if sess.Title != "第一个会话" {
		t.Errorf("标题 = %q", sess.Title)
	}
	if !strings.HasPrefix(sess.ID, "sess_") {
		t.Errorf("会话标识格式异常: %q", sess.ID)
	}
}

func TestNew_GeneratesUniqueIDs(t *testing.T) {
	m, _ := newManager(t)
	seen := make(map[string]bool)
	for range 50 {
		sess, err := m.New("")
		if err != nil {
			t.Fatalf("新建失败: %v", err)
		}
		if seen[sess.ID] {
			t.Fatalf("会话标识重复: %s", sess.ID)
		}
		seen[sess.ID] = true
	}
}

func TestNew_DefaultTitle(t *testing.T) {
	m, _ := newManager(t)
	sess, err := m.New("")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	if sess.Title != session.DefaultTitle {
		t.Errorf("标题 = %q，期望 %q", sess.Title, session.DefaultTitle)
	}
}

// 切换失败时必须保持当前会话不变，否则一次笔误就丢失了正在进行的上下文。
// 编号按创建顺序分配，是面向用户的主要引用方式。
func TestSessions_AreNumberedFromOne(t *testing.T) {
	m, _ := newManager(t)

	a, err := m.New("甲")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	b, err := m.New("乙")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	c, err := m.New("丙")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}

	for want, sess := range map[int]*model.Session{1: a, 2: b, 3: c} {
		if sess.Num != want {
			t.Errorf("会话 %s 的编号 = %d，期望 %d", sess.Title, sess.Num, want)
		}
	}
	if m.CurrentNum() != 3 {
		t.Errorf("当前编号 = %d，期望 3", m.CurrentNum())
	}
}

func TestSwitch_ByNumber(t *testing.T) {
	m, _ := newManager(t)
	a, _ := m.New("甲")
	_, _ = m.New("乙")

	sess, err := m.Switch("1")
	if err != nil {
		t.Fatalf("按编号切换失败: %v", err)
	}
	if sess.ID != a.ID {
		t.Errorf("编号 1 对应 %q，期望 %q", sess.ID, a.ID)
	}
	if m.CurrentID() != a.ID || m.CurrentNum() != 1 {
		t.Errorf("当前会话未更新：id=%q num=%d", m.CurrentID(), m.CurrentNum())
	}
}

// 内部标识仍然可用，便于脚本与日志精确定位。
func TestSwitch_ByFullID(t *testing.T) {
	m, _ := newManager(t)
	_, _ = m.New("甲")
	b, _ := m.New("乙")

	if _, err := m.Switch(b.ID); err != nil {
		t.Fatalf("按标识切换失败: %v", err)
	}
	if m.CurrentID() != b.ID {
		t.Errorf("当前会话 = %q，期望 %q", m.CurrentID(), b.ID)
	}
}

func TestSwitch_NumberIsWhitespaceTolerant(t *testing.T) {
	m, _ := newManager(t)
	_, _ = m.New("甲")

	if _, err := m.Switch("  1  "); err != nil {
		t.Errorf("编号两侧的空白应被忽略，实际报错: %v", err)
	}
}

func TestSwitch_InvalidReferences(t *testing.T) {
	m, _ := newManager(t)
	first, _ := m.New("甲")

	cases := []struct {
		ref  string
		want string // 错误信息中应出现的关键词
	}{
		{"", "编号"},
		{"99", "99"},
		{"0", "正整数"},
		{"-1", "正整数"},
		{"sess_不存在", "sess_不存在"},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			_, err := m.Switch(tc.ref)
			if err == nil {
				t.Fatalf("%q 应切换失败", tc.ref)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息应包含 %q，实际：%v", tc.want, err)
			}
			// 切换失败时当前会话必须保持不变。
			if m.CurrentID() != first.ID {
				t.Errorf("切换失败后当前会话变成了 %q", m.CurrentID())
			}
		})
	}
}

func TestResolve_DoesNotChangeCurrent(t *testing.T) {
	m, _ := newManager(t)
	_, _ = m.New("甲")
	_, _ = m.New("乙")

	if _, err := m.Resolve("1"); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if m.CurrentNum() != 2 {
		t.Errorf("Resolve 不应改变当前会话，当前编号 = %d", m.CurrentNum())
	}
}

func TestSwitch_UnknownSessionKeepsCurrent(t *testing.T) {
	m, _ := newManager(t)

	first, err := m.New("甲")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}

	if _, err := m.Switch("sess_不存在"); err == nil {
		t.Fatal("切换到不存在的会话应返回错误")
	}
	if m.CurrentID() != first.ID {
		t.Errorf("切换失败后当前会话 = %q，期望保持 %q", m.CurrentID(), first.ID)
	}
}

func TestSwitch_BetweenSessions(t *testing.T) {
	m, _ := newManager(t)

	a, _ := m.New("甲")
	b, _ := m.New("乙")
	if m.CurrentID() != b.ID {
		t.Fatal("第二次新建后当前会话应为乙")
	}

	if _, err := m.Switch(a.ID); err != nil {
		t.Fatalf("切换失败: %v", err)
	}
	if m.CurrentID() != a.ID {
		t.Errorf("当前会话 = %q，期望 %q", m.CurrentID(), a.ID)
	}
}

func TestCurrent_BeforeAnySession(t *testing.T) {
	m, _ := newManager(t)
	cur, err := m.Current()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if cur != nil {
		t.Errorf("未选定会话时应返回 nil，实际 %+v", cur)
	}
}

func TestEnsure_CreatesWhenMissing(t *testing.T) {
	m, _ := newManager(t)

	sess, err := m.Ensure()
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if sess == nil || m.CurrentID() == "" {
		t.Fatal("Ensure 应创建一个当前会话")
	}

	// 再次调用应当复用，而不是又建一个。
	again, err := m.Ensure()
	if err != nil {
		t.Fatalf("Ensure 失败: %v", err)
	}
	if again.ID != sess.ID {
		t.Errorf("Ensure 不应重复创建，%q != %q", again.ID, sess.ID)
	}
}

func TestList(t *testing.T) {
	m, _ := newManager(t)
	if list, err := m.List(); err != nil || len(list) != 0 {
		t.Fatalf("初始列表应为空，err=%v, len=%d", err, len(list))
	}

	for _, title := range []string{"甲", "乙", "丙"} {
		if _, err := m.New(title); err != nil {
			t.Fatalf("新建失败: %v", err)
		}
	}
	list, err := m.List()
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("会话数量 = %d，期望 3", len(list))
	}
}

// 跨进程恢复：关闭存储后重新打开，会话与历史都应还在。
func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persist.db")

	// 第一次「进程」：建两个会话并写入历史。
	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	m1 := session.NewManager(s1)
	a, _ := m1.New("会话甲")
	b, _ := m1.New("会话乙")

	if err := s1.AppendMessages(
		model.Message{SessionID: a.ID, Role: model.RoleUser, Content: "甲的第一个问题"},
		model.Message{SessionID: a.ID, Role: model.RoleAssistant, Content: "甲的第一个回答"},
		model.Message{SessionID: b.ID, Role: model.RoleUser, Content: "乙的第一句话"},
	); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	s1.Close()

	// 第二次「进程」：重新打开。
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer s2.Close()
	m2 := session.NewManager(s2)

	list, err := m2.List()
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("重启后会话数量 = %d，期望 2", len(list))
	}

	if _, err := m2.Switch(a.ID); err != nil {
		t.Fatalf("重启后切换失败: %v", err)
	}
	msgs, err := s2.Messages(a.ID, 0)
	if err != nil {
		t.Fatalf("重启后读取历史失败: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("重启后历史条数 = %d，期望 2", len(msgs))
	}
	if msgs[0].Content != "甲的第一个问题" {
		t.Errorf("历史内容 = %q", msgs[0].Content)
	}

	// 重启后隔离仍然成立。
	bMsgs, _ := s2.Messages(b.ID, 0)
	if len(bMsgs) != 1 || bMsgs[0].Content != "乙的第一句话" {
		t.Errorf("会话乙的历史 = %+v", bMsgs)
	}
	for _, m := range bMsgs {
		if m.Content == "甲的第一个问题" {
			t.Error("会话乙不应包含会话甲的消息")
		}
	}
}
