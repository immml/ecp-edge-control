// Command desktop 是 ECP 控制面的桌面 APP 形态。
//
// 目标：**双击即用**——不需要浏览器、不需要手动敲命令。
// 实现：复用控制面全部核心逻辑（配置/存储/CA/gRPC/指令分发/控制台），
// 但把控制台从"HTTPS :8443"改为"HTTP 127.0.0.1 随机端口"，再用
// WebView2 窗口加载它。节点回连仍走 gRPC :7443，不受影响。
//
// 为什么 HTTP 而非 HTTPS：
//   - WebView2 对自签证书会整页拦截，体验极差
//   - 本方案监听仅 127.0.0.1 回环，无证书也安全（Peer 不能从外部访问）
//   - 登录态加密由 JWT 头承担，回环传输无泄露面
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jchv/go-webview2"

	"ecp.dev/ecp/server/internal/api"
	"ecp.dev/ecp/server/internal/auth"
	"ecp.dev/ecp/server/internal/ca"
	"ecp.dev/ecp/server/internal/command"
	"ecp.dev/ecp/server/internal/config"
	"ecp.dev/ecp/server/internal/grpcserver"
	"ecp.dev/ecp/server/internal/store"
	"ecp.dev/ecp/server/internal/store/model"
	"ecp.dev/ecp/server/internal/web"
)

var version = "dev"

func main() {
	// 配置：桌面模式默认用同目录下的 desktop.yaml / 环境变量 ECP_CONFIG，
	// 缺失则纯默认（server.Listen 会被覆盖为回环随机端口）。
	cfgPath := os.Getenv("ECP_CONFIG")
	if cfgPath == "" {
		cfgPath = filepath.Join(exeDir(), "desktop.yaml")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置加载失败: %v\n", err)
		os.Exit(1)
	}

	// 桌面模式强制：数据目录与配置同目录（便携，移动整个文件夹即迁移）
	if cfg.Server.DataDir == "" || cfg.Server.DataDir == "./runtime/data" {
		cfg.Server.DataDir = filepath.Join(exeDir(), "data")
	}
	if cfg.Store.DSN == "" || cfg.Store.DSN == "./runtime/data/ecp.db" {
		cfg.Store.DSN = filepath.Join(cfg.Server.DataDir, "ecp.db")
	}
	cfg.Server.WebDir = "" // 始终用内嵌前端

	logFilePath := filepath.Join(cfg.Server.DataDir, "desktop.log")
	log := newFileLogger(logFilePath)

	if err := cfg.EnsureDirs(); err != nil {
		fmt.Fprintf(os.Stderr, "初始化目录失败: %v\n", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.Store.Driver, cfg.DSN(), log)
	if err != nil {
		log.Error("数据库打开失败", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// 首次启动：创建超管账户（口令仅显示一次）
	if err := bootstrapAdmin(st, log); err != nil {
		log.Error("初始化超管失败", "err", err)
	}

	// 内置 CA（复用：desktop 与 server 共用同一数据形态）
	certsDir := filepath.Join(cfg.Server.DataDir, "certs")
	caInstance, err := ca.LoadOrCreate(certsDir)
	if err != nil {
		log.Error("初始化 CA 失败", "err", err)
		os.Exit(1)
	}

	// gRPC：节点回连入口，保持 0.0.0.0:7443 默认
	grpcSrv := grpcserver.New(st, caInstance, cfg, log)
	go func() {
		if err := grpcSrv.Serve(cfg.Server.GRPCListen); err != nil {
			log.Error("gRPC 服务异常", "err", err)
		}
	}()

	// 控制台 engine（WebView2 / 浏览器双形态共用一套）
	disp := command.New(grpcSrv.Sessions(), st, log.Info)
	advIP := detectAdvertiseIP(cfg)
	engine := api.New(st, grpcSrv.Sessions(), disp, grpcSrv, log.Info, cfg.Server.DataDir, advIP, httpPort(cfg))
	engine.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		ext := filepath.Ext(p)
		// SPA 路由（无扩展名）：回退首页，由前端 history 路由接管
		if ext == "" || ext == ".html" {
			data, err := web.FS().ReadFile("spa/index.html")
			if err != nil {
				c.Status(http.StatusInternalServerError)
				return
			}
			c.Data(http.StatusOK, "text/html; charset=utf-8", data)
			return
		}
		// 静态资源：存在则按扩展名返回正确 MIME；不存在 404
		data, err := web.FS().ReadFile("spa" + p)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		ct := mime.TypeByExtension(ext)
		if ct == "" {
			ct = "application/octet-stream"
		}
		c.Data(http.StatusOK, ct, data)
	})

	// HTTP 回环监听：随机端口，ListenAndServe 无需 TLS
	srv := &http.Server{Handler: engine}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Error("回环监听失败", "err", err)
		os.Exit(1)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		log.Info("控制台监听(桌面)", "addr", fmt.Sprintf("http://127.0.0.1:%d", port))
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP 服务异常", "err", err)
		}
	}()

	// 窗口生命周期：关闭窗口 → 整体退出
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	exited := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(exited)
	}()

	log.Info("创建 WebView2 窗口", "url", fmt.Sprintf("http://127.0.0.1:%d", port))
	w := webview2.NewWithOptions(webview2.WebViewOptions{Debug: os.Getenv("ECP_DEBUG") != ""})
	if w == nil {
		log.Error("WebView2 初始化失败（缺少运行时？安装 Edge WebView2 后重试）")
		os.Exit(1)
	}
	log.Info("WebView2 窗口已创建")
	defer w.Destroy()
	w.SetTitle("ECP 边缘节点控制台 v" + version)
	w.SetSize(1280, 800, webview2.HintNone)
	w.Navigate(fmt.Sprintf("http://127.0.0.1:%d", port))
	log.Info("开始运行窗口消息循环")

	// 窗口关闭（webview2.Run 返回）或收到信号 → 关闭服务
	w.Run()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Info("控制台已退出")
}

