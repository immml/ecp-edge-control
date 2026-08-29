// Package model 定义控制面的持久化实体。
//
// 两条不可动摇的约束：
//  1. 审计日志 append-only——只允许插入，不提供更新与删除方法
//  2. 注册 Key 只存哈希——明文仅在签发那一刻展示一次，之后无法找回
//
// 这两条不是"安全最佳实践"，是本项目的合规底线。
package model

import "time"

// 节点状态
const (
	StatusUnknown = "unknown"
	StatusOnline  = "online"
	StatusOffline = "offline"
)

// RBAC 三级角色
const (
	RoleAdmin    = "admin"    // 超管：管用户与节点注册
	RoleOperator = "operator" // 运维：SSH、文件、网络、部署
	RoleViewer   = "viewer"   // 只读：看状态与日志
)

// Node 是节点主表。
type Node struct {
	ID           string `gorm:"primaryKey;size:64" json:"id"`
	Hostname     string `gorm:"size:255;index" json:"hostname"`
	Arch         string `gorm:"size:32" json:"arch"`
	OS           string `gorm:"size:64" json:"os"`
	OSVersion    string `gorm:"size:64" json:"os_version"`
	Kernel       string `gorm:"size:128" json:"kernel"`
	AgentVersion string `gorm:"size:64" json:"agent_version"`
	TailscaleIP  string `gorm:"size:64;index" json:"tailscale_ip"`
	Status       string `gorm:"size:16;default:unknown;index" json:"status"`
	// CapabilitiesJSON 存放 Agent 上报的能力探测结果（CapabilityReport 的 JSON 序列化）
	CapabilitiesJSON string    `gorm:"type:text" json:"capabilities"`
	RegisteredAt     time.Time `json:"registered_at"`
	LastSeenAt       time.Time `gorm:"index" json:"last_seen_at"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// NodeFingerprint 记录注册 Key 与硬件指纹的绑定关系。
// 指纹变更是需要人工复核的安全事件，不是普通的字段更新。
type NodeFingerprint struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	NodeID      string    `gorm:"size:64;uniqueIndex" json:"node_id"`
	Fingerprint string    `gorm:"size:128;index" json:"fingerprint"`
	BoundAt     time.Time `json:"bound_at"`
}

// RegistrationKey 是节点注册凭据。只存哈希。
type RegistrationKey struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	KeyHash     string     `gorm:"size:128;uniqueIndex;not null" json:"key_hash"`
	Label       string     `gorm:"size:255" json:"label"`
	CreatedBy   string     `gorm:"size:64" json:"created_by"`
	BoundNodeID string     `gorm:"size:64;index" json:"bound_node_id"`
	ExpiresAt   *time.Time `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// NodeCredential 是签发给节点的客户端证书台账。
type NodeCredential struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	NodeID     string     `gorm:"size:64;index" json:"node_id"`
	CertSerial string     `gorm:"size:64;uniqueIndex" json:"cert_serial"`
	IssuedAt   time.Time  `json:"issued_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// User 是控制台用户。
type User struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Username     string     `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string     `gorm:"size:255;not null" json:"-"`
	Role         string     `gorm:"size:16;default:viewer;not null" json:"role"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at"`
}

// AuditLog 是审计日志。append-only，不提供更新与删除。
//
// 每一次下发操作都必须留痕：谁、何时、对哪个节点、做了什么、结果如何。
type AuditLog struct {
	ID       uint      `gorm:"primaryKey" json:"id"`
	Ts       time.Time `gorm:"index" json:"ts"`
	UserID   uint      `gorm:"index" json:"user_id"`
	Username string    `gorm:"size:64;index" json:"username"`
	NodeID   string    `gorm:"size:64;index" json:"node_id"`
	Action   string    `gorm:"size:64;index" json:"action"`
	Params   string    `gorm:"type:text" json:"params"`
	Result   string    `gorm:"size:32;index" json:"result"`
	Detail   string    `gorm:"type:text" json:"detail"`
	TraceID  string    `gorm:"size:64;index" json:"trace_id"`
}

