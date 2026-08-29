// Package transport 的隧道子模块：经 Tunnel 双向流做端口转发。
//
// 浏览器 → 控制面 /panel/:node/ 反向代理 → gRPC Tunnel 流 → Agent → 节点本地端口。
// Agent 启动时打开一条常驻 Tunnel 流等待 open 帧；open(target_addr) 后
// 拨号到节点本地地址（如 127.0.0.1:31252 的 1Panel），双向转发字节。
package transport

import (
	"context"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/agent/internal/register"
)

// tunnelSession 是一个端口转发会话。conn 为 nil 表示仍在拨号中，
// 期间到达的输入先缓冲进 buf，拨号完成后一次性 flush——避免请求字节被丢弃。
type tunnelSession struct {
	mu   sync.Mutex
	conn net.Conn
	buf  []byte
}

// tunnelState 管理全部隧道会话。
type tunnelState struct {
	mu     sync.Mutex // 保护 conns map
	sendMu sync.Mutex // gRPC 流 Send 必须串行，多会话并发上行会破坏流
	conns  map[string]*tunnelSession
	stream ecpv1.AgentService_TunnelClient
	log    func(string, ...any)
}

// openTunnel 启动常驻 Tunnel 流（PORT_FORWARD 服务）。阻塞直至流断开。
func (t *Transport) openTunnel(ctx context.Context, conn *grpc.ClientConn, id *register.Identity) error {
	client := ecpv1.NewAgentServiceClient(conn)
	stream, err := client.Tunnel(ctx)
	if err != nil {
		t.log.Warn("打开隧道失败", "err", err)
		return err
	}
	st := &tunnelState{conns: map[string]*tunnelSession{}, stream: stream, log: t.log.Info}
	t.log.Info("隧道已打开，等待转发会话")
	for {
		chunk, err := stream.Recv()
		if err != nil {
			st.closeAll()
			t.log.Warn("隧道断开", "err", err)
			return err
		}
		switch f := chunk.GetFrame().(type) {
		case *ecpv1.TunnelChunk_Open:
			t.log.Info("隧道 open", "sid", chunk.GetSessionId(), "type", f.Open.GetType(), "target", f.Open.GetTargetAddr())
			if f.Open.GetType() == ecpv1.TunnelSessionType_TUNNEL_SESSION_TYPE_PORT_FORWARD {
				st.open(chunk.GetSessionId(), f.Open.GetTargetAddr())
			}
		case *ecpv1.TunnelChunk_Data:
			st.write(chunk.GetSessionId(), f.Data)
		case *ecpv1.TunnelChunk_Close:
			t.log.Info("隧道 close", "sid", chunk.GetSessionId(), "code", f.Close.GetCode())
			st.close(chunk.GetSessionId())
		}
	}
}

// open 登记会话并异步拨号（拨号期间输入先缓冲）。
func (st *tunnelState) open(sid, target string) {
	if sid == "" || target == "" {
		return
	}
	st.mu.Lock()
	if old, ok := st.conns[sid]; ok && old.conn != nil {
		_ = old.conn.Close()
	}
	ses := &tunnelSession{}
	st.conns[sid] = ses
	st.mu.Unlock()
	go st.dialAndForward(sid, ses, target)
}

func (st *tunnelState) dialAndForward(sid string, ses *tunnelSession, target string) {
	conn, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		st.log("隧道 dial 失败", "sid", sid, "target", target, "err", err)
		_ = st.send(&ecpv1.TunnelChunk{
			SessionId: sid,
			Frame:     &ecpv1.TunnelChunk_Close{Close: &ecpv1.TunnelClose{Code: 1, Reason: "dial failed: " + err.Error()}},
		})
		st.close(sid)
		return
	}
	// 挂上 conn 并 flush 拨号期间缓冲的输入
	ses.mu.Lock()
	ses.conn = conn
	if len(ses.buf) > 0 {
		_, _ = conn.Write(ses.buf)
		ses.buf = nil
	}
	ses.mu.Unlock()
	st.log("隧道 dial 成功", "sid", sid, "target", target)

	// 目标 → 控制面
	go func() {
		buf := make([]byte, 32768)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, buf[:n])
				if serr := st.send(&ecpv1.TunnelChunk{
					SessionId: sid,
					Frame:     &ecpv1.TunnelChunk_Data{Data: cp},
				}); serr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		_ = st.send(&ecpv1.TunnelChunk{
			SessionId: sid,
			Frame:     &ecpv1.TunnelChunk_Close{Close: &ecpv1.TunnelClose{Code: 0, Reason: "eof"}},
		})
		st.close(sid)
	}()
}

// write 把控制面下发的输入写到目标连接；拨号未完成先缓冲。
func (st *tunnelState) write(sid string, data []byte) {
	st.mu.Lock()
	ses := st.conns[sid]
	st.mu.Unlock()
	if ses == nil || len(data) == 0 {
		return
	}
	ses.mu.Lock()
	defer ses.mu.Unlock()
	if ses.conn != nil {
		_, _ = ses.conn.Write(data)
	} else {
		ses.buf = append(ses.buf, data...)
	}
}

func (st *tunnelState) close(sid string) {
	st.mu.Lock()
	ses := st.conns[sid]
	delete(st.conns, sid)
	st.mu.Unlock()
	if ses != nil {
		ses.mu.Lock()
		if ses.conn != nil {
			_ = ses.conn.Close()
		}
		ses.buf = nil
		ses.mu.Unlock()
	}
}

func (st *tunnelState) closeAll() {
	st.mu.Lock()
	defer st.mu.Unlock()
	for sid, ses := range st.conns {
		ses.mu.Lock()
		if ses.conn != nil {
			_ = ses.conn.Close()
		}
		ses.mu.Unlock()
		delete(st.conns, sid)
	}
}

func (st *tunnelState) send(chunk *ecpv1.TunnelChunk) error {
	st.sendMu.Lock()
	defer st.sendMu.Unlock()
	return st.stream.Send(chunk)
}
