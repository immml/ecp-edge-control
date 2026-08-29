// Package cache 是 Agent 的本地缓存。
//
// 存在的唯一理由：控制面按需上线，大部分时间不在线。
// 这期间采集到的遥测、产生的事件、执行过的操作结果都要先存在本地，
// 等控制面上线后通过 UploadBacklog 补传。
package cache

import (
	"errors"
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var ErrNotFound = errors.New("记录不存在")

// Sample 是一条本地缓存的遥测采样。
type Sample struct {
	ID                 uint      `gorm:"primaryKey"`
	Ts                 time.Time `gorm:"index;not null"`
	CPUPercent         float64
	MemTotalBytes      uint64
	MemUsedBytes       uint64
	DiskTotalBytes     uint64
	DiskUsedBytes      uint64
	NetRxBytes         uint64
	NetTxBytes         uint64
	Load1              float64
	TemperatureCelsius float64
	ContainersRunning  uint32
	Uploaded           bool `gorm:"default:false;index"`
}

// Event 是一条本地缓存的节点事件。
type Event struct {
	ID       uint      `gorm:"primaryKey"`
	Ts       time.Time `gorm:"index;not null"`
	Kind     string    `gorm:"size:64;index"`
	Message  string    `gorm:"type:text"`
	Data     string    `gorm:"type:text"`
	Uploaded bool      `gorm:"default:false;index"`
}

// AlertRecord 是一条本地告警历史。控制面不在线时也要能看到"曾经告警过"。
type AlertRecord struct {
	ID      uint      `gorm:"primaryKey"`
	Ts      time.Time `gorm:"index;not null"`
	Rule    string    `gorm:"size:128"`
	Message string    `gorm:"type:text"`
	Sent    bool      `gorm:"default:false"`
}

// Cache 是本地缓存的入口。
type Cache struct {
	db *gorm.DB
}

// Open 打开本地缓存数据库并建表。
func Open(path string) (*Cache, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("打开本地缓存失败: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&Sample{}, &Event{}, &AlertRecord{}); err != nil {
		return nil, fmt.Errorf("本地缓存建表失败: %w", err)
	}
	return &Cache{db: db}, nil
}

// Close 关闭缓存。
func (c *Cache) Close() error {
	sqlDB, err := c.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// AppendSample 写入一条遥测采样。
func (c *Cache) AppendSample(s *Sample) error {
	if s.Ts.IsZero() {
		s.Ts = time.Now()
	}
	return c.db.Create(s).Error
}

// AppendEvent 写入一条节点事件。
func (c *Cache) AppendEvent(e *Event) error {
	if e.Ts.IsZero() {
		e.Ts = time.Now()
	}
	return c.db.Create(e).Error
}

// AppendAlert 写入一条告警历史。
func (c *Cache) AppendAlert(a *AlertRecord) error {
	if a.Ts.IsZero() {
		a.Ts = time.Now()
	}
	return c.db.Create(a).Error
}

// PendingSamples 返回尚未补传的采样，按时间正序。
func (c *Cache) PendingSamples(limit int) ([]Sample, error) {
	if limit <= 0 {
		limit = 1000
	}
	var out []Sample
	if err := c.db.Where("uploaded = ?", false).Order("ts asc").Limit(limit).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// PendingEvents 返回尚未补传的事件。
func (c *Cache) PendingEvents(limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 500
	}
	var out []Event
	if err := c.db.Where("uploaded = ?", false).Order("ts asc").Limit(limit).Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// MarkSamplesUploaded 把指定 ID 的采样标记为已补传。
func (c *Cache) MarkSamplesUploaded(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return c.db.Model(&Sample{}).Where("id IN ?", ids).Update("uploaded", true).Error
}

// MarkEventsUploaded 把指定 ID 的事件标记为已补传。
func (c *Cache) MarkEventsUploaded(ids []uint) error {
	if len(ids) == 0 {
		return nil
	}
	return c.db.Model(&Event{}).Where("id IN ?", ids).Update("uploaded", true).Error
}

// Prune 清理超过保留期的已补传数据。
//
// 边缘节点磁盘有限（Orange Pi 上虽然还有 200G，但不能假设所有节点都这样），
// 未补传的数据永远不清理——宁可占空间也不能丢数据。
func (c *Cache) Prune(retention time.Duration) error {
	cutoff := time.Now().Add(-retention)
	if err := c.db.Where("ts < ? AND uploaded = ?", cutoff, true).Delete(&Sample{}).Error; err != nil {
		return err
	}
	return c.db.Where("ts < ? AND uploaded = ?", cutoff, true).Delete(&Event{}).Error
}
