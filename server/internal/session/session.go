// Package session 维护控制面侧的节点在线状态与会话。
//
// 设计约束：控制面按需上线，节点常在线。一份节点连接对应一个 gRPC 流，
// 这里只保存流的句柄与最新遥测，不持久化——节点状态本就该随控制面重启重建。
package session

import (
	"sync"
	"time"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// Session 是单个节点的活动连接。
type Session struct {
	NodeID   string
	Stream   ecpv1.AgentService_CommandStreamServer
	LastSeen time.Time
}

// Manager 管理所有在线节点的流与会话。
type Manager struct {
	mu        sync.RWMutex
	sessions  map[string]*Session
	ttl       time.Duration
	telemetry map[string]*ecpv1.Telemetry
	waiters   map[string]chan *ecpv1.CommandResult
}

// New 创建会话管理器。ttl 控制"多久没心跳算离线"。
func New(ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	return &Manager{
		sessions:  make(map[string]*Session),
		telemetry: make(map[string]*ecpv1.Telemetry),
		waiters:   make(map[string]chan *ecpv1.CommandResult),
		ttl:       ttl,
	}
}

// Attach 登记一个节点的流。
func (m *Manager) Attach(nodeID string, stream ecpv1.AgentService_CommandStreamServer) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &Session{NodeID: nodeID, Stream: stream, LastSeen: time.Now()}
	m.sessions[nodeID] = s
	return s
}

// Detach 移除节点连接。
func (m *Manager) Detach(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, nodeID)
}

// Touch 更新节点最后心跳时间。返回是否此前在线。
func (m *Manager) Touch(nodeID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[nodeID]
	if !ok {
		return false
	}
	s.LastSeen = time.Now()
	return true
}

// Send 向指定节点下发一条控制消息。失败（流断了）返回错误。
func (m *Manager) Send(nodeID string, msg *ecpv1.ControlMessage) error {
	m.mu.RLock()
	s, ok := m.sessions[nodeID]
	m.mu.RUnlock()
	if !ok {
		return ErrOffline
	}
	return s.Stream.Send(msg)
}

// OnlineCount 返回当前在线节点数。
func (m *Manager) OnlineCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n := 0
	for _, s := range m.sessions {
		if time.Since(s.LastSeen) <= m.ttl {
			n++
		}
	}
	return n
}

// IsOnline 判断节点是否在 TTL 窗口内活跃。
func (m *Manager) IsOnline(nodeID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[nodeID]
	if !ok {
		return false
	}
	return time.Since(s.LastSeen) <= m.ttl
}

// PutTelemetry 缓存节点最新遥测点。
func (m *Manager) PutTelemetry(nodeID string, t *ecpv1.Telemetry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.telemetry[nodeID] = t
}

// LatestTelemetry 返回节点最新遥测（可能为 nil）。
func (m *Manager) LatestTelemetry(nodeID string) *ecpv1.Telemetry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.telemetry[nodeID]
}

// RegisterWaiter 注册一个等待指定 trace_id 结果的通道，返回该通道。
// 调用方应在拿到结果或超时后调用 CancelWaiter 释放。
func (m *Manager) RegisterWaiter(traceID string) <-chan *ecpv1.CommandResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan *ecpv1.CommandResult, 1)
	m.waiters[traceID] = ch
	return ch
}

// CancelWaiter 移除指定 trace_id 的等待者。
func (m *Manager) CancelWaiter(traceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.waiters[traceID]; ok {
		close(ch)
		delete(m.waiters, traceID)
	}
}

// DeliverResult 把 Agent 回传的执行结果投递给对应的等待者。
// 没有等待者（如离线补传、心跳频道的无关帧）则静默丢弃。
func (m *Manager) DeliverResult(traceID string, res *ecpv1.CommandResult) bool {
	m.mu.Lock()
	ch, ok := m.waiters[traceID]
	if ok {
		delete(m.waiters, traceID)
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- res:
	default:
	}
	return true
}

var ErrOffline = ErrOfflineType("节点离线")

type ErrOfflineType string

func (e ErrOfflineType) Error() string { return string(e) }
