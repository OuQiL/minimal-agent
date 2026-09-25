// Package session 管理会话的创建、切换与查询。
//
// 会话的对话历史与工具产生的会话内状态（如待办）都按会话标识隔离。
// 隔离靠的是存储层的查询条件——所有读写都必须带上会话标识，
// 因此这里不持有任何跨会话的缓存，切换会话只是改变一个标识。
package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"minimal-agent/internal/model"
	"minimal-agent/internal/store"
)

// DefaultTitle 是未指定标题时使用的会话标题。
const DefaultTitle = "新会话"

// Manager 管理当前会话与全部会话列表。
type Manager struct {
	store      *store.Store
	current    string
	currentNum int
}

// NewManager 创建一个会话管理器。当前会话在首次 New 或 Switch 前为空。
func NewManager(s *store.Store) *Manager {
	return &Manager{store: s}
}

// New 创建一个新会话并将其设为当前会话。
func (m *Manager) New(title string) (*model.Session, error) {
	if title == "" {
		title = DefaultTitle
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	sess, err := m.store.CreateSession(id, title)
	if err != nil {
		return nil, err
	}
	m.current, m.currentNum = sess.ID, sess.Num
	return sess, nil
}

// Switch 按引用切换当前会话。
//
// 引用可以是面向用户的编号（如 "2"），也可以是内部标识（如 "sess_2efdb9c24d"）。
// 单用户场景下手输随机标识并不现实，因此编号是主要方式；标识保留下来是为了
// 脚本与日志里能精确定位。
//
// 切换失败时当前会话保持不变——否则一次笔误就会让用户丢失正在进行的上下文。
func (m *Manager) Switch(ref string) (*model.Session, error) {
	sess, err := m.Resolve(ref)
	if err != nil {
		return nil, err
	}
	m.current, m.currentNum = sess.ID, sess.Num
	return sess, nil
}

// Resolve 把用户给的引用解析成会话，但不改变当前会话。
func (m *Manager) Resolve(ref string) (*model.Session, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("请提供会话编号，例如 /switch 2；用 /list 查看全部会话")
	}

	// 内部标识一律以 sess_ 开头，因此纯数字的输入一定是编号，不会混淆。
	if n, err := strconv.Atoi(ref); err == nil {
		if n <= 0 {
			return nil, fmt.Errorf("会话编号必须是正整数，实际收到 %d", n)
		}
		sess, err := m.store.SessionByNum(n)
		if err != nil {
			return nil, fmt.Errorf("没有编号为 %d 的会话，用 /list 查看可用的编号", n)
		}
		return sess, nil
	}

	sess, err := m.store.GetSession(ref)
	if err != nil {
		return nil, fmt.Errorf("找不到会话 %s，用 /list 查看可用的会话", ref)
	}
	return sess, nil
}

// Current 返回当前会话；尚未选定任何会话时返回 nil。
func (m *Manager) Current() (*model.Session, error) {
	if m.current == "" {
		return nil, nil
	}
	sess, err := m.store.GetSession(m.current)
	if err != nil {
		return nil, err
	}
	m.currentNum = sess.Num
	return sess, nil
}

// CurrentID 返回当前会话标识，未选定时为空串。
func (m *Manager) CurrentID() string { return m.current }

// CurrentNum 返回当前会话的面向用户的编号，未选定时为 0。
func (m *Manager) CurrentNum() int { return m.currentNum }

// List 列出全部会话，按最近活动时间倒序。
func (m *Manager) List() ([]model.Session, error) { return m.store.ListSessions() }

// Ensure 保证存在一个当前会话，没有就创建一个。
func (m *Manager) Ensure() (*model.Session, error) {
	cur, err := m.Current()
	if err != nil {
		return nil, err
	}
	if cur != nil {
		return cur, nil
	}
	return m.New("")
}

// newID 生成一个短小且唯一的会话标识。
func newID() (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成会话标识失败: %w", err)
	}
	return "sess_" + hex.EncodeToString(b), nil
}
