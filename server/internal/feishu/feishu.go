// Package feishu 实现飞书机器人双向指令。
//
// 单向告警（agent 侧 FeishuWebhook 推送）已在 agent 实现；本包补上反向：
// 用户在飞书里 @机器人 发指令 → 控制面执行 → 结果回推飞书。
//
// 接入方式：飞书开放平台**长连接模式**（官方 Go SDK 内置 WebSocket 全双工通道），
// 免公网回调 URL、免验签解密，依赖控制面进程保持长连接订阅 im.message.receive_v1。
// 这正好符合"控制面按需上线、常驻时即可用"的形态。
//
// 指令语法：
//   @机器人 help                      → 指令帮助
//   @机器人 status                    → 节点在线状态总览
//   @机器人 exec <node_id> <cmd>      → 对指定节点执行 shell 命令并回执
//   @机器人 vnc <node_id>             → 查看节点 VNC 状态（运行/停止）
//
// 鉴权：app_id/app_secret 换 tenant_access_token（SDK 内置）；消息处理支持
// 用户 open_id 白名单（Config.AllowedUsers，空=不限制）。
package feishu

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"google.golang.org/protobuf/types/known/structpb"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/server/internal/command"
	"ecp.dev/ecp/server/internal/store"
)

// Config 是飞书双向模块配置（全部来自环境变量，凭据不落盘）。
type Config struct {
	AppID     string
	AppSecret string
	// 允许执行指令的用户（飞书 open_id 白名单；空=不限制）
	AllowedUsers []string
}

// App 是飞书双向模块的总控。
type App struct {
	cfg    Config
	disp   *command.Dispatcher
	st     *store.Store
	log    *slog.Logger
	client *lark.Client // 发送消息用（与长连接共用同一应用凭据）
}

// ConfigFromEnv 从环境变量构造配置：
//   ECP_FEISHU_APP_ID / ECP_FEISHU_APP_SECRET（必须）
//   ECP_FEISHU_ALLOWED_USERS（可选，逗号分隔的 open_id 白名单；空=不限制）
func ConfigFromEnv() Config {
	cfg := Config{
		AppID:     os.Getenv("ECP_FEISHU_APP_ID"),
		AppSecret: os.Getenv("ECP_FEISHU_APP_SECRET"),
	}
	if v := os.Getenv("ECP_FEISHU_ALLOWED_USERS"); v != "" {
		for _, u := range strings.Split(v, ",") {
			if u = strings.TrimSpace(u); u != "" {
				cfg.AllowedUsers = append(cfg.AllowedUsers, u)
			}
		}
	}
	return cfg
}

// New 构造飞书双向模块。未配置 AppID/AppSecret 时返回 nil（功能关闭）。
func New(cfg Config, disp *command.Dispatcher, st *store.Store, log *slog.Logger) *App {
	if cfg.AppID == "" || cfg.AppSecret == "" {
		log.Info("飞书双向未启用（缺少 ECP_FEISHU_APP_ID / ECP_FEISHU_APP_SECRET）")
		return nil
	}
	return &App{cfg: cfg, disp: disp, st: st, log: log}
}

// Run 启动长连接，阻塞直至 ctx 取消；SDK 内部负责断线重连。
func (a *App) Run(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.client = lark.NewClient(a.cfg.AppID, a.cfg.AppSecret)
	a.log.Info("飞书双向通道启动", "app_id", a.cfg.AppID)

	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(a.handleMessage)

	wsCli := larkws.NewClient(a.cfg.AppID, a.cfg.AppSecret,
		larkws.WithEventHandler(handler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)
	a.log.Info("飞书长连接建立中（开放平台事件订阅需切换为「使用长连接接收事件」）")
	return wsCli.Start(ctx)
}

// handleMessage 处理 im.message.receive_v1（私聊 / 群聊 @机器人）。
func (a *App) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	ev := event.Event
	if ev == nil || ev.Message == nil {
		return nil
	}
	openID := ""
	if ev.Sender != nil && ev.Sender.SenderId != nil && ev.Sender.SenderId.OpenId != nil {
		openID = *ev.Sender.SenderId.OpenId
	}
	chatID := ""
	if ev.Message.ChatId != nil {
		chatID = *ev.Message.ChatId
	}
	msgType := ""
	if ev.Message.MessageType != nil {
		msgType = *ev.Message.MessageType
	}
	content := ""
	if ev.Message.Content != nil {
		content = *ev.Message.Content
	}

	// 只处理文本消息
	if msgType != "text" {
		return nil
	}

	// 权限：open_id 白名单
	if len(a.cfg.AllowedUsers) > 0 {
		allowed := false
		for _, u := range a.cfg.AllowedUsers {
			if u == openID {
				allowed = true
				break
			}
		}
		if !allowed {
			a.reply(chatID, "你没有权限执行该指令")
			return nil
		}
	}

	text := extractText(content)
	if text == "" {
		return nil
	}
	a.log.Info("收到飞书指令", "from", openID, "chat", chatID, "text", text)
	a.dispatch(chatID, text)
	return nil
}

