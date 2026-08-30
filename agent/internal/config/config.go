// Package config 负责 Agent 侧的配置。
//
// Agent 是丢到边缘节点上就跑的静态二进制，配置遵循两条原则：
//  1. 路径严格遵守 A6 完全隔离：数据 /opt/ecp-agent，配置 /etc/ecp
//  2. 控制面端点只是"种子"——真正的地址由控制面下发后持久化到
//     known_endpoints.json，控制面换机器也不会失联
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// 隔离路径常量。改这里之前先想清楚：这是需求 A6 的硬约束。
const (
	DefaultDataDir   = "/opt/ecp-agent"
	DefaultConfigDir = "/etc/ecp"
)

// Config 是 Agent 的完整配置。
type Config struct {
	Agent        AgentConfig        `yaml:"agent"`
	Registration RegistrationConfig `yaml:"registration"`
	ControlPlane ControlPlaneConfig `yaml:"control_plane"`
	Relay        RelayConfig        `yaml:"relay"`
	Telemetry    TelemetryConfig    `yaml:"telemetry"`
	Cache        CacheConfig        `yaml:"cache"`
	Alert        AlertConfig        `yaml:"alert"`
	Docker       DockerConfig       `yaml:"docker"`
}

// AgentConfig 是 Agent 自身的基础配置。
type AgentConfig struct {
	NodeID    string `yaml:"node_id"`
	DataDir   string `yaml:"data_dir"`
	ConfigDir string `yaml:"config_dir"`
	LogLevel  string `yaml:"log_level"`
}

// RegistrationConfig 是注册凭据配置。
//
// 注册 Key 可以从文件读，也可以直接写在配置里。
// 推荐用文件——重装系统时只要保住这个文件就能自动重连受控，
// 这正是老板要的"上线即控"。
type RegistrationConfig struct {
	Key     string `yaml:"key"`
	KeyFile string `yaml:"key_file"`
}

// ControlPlaneConfig 是控制面连接配置。
type ControlPlaneConfig struct {
	// 端点种子，只是 bootstrap 用的起点
	Endpoints []string `yaml:"endpoints"`
	// 控制面下发后持久化的已知地址文件
	KnownEndpointsFile string `yaml:"known_endpoints_file"`
	// CA 证书路径，用于校验控制面身份
	CACert string `yaml:"ca_cert"`
	// 客户端证书与私钥，注册成功后写入
	ClientCert string `yaml:"client_cert"`
	ClientKey  string `yaml:"client_key"`
}

// TelemetryConfig 是状态采集配置。
type TelemetryConfig struct {
	Interval time.Duration `yaml:"interval"`
}

// RelayConfig 是紧急通道（Cloudflare Worker 中转）配置。
//
// Tailscale 主通道不可达时，Agent 与 GUI 两端出站 wss 连 Worker，
// Worker + Durable Object 按 node_id 分房间双向转发，实现 NAT 穿透。
// 流量为标准 TLS/WSS，自有域名，合规可审计。
type RelayConfig struct {
	Enabled bool   `yaml:"enabled"`
	// Worker 的 WSS 入口，如 wss://relay.example.com/agent
	URL string `yaml:"url"`
	// 鉴权令牌，与 Worker 侧 AGENT_TOKEN 一致；优先读环境变量 ECP_RELAY_TOKEN
	Token string `yaml:"token"`
}

// CacheConfig 是本地缓存配置。控制面离线时数据先落这里，上线后补传。
type CacheConfig struct {
	Path      string        `yaml:"path"`
	Retention time.Duration `yaml:"retention"`
}

// AlertConfig 是告警配置。规则在节点本地执行，不依赖控制面在线。
type AlertConfig struct {
	RulesFile     string `yaml:"rules_file"`
	FeishuWebhook string `yaml:"feishu_webhook"`
}

// DockerConfig 是容器管理配置。
//
// ManagedLabel 是隔离红线的实现基础：Agent 只对带这个 label 的容器
// 执行写操作，节点上的业务容器（如 wxedge）永远碰不到。
type DockerConfig struct {
	ManagedLabel string `yaml:"managed_label"`
}

// Default 返回默认配置。
func Default() *Config {
	return &Config{
		Agent: AgentConfig{
			DataDir:   DefaultDataDir,
			ConfigDir: DefaultConfigDir,
			LogLevel:  "info",
		},
		Registration: RegistrationConfig{
			KeyFile: filepath.Join(DefaultConfigDir, "registration.key"),
		},
		ControlPlane: ControlPlaneConfig{
			KnownEndpointsFile: filepath.Join(DefaultConfigDir, "known_endpoints.json"),
			CACert:             filepath.Join(DefaultConfigDir, "ca.crt"),
			ClientCert:         filepath.Join(DefaultConfigDir, "client.crt"),
			ClientKey:          filepath.Join(DefaultConfigDir, "client.key"),
		},
		Telemetry: TelemetryConfig{
			Interval: 30 * time.Second,
		},
		Cache: CacheConfig{
			Path:      filepath.Join(DefaultDataDir, "cache.db"),
			Retention: 7 * 24 * time.Hour,
		},
		Alert: AlertConfig{
			RulesFile: filepath.Join(DefaultConfigDir, "alert-rules.yaml"),
		},
		Docker: DockerConfig{
			ManagedLabel: "ecp.managed",
		},
	}
}

// Load 加载配置，path 为空返回默认值。
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
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	// 环境变量覆盖：飞书 Webhook 属于凭据，优先走 env，避免写进配置文件/仓库。
	if v := os.Getenv("ECP_FEISHU_WEBHOOK"); v != "" {
		cfg.Alert.FeishuWebhook = v
	}
	// 紧急通道令牌同理，走 env 优先。
	if v := os.Getenv("ECP_RELAY_TOKEN"); v != "" {
		cfg.Relay.Token = v
	}
	return cfg, nil
}

// EnsureDirs 创建运行目录。
// 注意：Agent 默认以普通用户运行，/opt/ecp-agent 与 /etc/ecp
// 需要由安装脚本预先创建并授权，这里只做兜底。
func (c *Config) EnsureDirs() error {
	for _, d := range []string{c.Agent.DataDir, c.Agent.ConfigDir} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("创建目录 %s 失败（可能需要提权）: %w", d, err)
		}
	}
	return nil
}

// RegistrationKeyValue 返回注册 Key：优先用配置文件里的值，
// 否则从 key_file 读取并去掉首尾空白。
func (c *Config) RegistrationKeyValue() (string, error) {
	if c.Registration.Key != "" {
		return c.Registration.Key, nil
	}
	if c.Registration.KeyFile == "" {
		return "", fmt.Errorf("未配置注册 Key，也没有指定 key_file")
	}
	data, err := os.ReadFile(c.Registration.KeyFile)
	if err != nil {
		return "", fmt.Errorf("读取注册 Key 文件失败: %w", err)
	}
	return string(data), nil
}
