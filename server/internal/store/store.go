// Package store 封装控制面的持久化。
//
// 一期用 SQLite（纯 Go 驱动，Agent 侧要 CGO_ENABLED=0 静态编译），
// 接口已按 PostgreSQL 的形状预留，将来换驱动不动业务代码。
package store

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/server/internal/store/model"
)

// 已知错误
var (
	ErrNotFound      = errors.New("记录不存在")
	ErrAlreadyExists = errors.New("记录已存在")
)

// Store 是控制面的数据访问入口。
type Store struct {
	db  *gorm.DB
	log *slog.Logger
}

// Open 打开数据库并自动迁移表结构。
//
// SQLite 强制启用 WAL：控制面与可能的备份进程会并发读写，
// WAL 能避免读写互相阻塞。
func Open(driver, dsn string, log *slog.Logger) (*Store, error) {
	if driver != "sqlite" {
		return nil, fmt.Errorf("不支持的存储驱动: %s（一期仅实现 sqlite）", driver)
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接失败: %w", err)
	}
	// 控制面是单进程，连接池保持小即可
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("启用 WAL 失败: %w", err)
	}
	if _, err := sqlDB.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return nil, fmt.Errorf("启用外键约束失败: %w", err)
	}

	s := &Store{db: db, log: log}
	if err := db.AutoMigrate(model.All()...); err != nil {
		return nil, fmt.Errorf("自动迁移失败: %w", err)
	}
	return s, nil
}

// Close 关闭数据库连接。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// DB 返回底层句柄。仅用于尚未抽象成方法的查询，
// 新代码应优先在本包内加方法。
func (s *Store) DB() *gorm.DB { return s.db }

// Tables 返回当前数据库中已存在的表名，用于启动自检。
func (s *Store) Tables() []string {
	var names []string
	_ = s.db.Raw("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name").
		Scan(&names).Error
	return names
}

// ============================================================
// 节点
// ============================================================

// UpsertNode 创建或更新节点。
func (s *Store) UpsertNode(n *model.Node) error {
	return s.db.Save(n).Error
}

// GetNode 按 ID 查询节点，不存在返回 ErrNotFound。
func (s *Store) GetNode(id string) (*model.Node, error) {
	var n model.Node
	if err := s.db.Where("id = ?", id).First(&n).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &n, nil
}