// bootstrapAdmin 与 server 入口一致：库内无用户时创建超管并打印口令。
func bootstrapAdmin(st *store.Store, log logger) error {
	n, err := st.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	pw := auth.GenerateInitialPassword()
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	u := &model.User{Username: "admin", PasswordHash: hash, Role: model.RoleAdmin}
	if err := st.CreateUser(u); err != nil {
		return err
	}
	log.Warn("已创建初始超管账户（请尽快修改口令）", "username", "admin", "password", pw)
	return nil
}

type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// exeDir 返回可执行文件所在目录（便携 APP 的数据落盘位置）。
func exeDir() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(p)
}

// detectAdvertiseIP 复用 server 的 OTA 通告探测：
// 显式配置优先，否则 tailscale status 抓本机 IPv4。
func detectAdvertiseIP(cfg *config.Config) string {
	if len(cfg.Advertise.Endpoints) > 0 {
		if host, _, err := net.SplitHostPort(cfg.Advertise.Endpoints[0]); err == nil {
			return host
		}
	}
	if out, err := exec.Command("tailscale", "status").Output(); err == nil {
		re := regexp.MustCompile(`(\d+\.\d+\.\d+\.\d+)\s+\S+\s+\S+\s+windows`)
		if m := re.FindSubmatch(out); len(m) > 1 {
			return string(m[1])
		}
	}
	return ""
}

// httpPort 返回控制台端口字符串（桌面随机端口在 listen 后回填）。
func httpPort(cfg *config.Config) string {
	if _, port, err := net.SplitHostPort(cfg.Server.Listen); err == nil && port != "" {
		return port
	}
	return "8443"
}

// newFileLogger 输出到 data/desktop.log（桌面 APP 无控制台窗口，日志落盘可查）。
func newFileLogger(path string) *slog.Logger {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		// 文件打不开就退回 stdout（至少不静默失败）
		return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	w := io.MultiWriter(os.Stdout, f)
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}