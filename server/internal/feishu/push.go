// 飞书群机器人 Webhook 推送（自定义机器人，可选「签名校验」）。
//
// 与长连接（双向指令）共存：长连接用于接收用户指令并回复，
// 本文件用于控制面主动向飞书群推送（告警聚合、状态上报、上线通知）。
//
// 签名算法（飞书开放平台-自定义机器人-安全设置-签名校验）：
//   timestamp = 当前秒级时间戳（字符串）
//   string_to_sign = timestamp + "\n" + secret
//   sign = base64( HMAC-SHA256(string_to_sign, key=空) )
//   body = {"timestamp", "sign", "msg_type": "text", "content": {"text": ...}}
package feishu

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// PushConfig 是飞书群机器人 Webhook 推送配置。
type PushConfig struct {
	WebhookURL    string // 如 https://open.feishu.cn/open-apis/bot/v2/hook/<token>
	WebhookSecret string // 签名校验密钥（安全设置里开启后必填；未开启可留空）
}

// PushConfigFromEnv 从环境变量构造：ECP_FEISHU_WEBHOOK_URL / ECP_FEISHU_WEBHOOK_SECRET。
func PushConfigFromEnv() PushConfig {
	return PushConfig{
		WebhookURL:    os.Getenv("ECP_FEISHU_WEBHOOK_URL"),
		WebhookSecret: os.Getenv("ECP_FEISHU_WEBHOOK_SECRET"),
	}
}

// Enabled 是否配置了群推送。
func (p PushConfig) Enabled() bool { return p.WebhookURL != "" }

// ErrPushDisabled 表示未配置 webhook。
var ErrPushDisabled = fmt.Errorf("飞书群推送未配置（ECP_FEISHU_WEBHOOK_URL）")

// Notify 向群推送纯文本消息。url 为空返回 ErrPushDisabled；
// secret 非空时自动带上签名（与飞书「签名校验」安全设置对应），
// 机器人在群里时可校验通过；群机器人未开签名校验时 secret 应留空。
func Notify(cfg PushConfig, text string) error {
	if cfg.WebhookURL == "" {
		return ErrPushDisabled
	}
	body := map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": text},
	}
	if cfg.WebhookSecret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		sign := signFeishu(cfg.WebhookSecret, ts)
		body["timestamp"] = ts
		body["sign"] = sign
	}
	payload, _ := json.Marshal(body)
	resp, err := http.Post(cfg.WebhookURL, "application/json; charset=utf-8", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return fmt.Errorf("推送响应解析失败: %s", strings.TrimSpace(string(b)))
	}
	if out.Code != 0 {
		return fmt.Errorf("推送被拒 code=%d msg=%s", out.Code, out.Msg)
	}
	return nil
}

// signFeishu 计算飞书签名：base64(HMAC-SHA256(timestamp+"\n"+secret, key=""))。
func signFeishu(secret, timestamp string) string {
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(""))
	mac.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// NotifyWithLog 带日志的推送（供启动验证/告警接线使用）。
func NotifyWithLog(cfg PushConfig, log *slog.Logger, text string) {
	if !cfg.Enabled() {
		return
	}
	if err := Notify(cfg, text); err != nil {
		log.Warn("飞书群推送失败", "err", err)
		return
	}
	log.Info("飞书群推送成功", "text", truncate(text, 60))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}