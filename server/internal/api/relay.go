// relay.go —— 紧急通道（Cloudflare Worker 中转）配置下发。
//
// 控制台登录后调用 GET /api/v1/relay/config 获取 relay 连接参数，
// 前端据此建立 wss 连接（/gui 房间）作为 Tailscale 直连不可用时的降级通道。
//
// 配置来源（优先级从高到低）：
//  1. 环境变量 ECP_RELAY_URL / ECP_RELAY_GUI_TOKEN（推荐，凭据不进配置文件）
//  2. 未配置 → enabled=false，前端不建立任何连接，功能透明关闭
package api

import (
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// RelayConfigResp 是下发前端的 relay 配置。
type RelayConfigResp struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	GUIToken  string `json:"gui_token"`
	WorkersDev string `json:"workers_dev,omitempty"`
}

// RelayConfig 返回紧急通道配置。需要 JWT 认证（在 api := r.Group("/api/v1") 内）。
func (h *Handler) RelayConfig(c *gin.Context) {
	url := os.Getenv("ECP_RELAY_URL")
	token := os.Getenv("ECP_RELAY_GUI_TOKEN")

	resp := RelayConfigResp{Enabled: false}
	if url != "" && token != "" {
		resp = RelayConfigResp{
			Enabled:    true,
			URL:        url,
			GUIToken:   token,
			WorkersDev: os.Getenv("ECP_RELAY_WORKERS_DEV"),
		}
	}
	c.JSON(http.StatusOK, resp)
}