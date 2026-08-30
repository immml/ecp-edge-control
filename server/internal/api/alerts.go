package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/server/internal/store/model"
)

// alertRuleYAML 生成下发到节点的规则 YAML（顶层列表，agent parseRules 兼容）。
type alertRuleYAML struct {
	Name        string  `yaml:"name"`
	Metric      string  `yaml:"metric"`
	Op          string  `yaml:"op"`
	Threshold   float64 `yaml:"threshold"`
	CooldownSec int     `yaml:"cooldown_sec"`
}

// ListAlertRules 规则列表（?node_id= 可选）。
func (h *Handler) ListAlertRules(c *gin.Context) {
	nodeID := c.Query("node_id")
	rules, err := h.store.ListAlertRules(nodeID)
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	ok(c, gin.H{"rules": rules, "total": len(rules)})
}

// CreateAlertRule 新建规则。
//
// body: {"node_id","name","metric","op","threshold","cooldown_sec"}
// 或 {"node_id","rule_yaml"} 直接给整段 YAML。
func (h *Handler) CreateAlertRule(c *gin.Context) {
	var in struct {
		NodeID      string  `json:"node_id"`
		Name        string  `json:"name"`
		Metric      string  `json:"metric"`
		Op          string  `json:"op"`
		Threshold   float64 `json:"threshold"`
		CooldownSec int     `json:"cooldown_sec"`
		RuleYAML    string  `json:"rule_yaml"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	r := &model.AlertRule{NodeID: in.NodeID, Name: in.Name, Enabled: true}
	if in.RuleYAML != "" {
		r.RuleYAML = in.RuleYAML
	} else {
		if in.Name == "" || in.Metric == "" || in.Op == "" || in.Threshold == 0 {
			fail(c, http.StatusBadRequest, codeParam, "name/metric/op/threshold 必填（或直接给 rule_yaml）")
			return
		}
		data, _ := yaml.Marshal(alertRuleYAML{
			Name:        in.Name,
			Metric:      in.Metric,
			Op:          in.Op,
			Threshold:   in.Threshold,
			CooldownSec: in.CooldownSec,
		})
		r.RuleYAML = string(data)
		r.Name = in.Name
	}
	if err := h.store.SaveAlertRule(r); err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	h.log("新增告警规则", r.Name, "node", r.NodeID)
	ok(c, r)
}

// UpdateAlertRule 更新规则（整体覆盖 YAML）。
func (h *Handler) UpdateAlertRule(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	old, err := h.store.GetAlertRule(uint(id))
	if err != nil {
		fail(c, http.StatusNotFound, codeNotFound, "规则不存在")
		return
	}
	var in struct {
		Name     string `json:"name"`
		RuleYAML string `json:"rule_yaml"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeParam, "参数错误")
		return
	}
	if in.Name != "" {
		old.Name = in.Name
	}
	if in.RuleYAML != "" {
		old.RuleYAML = in.RuleYAML
	}
	if in.Enabled != nil {
		old.Enabled = *in.Enabled
	}
	if err := h.store.SaveAlertRule(old); err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	h.log("更新告警规则", old.Name)
	ok(c, old)
}

// DeleteAlertRule 删除规则。
func (h *Handler) DeleteAlertRule(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	if err := h.store.DeleteAlertRule(uint(id)); err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	h.log("删除告警规则", "id", id)
	ok(c, gin.H{"deleted": true})
}

// DeployAlertRule 将（该节点或全局）启用规则组装为 YAML 下发到指定节点。
//
// body: {"node_id": "n-xxx"}——规则来源：该节点的规则；若无节点规则则用全局规则。
func (h *Handler) DeployAlertRule(c *gin.Context) {
	var in struct {
		NodeID string `json:"node_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.NodeID == "" {
		fail(c, http.StatusBadRequest, codeParam, "缺少 node_id")
		return
	}
	// 组装 YAML：先取该节点规则，空则取全局（node_id 为空）
	rules, _ := h.store.ListAlertRules(in.NodeID)
	if len(rules) == 0 {
		rules, _ = h.store.ListAlertRules("")
	}
	list := []alertRuleYAML{}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		var one alertRuleYAML
		if err := yaml.Unmarshal([]byte(r.RuleYAML), &one); err == nil && one.Name != "" {
			list = append(list, one)
		}
	}
	data, _ := yaml.Marshal(list)

	msg := &ecpv1.ControlMessage{
		Payload: &ecpv1.ControlMessage_AlertRules{
			AlertRules: &ecpv1.AlertRuleSync{RulesYaml: data, Version: "v1"},
		},
	}
	if err := h.sessions.Send(in.NodeID, msg); err != nil {
		if err.Error() == "节点离线，无法下发指令" {
			fail(c, http.StatusServiceUnavailable, codeOffline, "节点离线，规则已保存待节点上线后手动下发")
			return
		}
		fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	h.log("下发告警规则", in.NodeID, "count", len(list))
	ok(c, gin.H{"deployed": true, "count": len(list), "rules_yaml": string(data)})
}

// ListAlertEvents 告警事件列表（?node_id=&limit=）。
func (h *Handler) ListAlertEvents(c *gin.Context) {
	nodeID := c.Query("node_id")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	events, total, err := h.store.ListAlertEvents(nodeID, limit, 0)
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	ok(c, gin.H{"events": events, "total": total})
}

// MarkAlertEventsRead 标记事件已读（?node_id= 可选，空则全部）。
func (h *Handler) MarkAlertEventsRead(c *gin.Context) {
	nodeID := c.Query("node_id")
	if err := h.store.MarkAlertEventsRead(nodeID); err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	ok(c, gin.H{"read": true})
}
