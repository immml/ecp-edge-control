// Package api 的 VNC WebSocket 桥接（GET /api/v1/nodes/:id/vnc/ws）。
//
// 状态：UI 入口已撤回（边缘节点为无头后端，不需要 VNC 远程桌面）。
// handler/路由暂保留在 server 中（不删除），将来需要时接入前端 VncView.vue
// 即可启用，不影响其他功能。
package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"ecp.dev/ecp/server/internal/auth"
	"ecp.dev/ecp/server/internal/store/model"
)

// VncWS 是浏览器（noVNC）<-> 节点 VNC(5900) 的 WebSocket 桥。
//
// 鉴权：query ?token= (JWT)，要求 operator 以上权限。
// 链路：浏览器 RFB-over-WebSocket(binary) → 控制面 → 节点 5900
//   - 优先 Tailscale IP 直连（控制机在 tailnet，WireGuard 加密传输）
//   - 无 Tailscale IP 时走 gRPC Tunnel 转发到节点 127.0.0.1:5900
// VNC 自身鉴权（密码）由 RFB 协议完成，平台不绕过。
func (h *Handler) VncWS(c *gin.Context) {
	id := c.Param("id")
	tok := c.Query("token")
	claims, err := auth.ParseToken(tok)
	if err != nil || !auth.RoleCan(claims.Role, model.RoleOperator) {
		fail(c, http.StatusUnauthorized, codeUnauth, "未认证或权限不足")
		return
	}

	port := 5900
	if p := c.Query("port"); p != "" {
		if n, e := strconv.Atoi(p); e == nil && n > 0 && n < 65536 {
			port = n
		}
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// 拨号到节点 5900
	upstream, err := h.dialVncTarget(c.Request.Context(), id, port)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("[ecp] VNC 连接失败: "+err.Error()))
		return
	}
	defer upstream.Close()

	// 浏览器 → 节点（RFB 二进制帧）
	go func() {
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.BinaryMessage || mt == websocket.TextMessage {
				if _, err := upstream.Write(data); err != nil {
					return
				}
			}
		}
	}()

	// 节点 → 浏览器（RFB 二进制帧）
	buf := make([]byte, 32768)
	for {
		n, err := upstream.Read(buf)
		if n > 0 {
			if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
}

// dialVncTarget 拨号节点 VNC：优先 Tailscale IP 直连，否则走 gRPC Tunnel。
func (h *Handler) dialVncTarget(ctx context.Context, nodeID string, port int) (net.Conn, error) {
	// 1) Tailscale IP 直连
	if n, err := h.store.GetNode(nodeID); err == nil && n.TailscaleIP != "" {
		addr := net.JoinHostPort(n.TailscaleIP, strconv.Itoa(port))
		d := net.Dialer{Timeout: 6 * time.Second}
		if conn, err := d.DialContext(ctx, "tcp", addr); err == nil {
			return conn, nil
		}
	}
	// 2) gRPC Tunnel 转发到节点本地 VNC 端口
	return h.dialTunnelTarget(ctx, nodeID, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

var _ = io.EOF
