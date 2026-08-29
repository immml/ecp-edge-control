// Package config 负责控制面的配置加载。
//
// 设计原则：控制面是"按需启动的个人管理终端"，所以每个字段都必须有合理默认值，
// 空配置文件也能直接跑起来，不给用户制造启动障碍。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是控制面的完整配置。
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	TLS       TLSConfig       `yaml:"tls"`
	Auth      AuthConfig      `yaml:"auth"`
	Store     StoreConfig     `yaml:"store"`
	Discovery DiscoveryConfig `yaml:"discovery"`
	Advertise AdvertiseConfig `yaml:"advertise"`
	FRP       FRPConfig       `yaml:"frp"`
}

// ServerConfig 是进程监听与数据目录配置。
type ServerConfig struct {
	// HTTPS 监听地址，托管控制台静态资源与 REST API
	Listen string `yaml:"listen"`
	// gRPC 监听地址，节点长连接入口，强制 mTLS
	GRPCListen string `yaml:"grpc_listen"`
	// 数据目录，存放 SQLite 数据库、证书、前端构建产物
	DataDir string `yaml:"data_dir"`
	// 前端静态资源目录，为空则使用内嵌资源
	WebDir string `yaml:"web_dir"`
}

// TLSConfig 是证书配置。
//
// 三种模式：
//   - selfsigned：内置 CA 自签，零依赖，浏览器首次需手动信任（老板选定）
//   - tailscale：通过 MagicDNS 域名申请可信证书，体验更好，依赖 Tailscale 在线
//   - custom：使用用户自己的证书文件
type TLSConfig struct {
	Mode     string `yaml:"mode"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// tailscale 模式下的主机名，用于向 Tailscale 申请证书
	Hostname string `yaml:"hostname"`
}

// AuthConfig 是认证配置。
type AuthConfig struct {
	// JWT 签名密钥。为空时进程启动会生成随机密钥，
	// 代价是重启后所有登录态失效——对个人管理终端是可接受的。
	JWTSecret string        `yaml:"jwt_secret"`
	TokenTTL  time.Duration `yaml:"token_ttl"`
	// 首次启动时创建的超管账号
	BootstrapAdmin string `yaml:"bootstrap_admin"`
	// 为空则首次启动时生成随机密码并打印到日志
	BootstrapPassword string `yaml:"bootstrap_password"`
}

// StoreConfig 是存储配置。驱动目前只有 sqlite，接口已为 PostgreSQL 预留。
type StoreConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

// DiscoveryConfig 是节点发现配置。
//
// 砍掉 Cloudflare Worker 之后，发现机制改为三层：
//  1. 静态端点种子（agent.yaml 里配置，或通过安装脚本注入）
//  2. 控制面侧主动发现（Tailscale LocalAPI / mDNS）
//  3. 控制面下发 advertise_endpoints，让节点记住地址实现自愈
type DiscoveryConfig struct {
	Tailscale TailscaleDiscovery `yaml:"tailscale"`
	MDNS      MDNSDiscovery      `yaml:"mdns"`
}

// TailscaleDiscovery 是通过 Tailscale 发现对等节点的配置。
type TailscaleDiscovery struct {
	Enabled bool `yaml:"enabled"`
	// tailscale 可执行文件路径。Windows 上不在 PATH，需要显式指定；
	// Linux 留空即可走 PATH。
	CLIPath string `yaml:"cli_path"`
}

// MDNSDiscovery 是通过 mDNS 发现同局域网节点的配置。
type MDNSDiscovery struct {
	Enabled bool   `yaml:"enabled"`
	Service string `yaml:"service"`
}

// AdvertiseConfig 是控制面向节点通告的自身地址。
type AdvertiseConfig struct {
	// 显式指定的地址列表，优先级最高
	Endpoints []string `yaml:"endpoints"`
	// 为空时自动取 Tailscale IP + gRPC 端口
	AutoDetect bool `yaml:"auto_detect"`
}

// FRPConfig 是 FRP 备用通道配置。
//
// 关键：frpc 跑在控制机侧，Agent 侧零组件。默认关闭。
type FRPConfig struct {
	Enabled    bool   `yaml:"enabled"`
	ServerAddr string `yaml:"server_addr"`
	ServerPort int    `yaml:"server_port"`
	Token      string `yaml:"token"`
}

// Default 返回一份可直接使用的默认配置。
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Listen:     "127.0.0.1:8443",
			GRPCListen: "0.0.0.0:7443",
			DataDir:    "./runtime/data",
		},
		TLS: TLSConfig{
			Mode: "selfsigned",
		},
		Auth: AuthConfig{
			TokenTTL:       8 * time.Hour,
			BootstrapAdmin: "admin",
		},
		Store: StoreConfig{
			Driver: "sqlite",
			DSN:    "./runtime/data/ecp.db",
		},
		Discovery: DiscoveryConfig{
			Tailscale: TailscaleDiscovery{Enabled: true},
			MDNS:      MDNSDiscovery{Enabled: true, Service: "_ecp._tcp"},
		},
		Advertise: AdvertiseConfig{
			AutoDetect: true,
		},
		FRP: FRPConfig{
			Enabled: false,
		},
	}
}

// Load 从文件加载配置，用文件中的非零值覆盖默认值。
// path 为空时返回纯默认配置。
func Load(path string) (*Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	switch c.TLS.Mode {
	case "selfsigned", "tailscale", "custom":
	default:
		return fmt.Errorf("tls.mode 非法: %q（可选 selfsigned / tailscale / custom）", c.TLS.Mode)
	}
	if c.TLS.Mode == "custom" && (c.TLS.CertFile == "" || c.TLS.KeyFile == "") {
		return fmt.Errorf("tls.mode=custom 时必须提供 cert_file 与 key_file")
	}
	if c.Store.Driver != "sqlite" {
		return fmt.Errorf("store.driver 目前只支持 sqlite，收到 %q", c.Store.Driver)
	}
	return nil
}

// EnsureDirs 创建运行所需的目录。控制面应该做到"启动即用"，
// 不要让用户手工 mkdir。
func (c *Config) EnsureDirs() error {
	dirs := []string{
		c.Server.DataDir,
		filepath.Join(c.Server.DataDir, "certs"),
	}
	if c.Store.Driver == "sqlite" && c.Store.DSN != "" {
		dirs = append(dirs, filepath.Dir(c.resolveDSN()))
	}
	for _, d := range dirs {
		if d == "" || d == "." {
			continue
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", d, err)
		}
	}
	return nil
}

// resolveDSN 把相对路径的 DSN 解析到数据目录下。
func (c *Config) resolveDSN() string {
	if filepath.IsAbs(c.Store.DSN) {
		return c.Store.DSN
	}
	return filepath.Join(c.Server.DataDir, filepath.Base(c.Store.DSN))
}

// DSN 返回解析后的数据库连接串。
func (c *Config) DSN() string { return c.resolveDSN() }
