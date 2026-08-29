// Package api 实现控制台的 REST 接口与静态资源托管。
//
// 统一响应结构 { code, message, data }（见架构 v2 §8.1）。
// 所有写操作经由 JWT + RBAC 中间件，并由审计中间件留痕。
package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/structpb"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/server/internal/auth"
	"ecp.dev/ecp/server/internal/command"
	"ecp.dev/ecp/server/internal/session"
	"ecp.dev/ecp/server/internal/store"
	"ecp.dev/ecp/server/internal/store/model"
)

// 统一响应码（§8.1 子集）。
const (
	codeOK         = 0
	codeParam      = 10001
	codeUnauth     = 10002
	codeForbidden  = 10003
	codeNotFound   = 10004
	codeInternal   = 10006
	codeOffline    = 30001
)

type resp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func ok(c *gin.Context, data any) {
	c.JSON(http.StatusOK, resp{Code: codeOK, Message: "ok", Data: data})
}
func fail(c *gin.Context, httpStatus, code int, msg string) {
	c.JSON(httpStatus, resp{Code: code, Message: msg})
}

// Handler 聚合依赖。
type Handler struct {
	store      *store.Store
	sessions   *session.Manager
	dispatch   *command.Dispatcher
	log        func(string, ...any)
}

// New 构造 gin 引擎。
func New(st *store.Store, sessions *session.Manager, dispatch *command.Dispatcher, log func(string, ...any)) *gin.Engine {
	h := &Handler{store: st, sessions: sessions, dispatch: dispatch, log: log}
	r := gin.New()
	r.Use(gin.Recovery())

	r.POST("/api/v1/login", h.Login)

	api := r.Group("/api/v1")
	api.Use(h.JWTAuth())
	{
		api.GET("/me", h.Me)
		api.GET("/nodes", h.ListNodes)
		api.GET("/nodes/:id", h.GetNode)
		api.GET("/nodes/:id/telemetry", h.NodeTelemetry)
		api.GET("/audit", h.ListAudit)
		api.POST("/nodes/:id/command", h.RequireRole(model.RoleOperator), h.ExecCommand)
	}
	return r
}

// JWTAuth 校验 Bearer Token 并把 claims 注入上下文。
func (h *Handler) JWTAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		authz := c.GetHeader("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			fail(c, http.StatusUnauthorized, codeUnauth, "未认证")
			c.Abort()
			return
		}
		claims, err := auth.ParseToken(strings.TrimPrefix(authz, "Bearer "))
		if err != nil {
			fail(c, http.StatusUnauthorized, codeUnauth, "凭据无效或已过期")
			c.Abort()
			return
		}
		c.Set("uid", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("role", claims.Role)
		c.Next()
	}
}

// RequireRole 校验最低角色等级。
func (h *Handler) RequireRole(required string) gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("role")
		if !auth.RoleCan(role.(string), required) {
			fail(c, http.StatusForbidden, codeForbidden, "权限不足")
			c.Abort()
			return
		}
		c.Next()
	}
}

// Login 处理登录：校验口令并签发 JWT；首次无用户时拒绝（需先初始化超管）。
func (h *Handler) Login(c *gin.Context) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Username == "" || in.Password == "" {
		fail(c, http.StatusBadRequest, codeParam, "用户名与密码必填")
		return
	}
	u, err := h.store.GetUserByUsername(in.Username)
	if err != nil {
		fail(c, http.StatusUnauthorized, codeUnauth, "用户名或密码错误")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, in.Password) {
		fail(c, http.StatusUnauthorized, codeUnauth, "用户名或密码错误")
		return
	}
	_ = h.store.UpdateLastLogin(u.ID)
	token, err := auth.SignToken(u)
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "签发令牌失败")
		return
	}
	ok(c, gin.H{
		"token":   token,
		"username": u.Username,
		"role":     u.Role,
	})
}

// Me 返回当前登录用户信息。
func (h *Handler) Me(c *gin.Context) {
	ok(c, gin.H{
		"username": c.GetString("username"),
		"role":     c.GetString("role"),
	})
}

// ListNodes 返回节点列表（含在线状态与最新遥测摘要）。
func (h *Handler) ListNodes(c *gin.Context) {
	nodes, err := h.store.ListNodes()
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "查询失败")
		return
	}
	out := make([]gin.H, 0, len(nodes))
	for _, n := range nodes {
		online := h.sessions.IsOnline(n.ID)
		t := h.sessions.LatestTelemetry(n.ID)
		row := gin.H{
			"id":           n.ID,
			"hostname":     n.Hostname,
			"arch":         n.Arch,
			"os":           n.OS,
			"agent_version": n.AgentVersion,
			"status":       statusOf(online),
			"last_seen_at": n.LastSeenAt,
		}
		if t != nil {
			row["cpu_percent"] = t.CpuPercent
			row["mem_used_bytes"] = t.MemUsedBytes
			row["mem_total_bytes"] = t.MemTotalBytes
			row["containers_running"] = t.ContainerRunning
		}
		out = append(out, row)
	}
	ok(c, gin.H{"total": len(out), "nodes": out})
}