// ListNodes 返回全部节点，按主机名排序。
func (s *Store) ListNodes() ([]model.Node, error) {
	var out []model.Node
	if err := s.db.Order("hostname asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateNodeStatus 更新节点在线状态与最后见到的时间。
func (s *Store) UpdateNodeStatus(id, status string, caps string) error {
	return s.db.Model(&model.Node{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":            status,
			"capabilities_json": caps,
			"last_seen_at":      time.Now(),
		}).Error
}

// UpdateAgentVersion 更新 Agent 版本号。
func (s *Store) UpdateAgentVersion(id, version string) error {
	if version == "" {
		return nil
	}
	return s.db.Model(&model.Node{}).Where("id = ?", id).
		UpdateColumn("agent_version", version).Error
}

// UpdateTailscaleIP 更新节点 Tailscale IP。
func (s *Store) UpdateTailscaleIP(id, tsIP string) error {
	if tsIP == "" {
		return nil
	}
	return s.db.Model(&model.Node{}).Where("id = ?", id).
		UpdateColumn("tailscale_ip", tsIP).Error
}

// UpdateCapabilities 仅更新能力 JSON。
func (s *Store) UpdateCapabilities(id, caps string) error {
	return s.db.Model(&model.Node{}).Where("id = ?", id).
		UpdateColumn("capabilities_json", caps).Error
}

// ============================================================
// 注册 Key 与指纹（"上线即控"的核心）
// ============================================================

// CreateKey 登记一个新的注册 Key。只存哈希。
func (s *Store) CreateKey(k *model.RegistrationKey) error {
	return s.db.Create(k).Error
}

// GetKeyByHash 按哈希查找注册 Key。
func (s *Store) GetKeyByHash(hash string) (*model.RegistrationKey, error) {
	var k model.RegistrationKey
	if err := s.db.Where("key_hash = ?", hash).First(&k).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &k, nil
}

// GetFingerprint 查询节点已绑定的指纹。
func (s *Store) GetFingerprint(nodeID string) (*model.NodeFingerprint, error) {
	var f model.NodeFingerprint
	if err := s.db.Where("node_id = ?", nodeID).First(&f).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &f, nil
}

// BindFingerprint 绑定节点的硬件指纹。首次注册时调用一次。
func (s *Store) BindFingerprint(nodeID, fingerprint string) error {
	f := &model.NodeFingerprint{
		NodeID:      nodeID,
		Fingerprint: fingerprint,
		BoundAt:     time.Now(),
	}
	return s.db.Create(f).Error
}

// BindKeyToNode 把注册 Key 绑定到某个节点（首次绑定或重认证时调用）。
//
// 一旦绑定，该 Key 就只能用于这台节点——这是"上线即控"的硬约束：
// 即便 Key 泄露，攻击者也无法用它绑定一台新设备。
func (s *Store) BindKeyToNode(keyHash, nodeID string) error {
	return s.db.Model(&model.RegistrationKey{}).
		Where("key_hash = ?", keyHash).
		Update("bound_node_id", nodeID).Error
}

// IssueCredential 登记一张签发给节点的客户端证书台账。
//
// 证书本身的 PEM 不入库（私钥与证书都在节点本地），这里只记台账，
// 便于后续吊销查询与审计。
func (s *Store) IssueCredential(nodeID, serial string, expiresAt time.Time) error {
	c := &model.NodeCredential{
		NodeID:     nodeID,
		CertSerial: serial,
		IssuedAt:   time.Now(),
		ExpiresAt:  expiresAt,
	}
	return s.db.Create(c).Error
}

// IsCertRevoked 判断某个序列号的客户端证书是否已被吊销。
func (s *Store) IsCertRevoked(nodeID string, serial string) (bool, error) {
	var c model.NodeCredential
	if err := s.db.Where("node_id = ? AND cert_serial = ?", nodeID, serial).First(&c).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, nil // 不在台账里的证书视为无效
		}
		return true, err
	}
	return c.RevokedAt != nil, nil
}

// ============================================================
// 用户与 RBAC
// ============================================================

// GetUserByUsername 按用户名查询用户。
func (s *Store) GetUserByUsername(name string) (*model.User, error) {
	var u model.User
	if err := s.db.Where("username = ?", name).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &u, nil
}

// CreateUser 创建控制台用户。
func (s *Store) CreateUser(u *model.User) error {
	return s.db.Create(u).Error
}

