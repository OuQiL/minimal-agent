// Package store 提供基于 SQLite 的持久化。
//
// 它是会话状态的唯一真源：进程重启后，会话历史、待办与工具调用 trace
// 都从这里恢复。所有涉及数据的读写方法都强制要求 sessionID，刻意不提供
// 「读取全部消息」这类无会话范围的方法——会话隔离是靠查询条件保证的，
// 与其在调用方小心翼翼地记得加 WHERE，不如让方法签名本身不给犯错的机会。
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"minimal-agent/internal/model"

	_ "modernc.org/sqlite"
)

// ErrNotFound 表示按标识查找的记录不存在。
var ErrNotFound = errors.New("记录不存在")

// Store 是 SQLite 存储的句柄。
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL DEFAULT '',
    summary    TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id   TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    role         TEXT NOT NULL,
    content      TEXT NOT NULL DEFAULT '',
    reasoning    TEXT NOT NULL DEFAULT '',
    tool_calls   TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS todos (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    content    TEXT NOT NULL,
    done       INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tool_traces (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  TEXT NOT NULL,
    tool_name   TEXT NOT NULL,
    args        TEXT NOT NULL DEFAULT '',
    result      TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    error       TEXT NOT NULL DEFAULT '',
    started_at  TEXT NOT NULL,
    ended_at    TEXT NOT NULL,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    alias       INTEGER NOT NULL DEFAULT 0,
    repeated    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, seq);
CREATE INDEX IF NOT EXISTS idx_todos_session        ON todos(session_id);
CREATE INDEX IF NOT EXISTS idx_traces_session       ON tool_traces(session_id, id);
`

// Open 打开（必要时创建）数据库并建表。
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// SQLite 是单写者。把连接数限制为 1 可从根上规避写冲突，
	// 代价是读也串行化——在本项目的规模下这个代价可以忽略。
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("建表失败: %w", err)
	}
	return &Store{db: db}, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// DB 暴露底层句柄，仅供测试断言表结构使用。
func (s *Store) DB() *sql.DB { return s.db }

const timeLayout = time.RFC3339Nano

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// --- 会话 ---

// sessionColumns 是会话查询的公共部分。
//
// 两个派生列：
//   - num：面向用户的编号，按创建时间升序从 1 开始。单用户场景下比随机标识
//     好输得多，且它由创建顺序推导，不随列表排序变化而改变。
//   - preview：首条用户消息，让用户一眼认出这是哪段对话。
const sessionColumns = `
	SELECT s.id, s.title, s.summary, s.created_at, s.updated_at,
	       ROW_NUMBER() OVER (ORDER BY s.created_at ASC, s.id ASC) AS num,
	       COALESCE((
	           SELECT m.content FROM messages m
	           WHERE m.session_id = s.id AND m.role = 'user'
	           ORDER BY m.seq ASC LIMIT 1
	       ), '') AS preview
	FROM sessions s`

// CreateSession 新建一个会话。
func (s *Store) CreateSession(id, title string) (*model.Session, error) {
	now := time.Now()
	if _, err := s.db.Exec(
		`INSERT INTO sessions (id, title, summary, created_at, updated_at) VALUES (?, ?, '', ?, ?)`,
		id, title, formatTime(now), formatTime(now),
	); err != nil {
		return nil, fmt.Errorf("创建会话失败: %w", err)
	}
	// 刚创建的是最新的一个，其编号即会话总数。
	var num int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&num); err != nil {
		return nil, fmt.Errorf("读取会话编号失败: %w", err)
	}
	return &model.Session{ID: id, Num: num, Title: title, CreatedAt: now, UpdatedAt: now}, nil
}

// GetSession 按内部标识读取会话。
func (s *Store) GetSession(id string) (*model.Session, error) {
	return s.querySession("id = ?", id, fmt.Sprintf("会话 %s", id))
}

// SessionByNum 按面向用户的编号读取会话。
func (s *Store) SessionByNum(num int) (*model.Session, error) {
	return s.querySession("num = ?", num, fmt.Sprintf("会话编号 %d", num))
}

// querySession 在全部会话上求值派生列之后再过滤。
//
// 过滤条件必须放在外层：若与 ROW_NUMBER() 同层，窗口函数只会看到过滤后的
// 那一行，编号恒为 1。
func (s *Store) querySession(where string, arg any, label string) (*model.Session, error) {
	row := s.db.QueryRow(`SELECT * FROM (`+sessionColumns+`) WHERE `+where, arg)

	var (
		sess                 model.Session
		createdAt, updatedAt string
	)
	err := row.Scan(&sess.ID, &sess.Title, &sess.Summary, &createdAt, &updatedAt,
		&sess.Num, &sess.Preview)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, label)
	}
	if err != nil {
		return nil, fmt.Errorf("读取会话失败: %w", err)
	}
	sess.CreatedAt, sess.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	return &sess, nil
}

// ListSessions 按最近活动时间倒序列出全部会话。
func (s *Store) ListSessions() ([]model.Session, error) {
	rows, err := s.db.Query(
		`SELECT * FROM (` + sessionColumns + `) ORDER BY updated_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("列出会话失败: %w", err)
	}
	defer rows.Close()

	var out []model.Session
	for rows.Next() {
		var (
			sess                 model.Session
			createdAt, updatedAt string
		)
		if err := rows.Scan(&sess.ID, &sess.Title, &sess.Summary, &createdAt, &updatedAt,
			&sess.Num, &sess.Preview); err != nil {
			return nil, fmt.Errorf("扫描会话失败: %w", err)
		}
		sess.CreatedAt, sess.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
		out = append(out, sess)
	}
	return out, rows.Err()
}

