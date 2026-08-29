// Command server 是边缘计算节点控制平台的控制面进程。
//
// 设计约束：控制面按需上线，不保证 7×24 在线。
// 因此它必须做到启动即可用、退出不丢状态——节点侧持有全部自治能力。
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/server/internal/api"
	"ecp.dev/ecp/server/internal/auth"
	"ecp.dev/ecp/server/internal/ca"
	"ecp.dev/ecp/server/internal/command"
	"ecp.dev/ecp/server/internal/config"
	"ecp.dev/ecp/server/internal/grpcserver"
	"ecp.dev/ecp/server/internal/logx"
	"ecp.dev/ecp/server/internal/store"
	"ecp.dev/ecp/server/internal/store/model"
	"ecp.dev/ecp/server/internal/web"
)

// 由 -ldflags "-X main.version=..." 注入
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("ecp-server %s\n", version)
		fmt.Printf("proto contract: %s\n", ecpv1.File_ecp_v1_ecp_proto.Path())
		return
	}

	mode := "serve"
	cfgPath := os.Getenv("ECP_CONFIG")
	if len(os.Args) > 1 && os.Args[1] == "init" {
		mode = "init"
	}
	if cfgPath == "" && len(os.Args) > 2 && os.Args[1] == "-c" {
		cfgPath = os.Args[2]
	}
	if cfgPath == "" && len(os.Args) > 3 && os.Args[1] == "init" && os.Args[2] == "-c" {
		cfgPath = os.Args[3]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置加载失败: %v\n", err)
		os.Exit(1)
	}

	log := logx.New(os.Getenv("ECP_LOG_LEVEL"), os.Getenv("ECP_LOG_FORMAT"))

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

	if len(os.Args) > 1 && os.Args[1] == "keygen" {
		// 生成一份节点注册 Key：明文打印供 agent 配置，仅入库哈希。
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			fmt.Fprintf(os.Stderr, "生成随机 Key 失败: %v\n", err)
			os.Exit(1)
		}
		plain := hex.EncodeToString(buf)
		sum := sha256.Sum256([]byte(plain))
		keyHash := hex.EncodeToString(sum[:])
		label := "orangepi-deploy"
		if len(os.Args) > 2 && os.Args[2] != "" {
			label = os.Args[2]
		}
		if err := st.CreateKey(&model.RegistrationKey{KeyHash: keyHash, Label: label}); err != nil {
			fmt.Fprintf(os.Stderr, "创建注册 Key 失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("REGISTRATION_KEY=%s\n", plain)
		fmt.Printf("KEY_HASH=%s\n", keyHash)
		fmt.Printf("提示：把 REGISTRATION_KEY 写入 agent 的 registration.key（或 registration.key_file）\n")
		return
	}

	// 首次启动：创建超管账户（口令仅显示一次）
	if err := bootstrapAdmin(st, log); err != nil {
		log.Error("初始化超管失败", "err", err)
	}

	// 内置 CA：缺则生成，存于 runtime/certs；控制面重启复用，避免节点被迫重连
	certsDir := filepath.Join(cfg.Server.DataDir, "certs")
	caInstance, err := ca.LoadOrCreate(certsDir)
	if err != nil {
		log.Error("初始化 CA 失败", "err", err)
		os.Exit(1)
	}
	log.Info("CA 就绪", "certs_dir", absPath(certsDir))

	log.Info("控制面启动",
		"version", version,
		"https", cfg.Server.Listen,
		"grpc", cfg.Server.GRPCListen,
		"db", cfg.DSN(),
		"tls_mode", cfg.TLS.Mode,
	)

	if mode == "init" {
		log.Info("init 模式：库表已就绪，退出", "tables", len(st.Tables()))
		return
	}

	// 启动 gRPC 接入层（节点注册与指令通道）
	grpcSrv := grpcserver.New(st, caInstance, cfg, log)
	go func() {
		if err := grpcSrv.Serve(cfg.Server.GRPCListen); err != nil {
			log.Error("gRPC 服务异常", "err", err)
		}
	}()

	// REST + 控制台 HTTPS
	disp := command.New(grpcSrv.Sessions(), st, log.Info)
	engine := api.New(st, grpcSrv.Sessions(), disp, log.Info)
	engine.NoRoute(func(c *gin.Context) {
		// 静态资源：内置前端（embed 目录名 spa），未命中则回退首页（SPA history 模式）
		data, err := web.FS().ReadFile("spa" + c.Request.URL.Path)
		ct := mime.TypeByExtension(filepath.Ext(c.Request.URL.Path))
		if err != nil {
			// 路径未命中 → 回退首页（SPA 路由由前端处理）
			data, _ = web.FS().ReadFile("spa/index.html")
			ct = "text/html; charset=utf-8"
		}
		if ct == "" {
			ct = "application/octet-stream"
		}
		c.Data(http.StatusOK, ct, data)
	})

	srv := &http.Server{
		Addr:    cfg.Server.Listen,
		Handler: engine,
	}
	if cert, key, err := caInstance.SignServerCert([]string{"localhost", "ecp-control"}, 8760*time.Hour); err == nil {
		if tlsCert, err := tls.X509KeyPair(cert, key); err == nil {
			srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
		}
	}
	go func() {
		log.Info("控制台监听", "addr", "https://"+cfg.Server.Listen)
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP 服务异常", "err", err)
		}
	}()

	// 优雅退出
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Info("收到退出信号，正在关闭")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if err := st.Close(); err != nil {
		log.Warn("关闭数据库时出错", "err", err)
	}
	log.Info("控制面已退出")
}

// bootstrapAdmin 在库内无用户时创建默认超管，口令打印到日志（仅一次）。
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

func absPath(p string) string {
	if p == "" {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
