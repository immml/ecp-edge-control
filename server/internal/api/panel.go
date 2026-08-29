package api

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
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
//
// 1Panel 页面里的资源（/assets/...、/static/...、API 调用等）都是相对路径，
// 浏览器按当前 origin 解析后会落在控制台的根路径上，不会自动带上 panel 前缀。
// 解法：ModifyResponse 在 1Panel 返回的 HTML 头部注入 <base href>，让浏览器
// 把 1Panel 内部所有相对 URL 自动改写为 panel 路径，资源/API 再走隧道回来。
func (h *Handler) PanelProxy(c *gin.Context) {
	nodeID := c.Param("id")
	panelPrefix := "/api/v1/nodes/" + nodeID + "/panel"

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1, // 流式响应（日志/下载等）
		Director: func(req *http.Request) {
			// 去掉控制台侧前缀，还原 1Panel 原始路径
			req.URL.Scheme = "http"
			req.URL.Host = "node-panel"
			req.URL.Path = strings.TrimPrefix(req.URL.Path, panelPrefix)
			if req.URL.Path == "" {
				req.URL.Path = "/"
			}
			// 不要压缩响应——我们要在 ModifyResponse 里改 body，gzip 不一致会破
			req.Header.Set("Accept-Encoding", "identity")
		},
		ModifyResponse: func(resp *http.Response) error {
			ct := resp.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "text/html") {
				return nil
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			// 注入 <base href>，浏览器会把 HTML 里的所有相对 URL（/assets/...、
			// /api/v1/...、图片字体等）以 panel 路径为根解析，自动走隧道回来。
			baseTag := fmt.Sprintf(`<base href="%s/">`, panelPrefix)
			s := string(body)
			// 优先插在 <head> 后；若无 <head> 则插到 <html> 后；都没有就插最前
			switch {
			case strings.Contains(s, "<head>"):
				s = strings.Replace(s, "<head>", "<head>"+baseTag, 1)
			case strings.Contains(s, "<HEAD>"):
				s = strings.Replace(s, "<HEAD>", "<HEAD>"+baseTag, 1)
			case strings.Contains(s, "<html>"):
				s = strings.Replace(s, "<html>", "<html>"+baseTag, 1)
			default:
				s = baseTag + s
			}
			resp.Body = io.NopCloser(strings.NewReader(s))
			resp.ContentLength = int64(len(s))
			resp.Header.Set("Content-Length", strconv.Itoa(len(s)))
			// 移除不一致的压缩头
			resp.Header.Del("Content-Encoding")
			return nil
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
	_, out, write, closeFn, err := h.grpc.OpenTunnelSession(nodeID, panelTarget)
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
