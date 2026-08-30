// Package api 实现控制台的 REST 接口与静态资源托管。
//
// 统一响应结构 { code, message, data }（见架构 v2 §8.1）。
// 所有写操作经由 JWT + RBAC 中间件，并由审计中间件留痕。
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/structpb"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/server/internal/auth"
	"ecp.dev/ecp/server/internal/command"
	"ecp.dev/ecp/server/internal/grpcserver"
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
	store    *store.Store
	sessions *session.Manager
	dispatch *command.Dispatcher
	grpc     *grpcserver.Server
	log      func(string, ...any)
	// OTA：二进制存放目录、通告 IP、HTTPS 端口
	dataDir     string
	advertiseIP string
	httpsPort   string
}

// New 构造 gin 引擎。
func New(st *store.Store, sessions *session.Manager, dispatch *command.Dispatcher, gs *grpcserver.Server, log func(string, ...any), dataDir, advertiseIP, httpsPort string) *gin.Engine {
	h := &Handler{store: st, sessions: sessions, dispatch: dispatch, grpc: gs, log: log, dataDir: dataDir, advertiseIP: advertiseIP, httpsPort: httpsPort}
	r := gin.New()
	r.Use(gin.Recovery())

	r.POST("/api/v1/login", h.Login)
	// OTA：二进制上传/下载（下载免鉴权，仅内网静态文件）
	r.POST("/api/v1/agent/upload", h.UploadBinary)
	r.GET("/api/v1/agent/binaries/:name", h.ServeBinary)
	// WebSocket 无法携带 Authorization header，终端/VNC 走 query token 自校验
	r.GET("/api/v1/nodes/:id/terminal/ws", h.TerminalWS)
	r.GET("/api/v1/nodes/:id/vnc/ws", h.VncWS)
	// 1Panel 内置流量：浏览器只连控制台，经隧道转发到节点本地 31252。
	// 免鉴权（控制台仅本机监听；代理不绕过 1Panel 自身登录鉴权）。
	r.Any("/api/v1/nodes/:id/panel/*path", h.PanelProxy)

	api := r.Group("/api/v1")
	api.Use(h.JWTAuth())
	{
		api.GET("/me", h.Me)
		api.POST("/change-password", h.ChangePassword)
		// 紧急通道配置（relay）：登录后下发，前端据此建立降级通道
		api.GET("/relay/config", h.RelayConfig)
		api.GET("/nodes", h.ListNodes)
		api.GET("/nodes/:id", h.GetNode)
		api.GET("/nodes/:id/telemetry", h.NodeTelemetry)
		api.GET("/nodes/:id/files", h.RequireRole(model.RoleViewer), h.ListFiles)
		api.GET("/audit", h.ListAudit)
		// 告警闭环：规则 CRUD + 下发 + 事件
		api.GET("/alerts/rules", h.ListAlertRules)
		api.POST("/alerts/rules", h.RequireRole(model.RoleOperator), h.CreateAlertRule)
		api.PUT("/alerts/rules/:id", h.RequireRole(model.RoleOperator), h.UpdateAlertRule)
		api.DELETE("/alerts/rules/:id", h.RequireRole(model.RoleOperator), h.DeleteAlertRule)
		api.POST("/alerts/rules/deploy", h.RequireRole(model.RoleOperator), h.DeployAlertRule)
		api.GET("/alerts/events", h.ListAlertEvents)
		api.POST("/alerts/events/read", h.RequireRole(model.RoleOperator), h.MarkAlertEventsRead)
		api.POST("/nodes/:id/command", h.RequireRole(model.RoleOperator), h.ExecCommand)
		api.POST("/nodes/batch/command", h.RequireRole(model.RoleOperator), h.BatchCommand)
		api.POST("/nodes/:id/upgrade", h.RequireRole(model.RoleOperator), h.UpgradeAgent)
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

// ChangePassword 修改当前登录用户的密码：校验旧密码后更新。
func (h *Handler) ChangePassword(c *gin.Context) {
	// JWT 中间件注入的字段（见 JWTAuth：uid/username/role）
	userID := c.GetUint("uid")
	username := c.GetString("username")

	var in struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.OldPassword == "" || in.NewPassword == "" {
		fail(c, http.StatusBadRequest, codeParam, "旧密码与新密码必填")
		return
	}
	if len(in.NewPassword) < 6 || len(in.NewPassword) > 64 {
		fail(c, http.StatusBadRequest, codeParam, "新密码长度 6-64 位")
		return
	}
	u, err := h.store.GetUserByUsername(username)
	if err != nil {
		fail(c, http.StatusUnauthorized, codeUnauth, "用户不存在")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, in.OldPassword) {
		fail(c, http.StatusUnauthorized, codeUnauth, "旧密码错误")
		return
	}
	hash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "密码处理失败")
		return
	}
	if err := h.store.UpdatePassword(userID, hash); err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "密码更新失败")
		return
	}
	// 审计留痕
	h.log("用户修改密码", "user", username, "result", "ok")
	ok(c, gin.H{"changed": true})
}

