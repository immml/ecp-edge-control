// Package transport 的终端子模块：沿 CommandStream 承载的 pty 会话。
//
// 控制面下发 TerminalControl(open/data/resize/close)，Agent 起一个 pty shell，
// pty 输出经 AgentMessage_TerminalData 回传。会话与连接解耦：断线后控制面
// 可重发 open 恢复同一 session_id 的进程（当前实现为每 open 起新 shell）。
package transport

import (
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// terminalSession 是一个 pty 会话。
type terminalSession struct {
	cmd  *exec.Cmd
	pty  *os.File
	once sync.Once
}

// terminalManager 管理本 Agent 的全部终端会话。
type terminalManager struct {
	mu       sync.Mutex
	sessions map[string]*terminalSession
	// send 把 TerminalData 上行发回控制面（由 transport 注入）。
	send func(d *ecpv1.TerminalData) error
}

func newTerminalManager() *terminalManager {
	return &terminalManager{sessions: map[string]*terminalSession{}}
}

// Handle 处理一条下行终端控制消息。
func (m *terminalManager) Handle(tc *ecpv1.TerminalControl) {
	switch f := tc.GetFrame().(type) {
	case *ecpv1.TerminalControl_Open:
		m.open(tc.GetSessionId(), f.Open)
	case *ecpv1.TerminalControl_Data:
		m.write(tc.GetSessionId(), f.Data)
	case *ecpv1.TerminalControl_Resize:
		m.resize(tc.GetSessionId(), f.Resize)
	case *ecpv1.TerminalControl_Close:
		m.close(tc.GetSessionId())
	}
}

// open 启动一个 pty shell 并开始上行转发。
func (m *terminalManager) open(sessionID string, o *ecpv1.TunnelOpen) {
	if sessionID == "" {
		return
	}
	shell := o.GetShell()
	if shell == "" {
		shell = "/bin/bash"
		if _, err := os.Stat(shell); err != nil {
			shell = "/bin/sh"
		}
	}
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	cols := o.GetCols()
	rows := o.GetRows()
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		_ = m.send(&ecpv1.TerminalData{SessionId: sessionID, Data: []byte("\r\n[ecp] 启动终端失败: " + err.Error() + "\r\n"), Closed: true})
		return
	}
	m.mu.Lock()
	if old, ok := m.sessions[sessionID]; ok {
		old.close() // 同 id 旧会话先回收
	}
	m.sessions[sessionID] = &terminalSession{cmd: cmd, pty: ptmx}
	m.mu.Unlock()

	// pty 输出 → 上行
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if serr := m.send(&ecpv1.TerminalData{SessionId: sessionID, Data: append([]byte{}, buf[:n]...)}); serr != nil {
					break // 连接断开，停止上行
				}
			}
			if err != nil {
				break
			}
		}
		_ = m.send(&ecpv1.TerminalData{SessionId: sessionID, Closed: true})
		m.close(sessionID)
	}()
}

func (m *terminalManager) write(sessionID string, data []byte) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s != nil {
		_, _ = s.pty.Write(data)
	}
}

func (m *terminalManager) resize(sessionID string, r *ecpv1.TunnelResize) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	m.mu.Unlock()
	if s != nil && r != nil && r.GetCols() > 0 && r.GetRows() > 0 {
		_ = pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(r.GetCols()), Rows: uint16(r.GetRows())})
	}
}

func (m *terminalManager) close(sessionID string) {
	m.mu.Lock()
	s := m.sessions[sessionID]
	delete(m.sessions, sessionID)
	m.mu.Unlock()
	if s != nil {
		s.close()
	}
}

func (s *terminalSession) close() {
	s.once.Do(func() {
		if s.cmd != nil && s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		if s.pty != nil {
			_ = s.pty.Close()
		}
		_, _ = s.cmd.Process.Wait() // 回收僵尸
	})
}