// ConfigVersion 是配置版本，支持回滚。
type ConfigVersion struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	NodeID    string     `gorm:"size:64;index" json:"node_id"`
	Key       string     `gorm:"size:128;index" json:"key"`
	Value     string     `gorm:"type:text" json:"value"`
	Version   int        `json:"version"`
	Checksum  string     `gorm:"size:128" json:"checksum"`
	AppliedAt *time.Time `json:"applied_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// AlertRule 是告警规则。规则本身下发到节点本地执行，
// 控制面离线时节点照样能告警。
type AlertRule struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	NodeID    string    `gorm:"size:64;index" json:"node_id"`
	Name      string    `gorm:"size:128" json:"name"`
	RuleYAML  string    `gorm:"type:text" json:"rule_yaml"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Command 是指令台账。
type Command struct {
	TraceID         string     `gorm:"primaryKey;size:64" json:"trace_id"`
	NodeID          string     `gorm:"size:64;index" json:"node_id"`
	UserID          uint       `json:"user_id"`
	Type            string     `gorm:"size:64" json:"type"`
	Params          string     `gorm:"type:text" json:"params"`
	Status          string     `gorm:"size:32;index" json:"status"`
	Output          string     `gorm:"type:text" json:"output"`
	PrivilegeScript string     `gorm:"type:text" json:"privilege_script"`
	CreatedAt       time.Time  `gorm:"index" json:"created_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

// PanelTarget 记录节点上 1Panel 的地址配置。
//
// 只保存"怎么连"，绝不保存 1Panel 的账号密码——
// 登录凭据由用户本人在 1Panel 页面上输入，ECP 不代持、不绕过。
type PanelTarget struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	NodeID       string    `gorm:"size:64;uniqueIndex" json:"node_id"`
	ListenAddr   string    `gorm:"size:128" json:"listen_addr"`
	EntrancePath string    `gorm:"size:255" json:"entrance_path"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// NodeAddress 记录节点的已知地址，替代被砍掉的 Worker KV 映射表。
//
// Source 区分来源：seed（配置种子）、advertise（控制面下发）、
// discovery（Tailscale/mDNS 主动发现）。
type NodeAddress struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	NodeID      string    `gorm:"size:64;index" json:"node_id"`
	TailscaleIP string    `gorm:"size:64" json:"tailscale_ip"`
	GRPCPort    int       `json:"grpc_port"`
	Source      string    `gorm:"size:32" json:"source"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// DiscoveredPeer 记录发现但尚未纳管的对等节点。
//
// 合规红线：只记录，绝不自动纳管。纳管必须由人显式发起并持有注册 Key。
type DiscoveredPeer struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	TailscaleIP  string    `gorm:"size:64;index" json:"tailscale_ip"`
	Hostname     string    `gorm:"size:255" json:"hostname"`
	OS           string    `gorm:"size:64" json:"os"`
	DiscoveredAt time.Time `json:"discovered_at"`
	Dismissed    bool      `gorm:"default:false" json:"dismissed"`
}

// TelemetrySample 是遥测历史采样。控制面按需上线，
// 所以这里存的都是节点补传上来的历史数据。
type TelemetrySample struct {
	ID                 uint      `gorm:"primaryKey" json:"id"`
	NodeID             string    `gorm:"size:64;index;not null" json:"node_id"`
	Ts                 time.Time `gorm:"index;not null" json:"ts"`
	CPUPercent         float64   `json:"cpu_percent"`
	MemTotalBytes      uint64    `json:"mem_total_bytes"`
	MemUsedBytes       uint64    `json:"mem_used_bytes"`
	DiskTotalBytes     uint64    `json:"disk_total_bytes"`
	DiskUsedBytes      uint64    `json:"disk_used_bytes"`
	NetRxBytes         uint64    `json:"net_rx_bytes"`
	NetTxBytes         uint64    `json:"net_tx_bytes"`
	Load1              float64   `json:"load1"`
	TemperatureCelsius float64   `json:"temperature_celsius"`
	ContainersRunning  uint32    `json:"containers_running"`
}

// All 返回所有实体，供自动迁移使用。顺序即建表顺序。
func All() []any {
	return []any{
		&Node{},
		&NodeFingerprint{},
		&RegistrationKey{},
		&NodeCredential{},
		&User{},
		&AuditLog{},
		&ConfigVersion{},
		&AlertRule{},
		&Command{},
		&PanelTarget{},
		&NodeAddress{},
		&DiscoveredPeer{},
		&TelemetrySample{},
	}
}