// UpdateSessionSummary 写回压缩摘要并顺带刷新最近活动时间。
func (s *Store) UpdateSessionSummary(id, summary string) error {
	res, err := s.db.Exec(
		`UPDATE sessions SET summary = ?, updated_at = ? WHERE id = ?`,
		summary, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("更新摘要失败: %w", err)
	}
	return requireAffected(res, id)
}

// TouchSession 刷新会话的最近活动时间。
func (s *Store) TouchSession(id string) error {
	res, err := s.db.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("刷新会话时间失败: %w", err)
	}
	return requireAffected(res, id)
}

func requireAffected(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: 会话 %s", ErrNotFound, id)
	}
	return nil
}

// --- 消息 ---

// AppendMessages 按给定顺序为一个会话追加消息，并自动分配递增序号。
//
// 一次调用内使用事务，使「读取当前最大序号」与「写入」之间不会被插入，
// 保证序号连续且不重复。
func (s *Store) AppendMessages(msgs ...model.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer tx.Rollback()

	for i := range msgs {
		m := &msgs[i]
		var maxSeq sql.NullInt64
		if err := tx.QueryRow(
			`SELECT MAX(seq) FROM messages WHERE session_id = ?`, m.SessionID,
		).Scan(&maxSeq); err != nil {
			return fmt.Errorf("读取序号失败: %w", err)
		}
		m.Seq = int(maxSeq.Int64) + 1

		calls, err := marshalToolCalls(m.ToolCalls)
		if err != nil {
			return err
		}
		if m.CreatedAt.IsZero() {
			m.CreatedAt = time.Now()
		}
		if _, err := tx.Exec(
			`INSERT INTO messages (session_id, seq, role, content, reasoning, tool_calls, tool_call_id, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			m.SessionID, m.Seq, string(m.Role), m.Content, m.Reasoning, calls, m.ToolCallID, formatTime(m.CreatedAt),
		); err != nil {
			return fmt.Errorf("写入消息失败: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at = ? WHERE id = ?`,
		formatTime(time.Now()), msgs[0].SessionID); err != nil {
		return fmt.Errorf("刷新会话时间失败: %w", err)
	}
	return tx.Commit()
}

// Messages 按序号升序读取一个会话的消息，limit<=0 表示不限制。
func (s *Store) Messages(sessionID string, limit int) ([]model.Message, error) {
	q := `SELECT id, session_id, seq, role, content, reasoning, tool_calls, tool_call_id, created_at
	      FROM messages WHERE session_id = ? ORDER BY seq ASC`
	args := []any{sessionID}

	if limit > 0 {
		// 取最近 limit 条，再按序号升序还原为时间顺序。
		q = `SELECT * FROM (
		         SELECT id, session_id, seq, role, content, reasoning, tool_calls, tool_call_id, created_at
		         FROM messages WHERE session_id = ? ORDER BY seq DESC LIMIT ?
		     ) ORDER BY seq ASC`
		args = append(args, limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("读取消息失败: %w", err)
	}
	defer rows.Close()

	var out []model.Message
	for rows.Next() {
		var (
			m         model.Message
			role      string
			calls     string
			createdAt string
		)
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Seq, &role, &m.Content, &m.Reasoning,
			&calls, &m.ToolCallID, &createdAt); err != nil {
			return nil, fmt.Errorf("扫描消息失败: %w", err)
		}
		m.Role = model.Role(role)
		if m.ToolCalls, err = unmarshalToolCalls(calls); err != nil {
			return nil, err
		}
		m.CreatedAt = parseTime(createdAt)
		out = append(out, m)
	}
	return out, rows.Err()
}

// CountMessages 返回一个会话的消息条数。
func (s *Store) CountMessages(sessionID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&n)
	return n, err
}

