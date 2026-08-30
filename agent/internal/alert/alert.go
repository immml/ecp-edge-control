// Package alert 是节点本地的告警规则引擎。
//
// T4-C 的核心。设计原则（源自架构 v2 §6 与合规边界）：
//   - 规则在节点本地执行，控制面不在线也能触发——这正是"边缘自治"的意义；
//   - 命中后即时推送飞书机器人（不依赖控制面），同时写本地告警历史；
//   - 阈值类规则带冷却，避免抖动反复刷屏；离线检测基于心跳丢失计数。
//
// 规则既有"节点本地文件"又有"控制面下发"两种来源：控制面通过 AlertRuleSync
// 下发 YAML，Agent 落盘到 RulesFile 后热加载，二者最终归一为同一份本地文件。
package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/agent/internal/cache"
	"ecp.dev/ecp/agent/internal/config"
)

// Rule 是一条阈值告警规则。
//
// Metric 取值对应 ecpv1.Telemetry 的字段：
//   cpu_percent / mem_used_percent / disk_used_percent / load1 / temperature_celsius
type Rule struct {
	Name        string  `yaml:"name"`
	Metric      string  `yaml:"metric"`
	Op          string  `yaml:"op"` // ">" 或 "<"
	Threshold   float64 `yaml:"threshold"`
	CooldownSec int     `yaml:"cooldown_sec"` // 命中后冷却，避免反复刷屏
}

// Engine 是本地告警引擎。
type Engine struct {
	cfg        *config.Config
	cache      *cache.Cache
	log        *slog.Logger
	rules      []Rule
	lastFired  map[string]time.Time
	missed     int
	mu         sync.Mutex
	// OnEvent 可选回调：告警触发时上报事件到控制面（kind=alert_fired）
	OnEvent func(kind, message string)
}

// 离线判定：连续丢失多少个心跳周期后视为离线。
const offlineThreshold = 3

// New 构造引擎并加载本地规则文件（不存在则忽略）。
func New(cfg *config.Config, ch *cache.Cache, log *slog.Logger) *Engine {
	e := &Engine{
		cfg:       cfg,
		cache:     ch,
		log:       log,
		rules:     nil,
		lastFired: map[string]time.Time{},
	}
	_ = e.LoadRules()
	return e
}

// LoadRules 从本地规则文件加载（文件不存在时清空规则，不报错）。
func (e *Engine) LoadRules() error {
	path := e.cfg.Alert.RulesFile
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			e.rules = nil
			return nil
		}
		return err
	}
	rules, err := parseRules(data)
	if err != nil {
		return err
	}
	e.rules = rules
	return nil
}

// parseRules 兼容两种 YAML 形态：
//   - 顶层规则列表（引擎原生格式）
//   - { rules: [...] } 包裹（常见样例/部署格式）
func parseRules(data []byte) ([]Rule, error) {
	var list []Rule
	if err := yaml.Unmarshal(data, &list); err == nil {
		return list, nil
	}
	var wrapped struct {
		Rules []Rule `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &wrapped); err != nil {
		return nil, err
	}
	return wrapped.Rules, nil
}

// ApplyRules 控制面下发规则：落盘到 RulesFile 并重载。
func (e *Engine) ApplyRules(yamlBytes []byte) error {
	// 先尝试解析，避免写入非法内容覆盖现有规则
	if _, err := parseRules(yamlBytes); err != nil {
		return err
	}
	if err := os.WriteFile(e.cfg.Alert.RulesFile, yamlBytes, 0o600); err != nil {
		return err
	}
	rules, _ := parseRules(yamlBytes)
	e.rules = rules
	return nil
}

// Evaluate 对一次遥测快照做阈值评估，命中即触发。
func (e *Engine) Evaluate(t *ecpv1.Telemetry) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, r := range e.rules {
		val, ok := metricValue(t, r.Metric)
		if !ok {
			continue
		}
		breached := (r.Op == ">" && val > r.Threshold) ||
			(r.Op == "<" && val < r.Threshold)
		if !breached {
			continue
		}
		now := time.Now()
		if since, ok := e.lastFired[r.Name]; ok && now.Sub(since) < time.Duration(r.CooldownSec)*time.Second {
			continue // 冷却中，跳过
		}
		e.lastFired[r.Name] = now
		e.fire(r.Name, formatRule(r, val))
	}
}

// RecordHeartbeat 记录心跳成败，用于离线检测。
//
// 控制面连续 offlineThreshold 个周期不可达则触发离线告警；恢复后发一条恢复通知。
func (e *Engine) RecordHeartbeat(ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if ok {
		if e.missed >= offlineThreshold {
			e.fire("node_offline_resumed", "节点已恢复与控制面的连接")
		}
		e.missed = 0
		return
	}
	e.missed++
	if e.missed == offlineThreshold {
		e.fire("node_offline", "节点已连续多个周期无法连接控制面（疑似离线或网络中断）")
	}
}

// fire 触发一条告警：本地落地 + 上报控制面 + 飞书推送。
func (e *Engine) fire(name, message string) {
	_ = e.cache.AppendAlert(&cache.AlertRecord{Rule: name, Message: message})
	if e.OnEvent != nil {
		e.OnEvent("alert_fired", message)
	}
	if url := e.cfg.Alert.FeishuWebhook; url != "" {
		if err := pushFeishu(url, message); err != nil {
			e.log.Warn("飞书推送失败", "err", err)
		}
	}
	e.log.Info("告警触发", "rule", name, "msg", message)
}

// metricValue 从遥测中取出指定字段的值。
func metricValue(t *ecpv1.Telemetry, metric string) (float64, bool) {
	switch metric {
	case "cpu_percent":
		return t.GetCpuPercent(), true
	case "mem_used_percent":
		if t.GetMemTotalBytes() > 0 {
			return float64(t.GetMemUsedBytes()) / float64(t.GetMemTotalBytes()) * 100, true
		}
	case "disk_used_percent":
		if t.GetDiskTotalBytes() > 0 {
			return float64(t.GetDiskUsedBytes()) / float64(t.GetDiskTotalBytes()) * 100, true
		}
	case "load1":
		return t.GetLoad1(), true
	case "temperature_celsius":
		return t.GetTemperatureCelsius(), true
	}
	return 0, false
}

// formatRule 生成人类可读的告警文案。
func formatRule(r Rule, val float64) string {
	return "【边缘节点告警】规则 " + r.Name + " 触发：指标 " + r.Metric +
		" = " + strconv.FormatFloat(val, 'f', 2, 64) + " " + r.Op + " " +
		strconv.FormatFloat(r.Threshold, 'f', 2, 64)
}

// pushFeishu 向飞书自定义机器人 Webhook 推送纯文本消息。
func pushFeishu(url, text string) error {
	body, _ := json.Marshal(map[string]any{
		"msg_type": "text",
		"content":  map[string]any{"text": text},
	})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("飞书返回 HTTP %d", resp.StatusCode)
	}
	return nil
}
