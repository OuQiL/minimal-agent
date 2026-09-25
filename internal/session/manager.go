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

	"minimal-agent/internal/model"
	"minimal-agent/internal/store"
)

// DefaultTitle 是未指定标题时使用的会话标题。
const DefaultTitle = "新会话"

// Manager 管理当前会话与全部会话列表。
type Manager struct {
	store   *store.Store
	current string
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
	m.current = sess.ID
	return sess, nil
}

// Switch 切换当前会话。
//
// 切换失败时当前会话保持不变——否则一次笔误就会让用户丢失正在进行的上下文。
func (m *Manager) Switch(id string) (*model.Session, error) {
	sess, err := m.store.GetSession(id)
	if err != nil {
		return nil, fmt.Errorf("切换到会话 %s 失败：该会话不存在或无法读取", id)
	}
	m.current = sess.ID
	return sess, nil
}

// Current 返回当前会话；尚未选定任何会话时返回 nil。
func (m *Manager) Current() (*model.Session, error) {
	if m.current == "" {
		return nil, nil
	}
	return m.store.GetSession(m.current)
}

// CurrentID 返回当前会话标识，未选定时为空串。
func (m *Manager) CurrentID() string { return m.current }

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