// ListUsers 返回全部用户（不含密码哈希）。
func (s *Store) ListUsers() ([]model.User, error) {
	var out []model.User
	if err := s.db.Order("id asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// CountUsers 返回用户总数，用于首次启动判断是否需要创建超管。
func (s *Store) CountUsers() (int64, error) {
	var n int64
	if err := s.db.Model(&model.User{}).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// UpdatePassword 更新用户密码哈希（改密码入口用）。
func (s *Store) UpdatePassword(userID uint, hash string) error {
	return s.db.Model(&model.User{}).Where("id = ?", userID).Update("password_hash", hash).Error
}

// UpdateLastLogin 记录用户最后登录时间。
func (s *Store) UpdateLastLogin(id uint) error {
	return s.db.Model(&model.User{}).
		Where("id = ?", id).
		Update("last_login_at", time.Now()).Error
}

// ============================================================
// 审计日志（append-only）
// ============================================================

// AppendAudit 写入审计日志。这是本包中唯一允许写 audit_logs 的方法，
// 且刻意不提供 Update / Delete——审计不可篡改是合规底线。
func (s *Store) AppendAudit(a *model.AuditLog) error {
	if a.Ts.IsZero() {
		a.Ts = time.Now()
	}
	return s.db.Create(a).Error
}

// ListAudit 按条件查询审计日志，按时间倒序。
func (s *Store) ListAudit(nodeID, action string, limit int) ([]model.AuditLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := s.db.Model(&model.AuditLog{})
	if nodeID != "" {
		q = q.Where("node_id = ?", nodeID)
	}
	if action != "" {
		q = q.Where("action = ?", action)
	}
	var out []model.AuditLog
	if err := q.Order("ts desc").Limit(limit).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ============================================================
// 节点地址（替代被砍掉的 Worker KV）
// ============================================================

// SaveAddress 记录节点的已知地址。
func (s *Store) SaveAddress(a *model.NodeAddress) error {
	if a.LastSeenAt.IsZero() {
		a.LastSeenAt = time.Now()
	}
	return s.db.Save(a).Error
}

// ListAddresses 返回节点的全部已知地址。
func (s *Store) ListAddresses(nodeID string) ([]model.NodeAddress, error) {
	var out []model.NodeAddress
	if err := s.db.Where("node_id = ?", nodeID).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ============================================================
// 遥测采样（控制面落库，供控制台图表展示）
// ============================================================

// SaveTelemetry 写入一条遥测采样。每次心跳都会调用，写入频率由采集间隔决定。
func (s *Store) SaveTelemetry(nodeID string, t *ecpv1.Telemetry) error {
	if t == nil {
		return nil
	}
	sample := &model.TelemetrySample{
		NodeID:             nodeID,
		Ts:                 time.Now(),
		CPUPercent:         t.GetCpuPercent(),
		MemTotalBytes:      t.GetMemTotalBytes(),
		MemUsedBytes:       t.GetMemUsedBytes(),
		DiskTotalBytes:     t.GetDiskTotalBytes(),
		DiskUsedBytes:      t.GetDiskUsedBytes(),
		NetRxBytes:         t.GetNetRxBytes(),
		NetTxBytes:         t.GetNetTxBytes(),
		Load1:              t.GetLoad1(),
		TemperatureCelsius: t.GetTemperatureCelsius(),
		ContainersRunning:  t.GetContainerRunning(),
	}
	return s.db.Create(sample).Error
}

// ListTelemetry 返回节点最近的遥测采样（按时间倒序）。
func (s *Store) ListTelemetry(nodeID string, limit int) ([]model.TelemetrySample, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	var out []model.TelemetrySample
	if err := s.db.Where("node_id = ?", nodeID).
		Order("ts desc").Limit(limit).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// SaveAlertEvent 保存一条告警事件。
func (s *Store) SaveAlertEvent(ev *model.AlertEvent) error {
	return s.db.Create(ev).Error
}

// ListAlertEvents 查询告警事件（节点过滤 + 分页，按时间倒序）。
func (s *Store) ListAlertEvents(nodeID string, limit, offset int) ([]model.AlertEvent, int64, error) {
	q := s.db.Model(&model.AlertEvent{})
	if nodeID != "" {
		q = q.Where("node_id = ?", nodeID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var out []model.AlertEvent
	if err := q.Order("created_at desc").Limit(limit).Offset(offset).Find(&out).Error; err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ListAlertRules 返回全部告警规则（可按节点过滤）。
func (s *Store) ListAlertRules(nodeID string) ([]model.AlertRule, error) {
	q := s.db.Model(&model.AlertRule{})
	if nodeID != "" {
		q = q.Where("node_id = ?", nodeID)
	}
	var out []model.AlertRule
	if err := q.Order("id asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// SaveAlertRule 新建/更新告警规则。
func (s *Store) SaveAlertRule(r *model.AlertRule) error {
	return s.db.Save(r).Error
}

// GetAlertRule 按 ID 取规则。
func (s *Store) GetAlertRule(id uint) (*model.AlertRule, error) {
	var r model.AlertRule
	if err := s.db.First(&r, id).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

// DeleteAlertRule 删除规则。
func (s *Store) DeleteAlertRule(id uint) error {
	return s.db.Delete(&model.AlertRule{}, id).Error
}

// MarkAlertEventsRead 将指定节点的事件标记为已读。
func (s *Store) MarkAlertEventsRead(nodeID string) error {
	q := s.db.Model(&model.AlertEvent{})
	if nodeID != "" {
		q = q.Where("node_id = ?", nodeID)
	}
	return q.Update("read", true).Error
}
