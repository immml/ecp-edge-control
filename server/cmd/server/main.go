// Command server 是边缘计算节点控制平台的控制面进程。
//
// 设计约束：控制面按需上线，不保证 7×24 在线。
// 因此它必须做到启动即可用、退出不丢状态——节点侧持有全部自治能力。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/server/internal/ca"
	"ecp.dev/ecp/server/internal/config"
	"ecp.dev/ecp/server/internal/grpcserver"
	"ecp.dev/ecp/server/internal/logx"
	"ecp.dev/ecp/server/internal/store"
)

// 由 -ldflags "-X main.version=..." 注入
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("ecp-server %s\n", version)
		fmt.Printf("proto contract: %s\n", ecpv1.File_ecp_v1_ecp_proto.Path())
		return
	}

	// init 模式：建库建表后即退出，供部署脚本与首次运行使用
	mode := "serve"
	if len(os.Args) > 1 && os.Args[1] == "init" {
		mode = "init"
	}

	cfgPath := os.Getenv("ECP_CONFIG")
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

	// 内置 CA：缺则生成，存于 runtime/certs；控制面重启复用，避免节点被迫重连
	certsDir := filepath.Join(cfg.Server.DataDir, "certs")
	caInstance, err := ca.LoadOrCreate(certsDir)
	if err != nil {
		log.Error("初始化 CA 失败", "err",  err)
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

	printBootstrapInfo(log, cfg, st)

	if mode == "init" {
		log.Info("init 模式：库表已就绪，退出",
			"tables", len(st.Tables()),
		)
		return
	}

	// 启动 gRPC 接入层（节点注册与指令通道）
	grpcSrv := grpcserver.New(st, caInstance, cfg, log)
	go func() {
		if err := grpcSrv.Serve(cfg.Server.GRPCListen); err != nil {
			log.Error("gRPC 服务异常", "err", err)
		}
	}()

	// 优雅退出：控制面是按需启动的终端，退出时必须干净，
	// 否则 SQLite 可能留下锁文件，下次启动报错。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	log.Info("收到退出信号，正在关闭")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = shutdownCtx

	if err := st.Close(); err != nil {
		log.Warn("关闭数据库时出错", "err", err)
	}
	log.Info("控制面已退出")
}

// printBootstrapInfo 在首次启动时把必要信息明确打给用户。
// 控制面是个人管理终端，用户不该去翻文档才知道怎么登录。
func printBootstrapInfo(log logger, cfg *config.Config, st *store.Store) {
	var count int64
	if err := st.DB().Model(&struct{}{}).Count(&count).Error; err == nil {
		_ = count
	}

	log.Info("数据目录已就绪", "dir", absPath(cfg.Server.DataDir))
	log.Info("监听地址",
		"console", fmt.Sprintf("https://%s", cfg.Server.Listen),
		"note", "自签证书，浏览器首次访问需手动信任",
	)
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
