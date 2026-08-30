// VPN / Clash：导出 Clash 客户端配置（用于访问内网设备）。
//
// 服务端为网关上 xray-core（VMess+WS，配 Cloudflare Tunnel 暴露为
// https://vpn.<domain>:443），Clash 客户端加载本配置后，内网段走代理隧道。
package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// ClashConfigReq 生成 Clash 配置的请求参数。
type ClashConfigReq struct {
	Name   string `json:"name"`   // 节点名（默认 ecp-vpn）
	Server string `json:"server"` // 公网入口域名（vpn.yourdomain.com）
	Port   int    `json:"port"`   // 443
	UUID   string `json:"uuid"`   // xray 生成的 UUID
	Path   string `json:"path"`   // ws path（/ecp-vpn）
	// ExtraIPs 额外的内网网段（逗号分隔），默认 192.168.0.0/16, 172.16.0.0/12,
	// 10.0.0.0/8, 100.64.0.0/10（tailnet）
	ExtraIPs string `json:"extra_ips"`
}

// ClashConfig 生成 Clash yaml 文本。注意：响应为 text/plain，由前端保存为 .yaml。
func (h *Handler) ClashConfig(c *gin.Context) {
	var in ClashConfigReq
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, codeParam, "参数错误: "+err.Error())
		return
	}
	if in.Server == "" || in.UUID == "" {
		fail(c, http.StatusBadRequest, codeParam, "server 与 uuid 必填")
		return
	}
	if in.Port <= 0 {
		in.Port = 443
	}
	if in.Path == "" {
		in.Path = "/ecp-vpn"
	}
	if in.Name == "" {
		in.Name = "ecp-vpn"
	}

	yaml := buildClashYAML(in)
	c.Data(http.StatusOK, "application/x-yaml; charset=utf-8", []byte(yaml))
}

// buildClashYAML 组装 Clash 配置文本。
func buildClashYAML(in ClashConfigReq) string {
	ipRules := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"}
	if in.ExtraIPs != "" {
		for _, s := range strings.Split(in.ExtraIPs, ",") {
			if s = strings.TrimSpace(s); s != "" {
				ipRules = append(ipRules, s)
			}
		}
	}
	var b strings.Builder
	b.WriteString("# ECP VPN 自动生成\n")
	b.WriteString("mixed-port: 7890\n")
	b.WriteString("allow-lan: false\n")
	b.WriteString("mode: rule\n")
	b.WriteString("log-level: info\n\n")
	b.WriteString("proxies:\n")
	b.WriteString("  - name: \"" + in.Name + "\"\n")
	b.WriteString("    type: vmess\n")
	b.WriteString("    server: " + in.Server + "\n")
	b.WriteString(fmt.Sprintf("    port: %d\n", in.Port))
	b.WriteString("    uuid: " + in.UUID + "\n")
	b.WriteString("    alterId: 0\n")
	b.WriteString("    cipher: auto\n")
	b.WriteString("    udp: true\n")
	b.WriteString("    network: ws\n")
	b.WriteString("    ws-opts:\n")
	b.WriteString("      path: \"" + in.Path + "\"\n")
	b.WriteString("      headers:\n")
	b.WriteString("        Host: " + in.Server + "\n\n")
	b.WriteString("proxy-groups:\n")
	b.WriteString("  - name: \"VPN\"\n")
	b.WriteString("    type: select\n")
	b.WriteString("    proxies:\n")
	b.WriteString("      - DIRECT\n")
	b.WriteString("      - \"" + in.Name + "\"\n\n")
	b.WriteString("rules:\n")
	for _, ip := range ipRules {
		b.WriteString("  - IP-CIDR," + ip + ",VPN,no-resolve\n")
	}
	b.WriteString("  - MATCH,DIRECT\n")
	return b.String()
}