func marshalToolCalls(calls []model.ToolCall) (string, error) {
	if len(calls) == 0 {
		return "", nil
	}
	b, err := json.Marshal(calls)
	if err != nil {
		return "", fmt.Errorf("序列化工具调用失败: %w", err)
	}
	return string(b), nil
}

func unmarshalToolCalls(raw string) ([]model.ToolCall, error) {
	if raw == "" {
		return nil, nil
	}
	var calls []model.ToolCall
	if err := json.Unmarshal([]byte(raw), &calls); err != nil {
		return nil, fmt.Errorf("反序列化工具调用失败: %w", err)
	}
	return calls, nil
}

// --- 待办 ---

// AddTodo 为一个会话新增待办。
func (s *Store) AddTodo(sessionID, content string) (*model.Todo, error) {
	now := time.Now()
	res, err := s.db.Exec(
		`INSERT INTO todos (session_id, content, done, created_at) VALUES (?, ?, 0, ?)`,
		sessionID, content, formatTime(now))
	if err != nil {
		return nil, fmt.Errorf("新增待办失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &model.Todo{ID: id, SessionID: sessionID, Content: content, CreatedAt: now}, nil
}

// Todos 读取一个会话的全部待办。
func (s *Store) Todos(sessionID string) ([]model.Todo, error) {
	rows, err := s.db.Query(
		`SELECT id, session_id, content, done, created_at FROM todos WHERE session_id = ? ORDER BY id ASC`,
		sessionID)
	if err != nil {
		return nil, fmt.Errorf("读取待办失败: %w", err)
	}
	defer rows.Close()

	var out []model.Todo
	for rows.Next() {
		var (
			t         model.Todo
			done      int
			createdAt string
		)
		if err := rows.Scan(&t.ID, &t.SessionID, &t.Content, &done, &createdAt); err != nil {
			return nil, fmt.Errorf("扫描待办失败: %w", err)
		}
		t.Done = done != 0
		t.CreatedAt = parseTime(createdAt)
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- 工具调用 trace ---

// AppendTrace 写入一条工具调用记录。
func (s *Store) AppendTrace(t model.Trace) error {
	_, err := s.db.Exec(
		`INSERT INTO tool_traces
		 (session_id, tool_name, args, result, status, error, started_at, ended_at, duration_ms, alias, repeated)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.SessionID, t.ToolName, t.Args, t.Result, string(t.Status), t.Error,
		formatTime(t.StartedAt), formatTime(t.EndedAt), t.DurationMS, boolToInt(t.Alias), boolToInt(t.Repeated))
	if err != nil {
		return fmt.Errorf("写入 trace 失败: %w", err)
	}
	return nil
}

// Traces 按时间顺序读取一个会话的工具调用记录，limit<=0 表示不限制。
func (s *Store) Traces(sessionID string, limit int) ([]model.Trace, error) {
	q := `SELECT id, session_id, tool_name, args, result, status, error,
	             started_at, ended_at, duration_ms, alias, repeated
	      FROM tool_traces WHERE session_id = ? ORDER BY id ASC`
	args := []any{sessionID}
	if limit > 0 {
		q = `SELECT * FROM (
		         SELECT id, session_id, tool_name, args, result, status, error,
		                started_at, ended_at, duration_ms, alias, repeated
		         FROM tool_traces WHERE session_id = ? ORDER BY id DESC LIMIT ?
		     ) ORDER BY id ASC`
		args = append(args, limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("读取 trace 失败: %w", err)
	}
	defer rows.Close()

	var out []model.Trace
	for rows.Next() {
		var (
			t                  model.Trace
			status             string
			startedAt, endedAt string
			alias, repeated    int
		)
		if err := rows.Scan(&t.ID, &t.SessionID, &t.ToolName, &t.Args, &t.Result, &status, &t.Error,
			&startedAt, &endedAt, &t.DurationMS, &alias, &repeated); err != nil {
			return nil, fmt.Errorf("扫描 trace 失败: %w", err)
		}
		t.Status = model.TraceStatus(status)
		t.StartedAt, t.EndedAt = parseTime(startedAt), parseTime(endedAt)
		t.Alias, t.Repeated = alias != 0, repeated != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
