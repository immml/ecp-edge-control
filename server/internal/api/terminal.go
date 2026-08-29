package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/server/internal/auth"
	"ecp.dev/ecp/server/internal/store/model"
)

// upgrader 允许跨源（控制台与 API 同源，放宽便于调试）。
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 8192,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// TerminalWS 是浏览器 <-> 节点 PTY 的 WebSocket 桥。
//
// 鉴权：query ?token= (JWT)，要求 operator 以上权限（WS 无法带 header）。
// 上行：浏览器输入 → ControlMessage.TerminalControl(data)
// 下行：Agent 的 TerminalData → WS 文本帧
func (h *Handler) TerminalWS(c *gin.Context) {
	id := c.Param("id")

	// —— 鉴权（query token）——
	tok := c.Query("token")
	claims, err := auth.ParseToken(tok)
	if err != nil || !auth.RoleCan(claims.Role, model.RoleOperator) {
		fail(c, http.StatusUnauthorized, codeUnauth, "未认证或权限不足")
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return // gorilla 已写响应
	}
	defer conn.Close()

	if !h.sessions.IsOnline(id) {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("\r\n[ecp] 节点离线，无法打开终端\r\n"))
		return
	}

	sessionID := uuid.NewString()
	out := make(chan *ecpv1.TerminalData, 512)
	h.sessions.RegisterTerminalSink(sessionID, func(d *ecpv1.TerminalData) {
		select {
		case out <- d:
		default: // 消费者跟不上则丢弃，避免阻塞 gRPC 接收协程
		}
	})
	defer h.sessions.UnregisterTerminalSink(sessionID)

	cols, rows := termSize(c.Query("cols"), c.Query("rows"))
	open := &ecpv1.ControlMessage{
		Payload: &ecpv1.ControlMessage_Terminal{
			Terminal: &ecpv1.TerminalControl{
				SessionId: sessionID,
				Frame: &ecpv1.TerminalControl_Open{
					Open: &ecpv1.TunnelOpen{
						Type: ecpv1.TunnelSessionType_TUNNEL_SESSION_TYPE_TERMINAL,
						Cols: uint32(cols),
						Rows: uint32(rows),
					},
				},
			},
		},
	}
	if err := h.sessions.Send(id, open); err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("\r\n[ecp] 下发终端会话失败: "+err.Error()+"\r\n"))
		return
	}

	// —— 浏览器输入 → 节点 ——
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				_ = h.sessions.Send(id, &ecpv1.ControlMessage{
					Payload: &ecpv1.ControlMessage_Terminal{
						Terminal: &ecpv1.TerminalControl{
							SessionId: sessionID,
							Frame:     &ecpv1.TerminalControl_Close{Close: &ecpv1.TunnelClose{Code: 1000, Reason: "browser closed"}},
						},
					},
				})
				return
			}
			_ = h.sessions.Send(id, &ecpv1.ControlMessage{
				Payload: &ecpv1.ControlMessage_Terminal{
					Terminal: &ecpv1.TerminalControl{
						SessionId: sessionID,
						Frame:     &ecpv1.TerminalControl_Data{Data: data},
					},
				},
			})
		}
	}()

	// —— 节点输出 → 浏览器 ——
	for d := range out {
		if len(d.GetData()) > 0 {
			if err := conn.WriteMessage(websocket.TextMessage, d.GetData()); err != nil {
				break
			}
		}
		if d.GetClosed() {
			_ = conn.WriteMessage(websocket.TextMessage, []byte("\r\n[ecp] 终端会话已结束\r\n"))
			break
		}
	}
	_ = conn.Close()
}

// termSize 解析终端尺寸，非法值回退 80x24。
func termSize(cols, rows string) (int, int) {
	cc, cr := 80, 24
	if cols != "" {
		if n := atoiSafe(cols); n > 0 {
			cc = n
		}
	}
	if rows != "" {
		if n := atoiSafe(rows); n > 0 {
			cr = n
		}
	}
	return cc, cr
}

func atoiSafe(s string) int {
	s = strings.TrimSpace(s)
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
