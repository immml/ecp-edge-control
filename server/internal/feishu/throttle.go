// 告警推送节流：同一节点 + 同一规则在窗口期内只推送一次，防止高频告警刷屏。
package feishu

import (
	"log/slog"
	"regexp"
	"sync"
	"time"
)

// ThrottledNotifier 包装 PushConfig，按 node+rule 维度节流。
type ThrottledNotifier struct {
	cfg    PushConfig
	log    *slog.Logger
	window time.Duration

	mu   sync.Mutex
	last map[string]time.Time
}

// ruleRe 从告警文案中提取规则名：如「【边缘节点告警】规则 test_load_high 触发：…」。
var ruleRe = regexp.MustCompile(`规则\s+([^\s：:]+)`)

// NewThrottledNotifier 构造节流器；window<=0 时默认 5 分钟。
func NewThrottledNotifier(cfg PushConfig, log *slog.Logger, window time.Duration) *ThrottledNotifier {
	if window <= 0 {
		window = 5 * time.Minute
	}
	return &ThrottledNotifier{cfg: cfg, log: log, window: window, last: map[string]time.Time{}}
}

// Notify 按 nodeID+规则 节流推送；窗口期内重复触发仅记日志不推送。
func (t *ThrottledNotifier) Notify(nodeID, kind, message string) {
	if !t.cfg.Enabled() {
		return
	}
	rule := ruleRe.FindStringSubmatch(message)
	key := nodeID
	if len(rule) > 1 {
		key = nodeID + "|" + rule[1]
	} else {
		key = nodeID + "|" + message
	}

	t.mu.Lock()
	lastPush, seen := t.last[key]
	now := time.Now()
	if seen && now.Sub(lastPush) < t.window {
		t.mu.Unlock()
		t.log.Debug("告警推送已节流", "node", nodeID, "key", key)
		return
	}
	t.last[key] = now
	t.mu.Unlock()

	if err := Notify(t.cfg, message); err != nil {
		t.log.Warn("飞书群推送失败", "err", err)
		return
	}
	t.log.Info("飞书群推送成功", "text", truncate(message, 60))
}