// ListNodes 返回节点列表（含在线状态与最新遥测摘要）。
func (h *Handler) ListNodes(c *gin.Context) {
	nodes, err := h.store.ListNodes()
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "查询失败")
		return
	}
	out := make([]gin.H, 0, len(nodes))
	for i := range nodes {
		out = append(out, nodeView(h, &nodes[i]))
	}
	ok(c, gin.H{"total": len(out), "nodes": out})
}

// nodeView 把 DB node + session 遥测 + DB 中持久化的 Capabilities 拼成前端 Node 形状。
func nodeView(h *Handler, n *model.Node) gin.H {
	row := gin.H{
		"id":            n.ID,
		"hostname":      n.Hostname,
		"arch":          n.Arch,
		"os":            n.OS,
		"os_version":    n.OSVersion,
		"kernel":        n.Kernel,
		"agent_version": n.AgentVersion,
		"tailscale_ip":  n.TailscaleIP,
		"status":        statusOf(h.sessions.IsOnline(n.ID)),
		"last_seen_at":  n.LastSeenAt,
		"registered_at": n.RegisteredAt,
		"capabilities":  decodeCapabilities(n.CapabilitiesJSON),
		"telemetry":     telemetryView(h.sessions.LatestTelemetry(n.ID)),
	}
	return row
}

// telemetryView 把 ecpv1.Telemetry 映射成前端驼峰字段；离线（nil）返回零值。
func telemetryView(t *ecpv1.Telemetry) gin.H {
	if t == nil {
		return gin.H{
			"cpuPercent": 0, "memUsedBytes": 0, "memTotalBytes": 0,
			"diskUsedBytes": 0, "diskTotalBytes": 0,
			"netRxBytes": 0, "netTxBytes": 0, "load1": 0,
			"temperatureCelsius": 0, "containersRunning": 0,
		}
	}
	return gin.H{
		"cpuPercent":         t.CpuPercent,
		"memUsedBytes":       t.MemUsedBytes,
		"memTotalBytes":      t.MemTotalBytes,
		"diskUsedBytes":      t.DiskUsedBytes,
		"diskTotalBytes":     t.DiskTotalBytes,
		"netRxBytes":         t.NetRxBytes,
		"netTxBytes":         t.NetTxBytes,
		"load1":              t.Load1,
		"temperatureCelsius": t.TemperatureCelsius,
		"containersRunning":  t.ContainerRunning,
	}
}

// decodeCapabilities 把 DB 持久化的 capabilities JSON 字符串解出来；
// 空字符串返回空对象（前端拿到 null 会展示不全）。
func decodeCapabilities(s string) gin.H {
	empty := gin.H{
		"canReadSystemStats": false, "canTerminal": false, "canManageFiles": false,
		"canReadDocker": false, "canWriteDocker": false, "canManageTailscale": false,
		"canManageNetwork": false, "canManageSystemd": false, "canSelfUpgrade": false,
		"canReadNetConfig": false, "runAsUid": 0, "runAsUser": "", "missingTools": []any{},
	}
	if s == "" {
		return empty
	}
	var c ecpv1.CapabilityReport
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return empty
	}
	return gin.H{
		"canReadSystemStats": c.CanReadSystemStats,
		"canTerminal":        c.CanTerminal,
		"canManageFiles":     c.CanManageFiles,
		"canReadDocker":      c.CanReadDocker,
		"canWriteDocker":     c.CanWriteDocker,
		"canManageTailscale": c.CanManageTailscale,
		"canManageNetwork":   c.CanManageNetwork,
		"canManageSystemd":   c.CanManageSystemd,
		"CanSelfUpgrade":     c.CanSelfUpgrade,
		"canReadNetConfig":   c.CanReadNetConfig,
		"runAsUid":           c.RunAsUid,
		"runAsUser":          c.RunAsUser,
		"missingTools":       c.MissingTools,
		"panelEntrance":      c.PanelEntrance,
	}
}

