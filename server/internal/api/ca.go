// 免鉴权 CA 分发端点：新节点首次接入没有任何凭据，需要先无鉴权拿到控制面
// 自签根证书（CA 是公开公钥，泄露无风险），之后 TLS 才能校验服务器。
package api

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
)

// ServeCA 返回控制面自签根证书（data/certs/ca.crt）。
//
//	GET /api/v1/ca.crt
//
// 供节点安装脚本（bootstrap.sh --ca-url）与人工信任使用；与 OTA 二进制同理免鉴权。
func (h *Handler) ServeCA(c *gin.Context) {
	path := filepath.Join(h.dataDir, "certs", "ca.crt")
	b, err := os.ReadFile(path)
	if err != nil {
		fail(c, http.StatusNotFound, codeNotFound, "CA 证书不存在")
		return
	}
	c.Data(http.StatusOK, "application/x-pem-file; charset=utf-8", b)
}