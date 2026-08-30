// Command agent 是运行在边缘节点上的常驻守护进程。
//
// 设计约束：
//   - 单文件静态二进制，同时构建 linux/amd64 与 linux/arm64
//   - 默认以普通用户运行，需提权的操作降级为生成脚本供人工执行
//   - 控制面离线期间按最后下发的配置自治运行，数据本地缓存后补传
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"ecp.dev/ecp/agent/internal/cache"
	"ecp.dev/ecp/agent/internal/caps"
	"ecp.dev/ecp/agent/internal/config"
	"ecp.dev/ecp/agent/internal/relay"
	"ecp.dev/ecp/agent/internal/transport"
	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// 由 -ldflags "-X main.version=..." 注入
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	switch os.Args[1] {
	case "version":
		cmdVersion()
	case "caps":
		cmdCaps()
	case "run":
		cmdRun(os.Args[2:])
	default:
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Printf("ecp-agent %s (%s/%s)\n\n", version, runtime.GOOS, runtime.GOARCH)
	fmt.Println("用法:")
	fmt.Println("  ecp-agent version                 打印版本与协议契约")
	fmt.Println("  ecp-agent caps [-c <config>]      探测并打印本节点的能力（排查权限问题用）")
	fmt.Println("  ecp-agent run   [-c <config>]     常驻运行（注册、保活、采集、执行指令）")
	fmt.Println("")
	fmt.Println("全局选项:")
	fmt.Println("  -c <path>   配置文件路径，默认 /etc/ecp/agent.yaml")
}

func cmdVersion() {
	fmt.Printf("ecp-agent %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("proto contract: %s\n", ecpv1.File_ecp_v1_ecp_proto.Path())
}

// cmdCaps 探测并打印能力。
//
// 这个子命令是给现场运维用的：节点上某个功能点不动时，
// 先跑它看是"没装工具"还是"没权限"，不用去猜。
func cmdCaps() {
	cfg, err := config.Load(configPath(os.Args[2:]))
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置加载失败: %v\n", err)
		os.Exit(1)
	}

	s := caps.Probe()

	fmt.Printf("节点能力探测 (%s@%s, uid=%d)\n\n", s.RunAsUser, runtime.GOARCH, s.RunAsUID)
	rows := [][3]string{
		{"系统状态采集", boolMark(s.CanReadSystemStats), "读 /proc"},
		{"终端会话", boolMark(s.CanTerminal), "pty"},
		{"文件管理", boolMark(s.CanManageFiles), "受目标路径权限约束"},
		{"网络配置读取", boolMark(s.CanReadNetConfig), "resolv.conf / ip"},
		{"Docker 只读", boolMark(s.CanReadDocker), "unix socket"},
		{"Docker 写操作", boolMark(s.CanWriteDocker), "unix socket"},
		{"Tailscale 纳管", boolMark(s.CanManageTailscale), "免密执行 tailscale status"},
		{"网络控制(iptables)", boolMark(s.CanManageNetwork), "需 root 才能写"},
		{"systemd 管理", boolMark(s.CanManageSystemd), "用户级实例"},
		{"Agent 自升级", boolMark(s.CanSelfUpgrade), "安装目录可写"},
	}
	for _, r := range rows {
		fmt.Printf("  %s  %-20s %s\n", r[1], r[0], r[2])
	}

	if len(s.MissingTools) > 0 {
		fmt.Printf("\n缺失的外部工具: %s\n", strings.Join(s.MissingTools, ", "))
	}
	fmt.Printf("\n容器隔离标签: %s（只对该标签的容器执行写操作）\n", cfg.Docker.ManagedLabel)

	// 输出一份机器可读的 JSON，方便自动化采集
	if len(os.Args) > 2 && os.Args[len(os.Args)-1] == "--json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(s)
	}
}

// cmdRun 常驻运行。当前阶段先做初始化与能力上报，
// 注册与指令通道在后续任务中接入。
func cmdRun(args []string) {
	cfg, err := config.Load(configPath(args))
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置加载失败: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.EnsureDirs(); err != nil {
		fmt.Fprintf(os.Stderr, "目录初始化失败: %v\n", err)
		os.Exit(1)
	}

	c, err := cache.Open(cachePath(cfg))
	if err != nil {
		fmt.Fprintf(os.Stderr, "本地缓存打开失败: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	s := caps.Probe()
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	fmt.Printf("ecp-agent %s 启动\n", version)
	fmt.Printf("  节点: %s (%s/%s)\n", cfg.Agent.NodeID, runtime.GOOS, runtime.GOARCH)
	fmt.Printf("  数据目录: %s\n", cfg.Agent.DataDir)
	fmt.Printf("  配置目录: %s\n", cfg.Agent.ConfigDir)
	fmt.Printf("  缓存: %s\n", cachePath(cfg))
	fmt.Printf("  控制面端点种子: %v\n", cfg.ControlPlane.Endpoints)
	fmt.Printf("  可用能力: docker=%v tailscale=%v systemd=%v\n",
		s.CanReadDocker, s.CanManageTailscale, s.CanManageSystemd)
	fmt.Println("开始连接控制面（自动注册 + 心跳）...")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tr := transport.New(cfg, log, c)
	// 紧急通道（Cloudflare Worker 中转）：配置启用时独立协程常驻，
	// 与主通道并存；tailnet 不可达时 GUI 经它完成紧急控制。
	if cfg.Relay.Enabled {
		relayC := relay.New(cfg, log, tr.Exec(), s, c)
		go func() {
			if err := relayC.Run(ctx); err != nil && err != context.Canceled {
				log.Warn("紧急通道退出", "err", err)
			}
		}()
	}
	if err := tr.Run(ctx); err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "运行异常: %v\n", err)
		os.Exit(1)
	}
}

// configPath 从命令行参数取配置路径，未提供时用默认位置。
func configPath(args []string) string {
	for i, a := range args {
		if a == "-c" && i+1 < len(args) {
			return args[i+1]
		}
	}
	if p := os.Getenv("ECP_AGENT_CONFIG"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return ""
	}
	return filepath.Join(config.DefaultConfigDir, "agent.yaml")
}

// cachePath 解析缓存数据库路径。
func cachePath(cfg *config.Config) string {
	if cfg.Cache.Path != "" {
		return cfg.Cache.Path
	}
	return filepath.Join(cfg.Agent.DataDir, "cache.db")
}

func boolMark(b bool) string {
	if b {
		return "[可用]"
	}
	return "[需提权]"
}