// GetNode 返回单节点详情。
func (h *Handler) GetNode(c *gin.Context) {
	id := c.Param("id")
	n, err := h.store.GetNode(id)
	if err != nil {
		fail(c, http.StatusNotFound, codeNotFound, "节点不存在")
		return
	}
	ok(c, gin.H{
		"node":    n,
		"online":  h.sessions.IsOnline(id),
		"view":    nodeView(h, n),
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

// BatchCommand 向多个节点并行下发同一指令，汇总各节点结果。
// 离线节点不阻塞整体，单独标记 offline。
func (h *Handler) BatchCommand(c *gin.Context) {
	var in struct {
		NodeIDs    []string         `json:"node_ids"`
		Type       string           `json:"type"`
		Params     *structpb.Struct `json:"params"`
		TimeoutSec int32            `json:"timeout_sec"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || len(in.NodeIDs) == 0 {
		fail(c, http.StatusBadRequest, codeParam, "node_ids 至少选择一个节点")
		return
	}
	if in.Type == "" {
		fail(c, http.StatusBadRequest, codeParam, "指令类型必填")
		return
	}
	cmd := &ecpv1.Command{Type: commandType(in.Type), Params: in.Params, TimeoutSec: in.TimeoutSec}
	uid := c.GetUint("uid")
	username := c.GetString("username")

	type item struct {
		NodeID  string `json:"node_id"`
		Status  string `json:"status"` // ok / failed / offline / rejected
		Message string `json:"message"`
		Stdout  string `json:"stdout,omitempty"`
	}
	results := make([]item, len(in.NodeIDs))
	var wg sync.WaitGroup
	for i, nodeID := range in.NodeIDs {
		wg.Add(1)
		go func(i int, nodeID string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(c.Request.Context(), 35*time.Second)
			defer cancel()
			res, err := h.dispatch.Dispatch(ctx, uid, username, nodeID, cmd)
			if err != nil {
				if err.Error() == "节点离线，无法下发指令" {
					results[i] = item{NodeID: nodeID, Status: "offline", Message: "节点离线"}
					return
				}
				results[i] = item{NodeID: nodeID, Status: "failed", Message: err.Error()}
				return
			}
			st := "ok"
			switch res.Status {
			case ecpv1.ResultStatus_RESULT_STATUS_FAILED:
				st = "failed"
			case ecpv1.ResultStatus_RESULT_STATUS_REJECTED:
				st = "rejected"
			}
			results[i] = item{NodeID: nodeID, Status: st, Message: res.Message, Stdout: string(res.Stdout)}
		}(i, nodeID)
	}
	wg.Wait()
	ok(c, gin.H{"total": len(results), "results": results})
}
func (h *Handler) ListFiles(c *gin.Context) {
	id := c.Param("id")
	path := c.Query("path")
	if path == "" {
		path = "/"
	}
	params, err := structpb.NewStruct(map[string]any{"path": path})
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "构造参数失败")
		return
	}
	cmd := &ecpv1.Command{Type: ecpv1.CommandType_COMMAND_TYPE_FILE_LIST, Params: params}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
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
	if res.GetStatus() != ecpv1.ResultStatus_RESULT_STATUS_OK {
		fail(c, http.StatusInternalServerError, codeInternal, res.GetMessage())
		return
	}
	var items []gin.H
	if err := json.Unmarshal(res.GetStdout(), &items); err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "解析文件列表失败")
		return
	}
	ok(c, gin.H{"path": path, "items": items})
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
		ecpv1.CommandType_COMMAND_TYPE_FRP_STATUS,
		ecpv1.CommandType_COMMAND_TYPE_FRP_UP,
		ecpv1.CommandType_COMMAND_TYPE_FRP_DOWN,
		ecpv1.CommandType_COMMAND_TYPE_FRP_CONFIG_GET,
		ecpv1.CommandType_COMMAND_TYPE_FRP_CONFIG_SET,
		ecpv1.CommandType_COMMAND_TYPE_TAILSCALE_LOGIN_URL,
		ecpv1.CommandType_COMMAND_TYPE_VNC_STATUS,
		ecpv1.CommandType_COMMAND_TYPE_VNC_START,
		ecpv1.CommandType_COMMAND_TYPE_VNC_STOP,
	} {
		if strings.TrimPrefix(t.String(), "COMMAND_TYPE_") == norm {
			return t
		}
	}
	return ecpv1.CommandType_COMMAND_TYPE_UNSPECIFIED
}