// extractText 从飞书 text 消息 content JSON 中提取纯文本。
func extractText(contentJSON string) string {
	var c struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(contentJSON), &c) == nil && strings.TrimSpace(c.Text) != "" {
		return strings.TrimSpace(c.Text)
	}
	return strings.TrimSpace(contentJSON)
}

// dispatch 解析并执行指令。
func (a *App) dispatch(chatID, text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		a.reply(chatID, "指令为空。发送 help 查看用法。")
		return
	}
	// 去掉 @机器人 前缀（飞书文本消息里 @ 是 <at user_id="..."></at> 形式）
	if strings.HasPrefix(parts[0], "<at") || strings.HasPrefix(parts[0], "@") {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		a.reply(chatID, "发送 help 查看用法。")
		return
	}

	switch strings.ToLower(parts[0]) {
	case "help", "帮助":
		a.reply(chatID, helpText())
	case "status", "状态":
		a.handleStatus(chatID)
	case "exec", "执行":
		if len(parts) < 3 {
			a.reply(chatID, "用法：exec <node_id> <command>")
			return
		}
		a.reply(chatID, fmt.Sprintf("[执行] %s: %s（请稍候…）", parts[1], strings.Join(parts[2:], " ")))
		go a.execAndReply(chatID, parts[1], strings.Join(parts[2:], " "))
	case "vnc":
		if len(parts) < 2 {
			a.reply(chatID, "用法：vnc <node_id>")
			return
		}
		a.handleVnc(chatID, parts[1])
	default:
		a.reply(chatID, "未知指令 "+parts[0]+"。发送 help 查看用法。")
	}
}

// handleStatus 查询节点在线状态总览。
func (a *App) handleStatus(chatID string) {
	if a.st == nil {
		a.reply(chatID, "状态查询不可用")
		return
	}
	nodes, err := a.st.ListNodes()
	if err != nil {
		a.reply(chatID, "节点查询失败: "+err.Error())
		return
	}
	if len(nodes) == 0 {
		a.reply(chatID, "当前没有接入节点")
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("节点总数 %d：\n", len(nodes)))
	for _, n := range nodes {
		b.WriteString(fmt.Sprintf("  %s | %s | %s\n", n.ID, n.Hostname, n.Status))
	}
	a.reply(chatID, b.String())
}

// handleExec 执行 shell 命令（Dispatch 阻塞，放在 goroutine 里）。
func (a *App) execAndReply(chatID, nodeID, cmdStr string) {
	if a.disp == nil {
		a.reply(chatID, "指令分发不可用")
		return
	}
	ctx := context.Background()
	params, _ := structpb.NewStruct(map[string]any{"command": cmdStr})
	cmd := &ecpv1.Command{
		Type:       ecpv1.CommandType_COMMAND_TYPE_SHELL,
		Params:     params,
		TimeoutSec: 30,
	}
	res, err := a.disp.Dispatch(ctx, 0, "feishu", nodeID, cmd)
	if err != nil {
		a.reply(chatID, fmt.Sprintf("[失败] %s: %v", nodeID, err))
		return
	}
	// stdout 是 base64（protojson bytes 序列化），解码后回推
	stdout := res.GetStdout()
	if b, err := base64.StdEncoding.DecodeString(string(stdout)); err == nil {
		stdout = b
	}
	if len(stdout) > 1500 {
		stdout = stdout[:1500]
	}
	a.reply(chatID, fmt.Sprintf("[回执 %s] status=%d exit=%d\n%s", nodeID, int(res.GetStatus()), int(res.GetExitCode()), string(stdout)))
}

// handleVnc 查看节点 VNC 运行状态。
func (a *App) handleVnc(chatID, nodeID string) {
	if a.disp == nil {
		a.reply(chatID, "指令分发不可用")
		return
	}
	cmd := &ecpv1.Command{Type: ecpv1.CommandType_COMMAND_TYPE_VNC_STATUS, TimeoutSec: 10}
	res, err := a.disp.Dispatch(context.Background(), 0, "feishu", nodeID, cmd)
	if err != nil {
		a.reply(chatID, fmt.Sprintf("[失败] %s: %v", nodeID, err))
		return
	}
	a.reply(chatID, fmt.Sprintf("[VNC %s] status=%d\n%s", nodeID, int(res.GetStatus()), string(res.GetMessage())))
}

// reply 向飞书会话回推文本消息。
func (a *App) reply(chatID, text string) {
	if a.client == nil {
		a.log.Warn("飞书回推失败（client 未初始化）", "chat", chatID, "text", text)
		return
	}
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		a.log.Warn("飞书回推序列化失败", "err", err)
		return
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(string(content)).
			Build()).
		Build()
	resp, err := a.client.Im.V1.Message.Create(context.Background(), req)
	if err != nil {
		a.log.Warn("飞书回推失败", "err", err)
		return
	}
	if !resp.Success() {
		a.log.Warn("飞书回推被拒", "code", resp.Code, "msg", resp.Msg)
	}
}

func helpText() string {
	return "可用指令：\n" +
		"  status             节点在线状态\n" +
		"  exec <node> <cmd>  对节点执行 shell 命令\n" +
		"  vnc <node>         查看节点 VNC 状态\n" +
		"  help               本帮助"
}