// GetNode 返回单节点详情。
func (h *Handler) GetNode(c *gin.Context) {
	id := c.Param("id")
	n, err := h.store.GetNode(id)
	if err != nil {
		fail(c, http.StatusNotFound, codeNotFound, "节点不存在")
		return
	}
	telemetry := h.sessions.LatestTelemetry(id)
	ok(c, gin.H{
		"node":          n,
		"online":        h.sessions.IsOnline(id),
		"telemetry":     telemetry,
	})
}

// NodeTelemetry 返回节点遥测历史（SQLite），?limit= 控制条数，默认 60。
func (h *Handler) NodeTelemetry(c *gin.Context) {
	id := c.Param("id")
	limit := 60
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	items, err := h.store.ListTelemetry(id, limit)
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "查询遥测历史失败")
		return
	}
	ok(c, gin.H{
		"items":  items, // 最新在前，画图时前端自行反转
		"latest": h.sessions.LatestTelemetry(id),
	})
}

// ExecCommand 向节点下发指令并等待结果（需要 operator 以上权限）。
func (h *Handler) ExecCommand(c *gin.Context) {
	id := c.Param("id")
	var in struct {
		Type       string           `json:"type"`
		Params     *structpb.Struct `json:"params"`
		TimeoutSec int32            `json:"timeout_sec"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Type == "" {
		fail(c, http.StatusBadRequest, codeParam, "指令类型必填")
		return
	}
	cmd := &ecpv1.Command{Type: commandType(in.Type), Params: in.Params, TimeoutSec: in.TimeoutSec}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 35*time.Second)
	defer cancel()
	res, err := h.dispatch.Dispatch(ctx, c.GetUint("uid"), c.GetString("username"), id, cmd)
	if err != nil {
		if err.Error() == "节点离线，无法下发指令" {
			fail(c, http.StatusServiceUnavailable, codeOffline, "节点离线")
			return
		}
		fail(c, http.StatusInternalServerError, codeInternal, err.Error())
		return
	}
	ok(c, res)
}

// ListAudit 查询审计日志。
func (h *Handler) ListAudit(c *gin.Context) {
	nodeID := c.Query("node_id")
	action := c.Query("action")
	limit := 100
	logs, err := h.store.ListAudit(nodeID, action, limit)
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "查询失败")
		return
	}
	ok(c, gin.H{"total": len(logs), "logs": logs})
}

// statusOf 把在线布尔映射为业务状态字符串。
func statusOf(online bool) string {
	if online {
		return model.StatusOnline
	}
	return model.StatusOffline
}

// commandType 把字符串命令类型映射为枚举。前端可传枚举全名（COMMAND_TYPE_SHELL）
// 或短名（SHELL），不区分大小写。
func commandType(s string) ecpv1.CommandType {
	norm := strings.ToUpper(strings.TrimPrefix(s, "COMMAND_TYPE_"))
	for _, t := range []ecpv1.CommandType{
		ecpv1.CommandType_COMMAND_TYPE_SHELL,
		ecpv1.CommandType_COMMAND_TYPE_FILE_LIST,
		ecpv1.CommandType_COMMAND_TYPE_FILE_READ,
		ecpv1.CommandType_COMMAND_TYPE_FILE_WRITE,
		ecpv1.CommandType_COMMAND_TYPE_FILE_DELETE,
		ecpv1.CommandType_COMMAND_TYPE_FILE_STAT,
		ecpv1.CommandType_COMMAND_TYPE_NET_GET,
		ecpv1.CommandType_COMMAND_TYPE_NET_SET,
		ecpv1.CommandType_COMMAND_TYPE_FIREWALL_GET,
		ecpv1.CommandType_COMMAND_TYPE_FIREWALL_SET,
		ecpv1.CommandType_COMMAND_TYPE_TAILSCALE_STATUS,
		ecpv1.CommandType_COMMAND_TYPE_TAILSCALE_UP,
		ecpv1.CommandType_COMMAND_TYPE_TAILSCALE_DOWN,
		ecpv1.CommandType_COMMAND_TYPE_DOCKER_LIST,
		ecpv1.CommandType_COMMAND_TYPE_DOCKER_ACTION,
		ecpv1.CommandType_COMMAND_TYPE_DOCKER_LOGS,
		ecpv1.CommandType_COMMAND_TYPE_AGENT_UPGRADE,
		ecpv1.CommandType_COMMAND_TYPE_ALERT_RULE_SYNC,
		ecpv1.CommandType_COMMAND_TYPE_LOG_QUERY,
	} {
		if strings.TrimPrefix(t.String(), "COMMAND_TYPE_") == norm {
			return t
		}
	}
	return ecpv1.CommandType_COMMAND_TYPE_UNSPECIFIED
}
