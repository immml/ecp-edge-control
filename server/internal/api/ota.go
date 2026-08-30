package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/structpb"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// otaBinariesDir 返回二进制存放目录（DataDir/binaries）。
func (h *Handler) otaBinariesDir() string {
	return filepath.Join(h.dataDir, "binaries")
}

// UploadBinary 上传 Agent 二进制（multipart file=xxx）。
//
// 存到 DataDir/binaries/<filename>，返回文件名与 SHA256（供升级时校验）。
func (h *Handler) UploadBinary(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		fail(c, http.StatusBadRequest, codeParam, "缺少 file 字段")
		return
	}
	name := filepath.Base(file.Filename)
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		fail(c, http.StatusBadRequest, codeParam, "非法文件名")
		return
	}
	dir := h.otaBinariesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "创建目录失败")
		return
	}
	dst := filepath.Join(dir, name)
	if err := c.SaveUploadedFile(file, dst); err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "保存失败: "+err.Error())
		return
	}
	// SHA256
	data, err := os.ReadFile(dst)
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "读取失败")
		return
	}
	sum := sha256.Sum256(data)
	ok(c, gin.H{
		"name":   name,
		"sha256": hex.EncodeToString(sum[:]),
		"size":   len(data),
	})
	h.log("上传 Agent 二进制", name, "size", len(data))
}

// ServeBinary 提供二进制下载端点（节点 OTA 拉取用）。
//
// 仅限 Tailscale 内网静态文件，不承载鉴权（文件为自有 Agent 二进制，非机密）。
func (h *Handler) ServeBinary(c *gin.Context) {
	name := filepath.Base(c.Param("name"))
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		fail(c, http.StatusBadRequest, codeParam, "非法文件名")
		return
	}
	path := filepath.Join(h.otaBinariesDir(), name)
	if _, err := os.Stat(path); err != nil {
		fail(c, http.StatusNotFound, codeNotFound, "二进制不存在")
		return
	}
	c.File(path)
}

// UpgradeAgent 下发 Agent 在线升级指令。
//
// body: {"binary": "ecp-agent-linux-arm64"}（先通过 UploadBinary 上传）。
// 升级会重启 Agent 进程，指令结果可能收不到——前端按"升级中"处理。
func (h *Handler) UpgradeAgent(c *gin.Context) {
	id := c.Param("id")
	var in struct {
		Binary string `json:"binary"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Binary == "" {
		fail(c, http.StatusBadRequest, codeParam, "缺少 binary 参数")
		return
	}
	name := filepath.Base(in.Binary)
	path := filepath.Join(h.otaBinariesDir(), name)
	data, err := os.ReadFile(path)
	if err != nil {
		fail(c, http.StatusNotFound, codeNotFound, "二进制未上传，请先上传")
		return
	}
	sum := sha256.Sum256(data)
	port := h.httpsPort
	if port == "" {
		port = "8443"
	}
	url := fmt.Sprintf("https://%s:%s/api/v1/agent/binaries/%s", h.advertiseIP, port, name)
	if h.advertiseIP == "" {
		fail(c, http.StatusInternalServerError, codeInternal, "控制面未配置通告地址（advertise.endpoints）")
		return
	}

	params := map[string]any{
		"url":    url,
		"sha256": hex.EncodeToString(sum[:]),
	}
	sp, err := structpb.NewStruct(params)
	if err != nil {
		fail(c, http.StatusInternalServerError, codeInternal, "构造参数失败")
		return
	}
	cmd := &ecpv1.Command{Type: ecpv1.CommandType_COMMAND_TYPE_AGENT_UPGRADE, Params: sp, TimeoutSec: 120}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 130*time.Second)
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
	// 升级重启 agent，结果大概率是超时/断连——返回"已下发"语义
	status := res.GetStatus()
	if status == ecpv1.ResultStatus_RESULT_STATUS_NEEDS_PRIVILEGE {
		ok(c, gin.H{
			"upgrading":        false,
			"needs_privilege":  true,
			"privilege_script": res.GetPrivilegeScript(),
		})
		return
	}
	h.log("下发 Agent 升级", id, "binary", name, "sha256", hex.EncodeToString(sum[:])[:12])
	ok(c, gin.H{"upgrading": true, "url": url, "sha256": hex.EncodeToString(sum[:])})
}
