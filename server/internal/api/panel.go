package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// panelTarget 是 1Panel 在节点上的本地监听地址。
const panelTarget = "127.0.0.1:31252"

// PanelProxy 把 /api/v1/nodes/:id/panel/*path 反向代理到节点本地的 1Panel。
//
// 浏览器只与控制台同源通信，流量经 gRPC 隧道转发到节点 127.0.0.1:31252。
// 仅做网络可达；1Panel 自身的登录鉴权（含安全入口）由 1Panel 执行，平台不绕过。
func (h *Handler) PanelProxy(c *gin.Context) {
	nodeID := c.Param("id")

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1, // 流式响应（日志/下载等）
		Director: func(req *http.Request) {
			// 去掉控制台侧前缀，还原 1Panel 原始路径
			prefix := "/api/v1/nodes/" + nodeID + "/panel"
			req.URL.Scheme = "http"
			req.URL.Host = "node-panel"
			req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
			if req.URL.Path == "" {
				req.URL.Path = "/"
			}
		},
		Transport: &http.Transport{
			// 隧道会话是一次性的：每次请求新建会话，禁掉 keep-alive 防止复用已关闭连接
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return h.dialPanelTunnel(ctx, nodeID)
			},
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "1Panel 隧道不可用："+err.Error(), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

// dialPanelTunnel 为一次反向代理连接建立隧道会话，返回虚拟 net.Conn。
func (h *Handler) dialPanelTunnel(ctx context.Context, nodeID string) (net.Conn, error) {
	return h.dialTunnelTarget(ctx, nodeID, panelTarget)
}

// dialTunnelTarget 建立到节点指定本地地址的隧道会话（panel 用 31252，VNC 用 5900）。
func (h *Handler) dialTunnelTarget(ctx context.Context, nodeID, target string) (net.Conn, error) {
	_, out, write, closeFn, err := h.grpc.OpenTunnelSession(nodeID, target)
	if err != nil {
		return nil, err
	}
	return &tunnelConn{write: write, closeFn: closeFn, in: out}, nil
}

// tunnelConn 把隧道会话包装成 net.Conn 供 httputil 使用。
type tunnelConn struct {
	write   func([]byte) error
	closeFn func()
	in      <-chan []byte
	once    sync.Once
}

func (c *tunnelConn) Read(p []byte) (int, error) {
	data, ok := <-c.in
	if !ok {
		return 0, io.EOF
	}
	return copy(p, data), nil
}

func (c *tunnelConn) Write(p []byte) (int, error) {
	if err := c.write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *tunnelConn) Close() error {
	c.once.Do(c.closeFn)
	return nil
}

func (c *tunnelConn) LocalAddr() net.Addr               { return dummyAddr("local") }
func (c *tunnelConn) RemoteAddr() net.Addr              { return dummyAddr("tunnel") }
func (c *tunnelConn) SetDeadline(t time.Time) error     { return nil }
func (c *tunnelConn) SetReadDeadline(t time.Time) error { return nil }
func (c *tunnelConn) SetWriteDeadline(t time.Time) error { return nil }

type dummyAddr string

func (d dummyAddr) Network() string { return "tunnel" }
func (d dummyAddr) String() string  { return string(d) }
