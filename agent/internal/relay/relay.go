// Package relay 实现 Agent 的紧急通道：出站 WSS 连接 Cloudflare Worker。
//
// 场景：Tailscale 主通道不可达时（跨网隔离、tailnet 故障、控制机异地），
// Agent 与控制机 GUI 各自出站连 Worker，Worker + Durable Object 按 node_id
// 分房间双向转发，实现 NAT 穿透下的紧急/临时远程控制。
//
// 与主通道（gRPC CommandStream）的关系：relay 是独立可选的辅助通道，
// 不替代主通道。启用时两者并存——主通道恢复后 GUI 自动切回，relay 仅作为
// 备用兜底，不承载完整遥测历史（遥测仍走主通道入库）。
//
// 协议帧（JSON，语义对齐 proto Command/CommandResult）：
//   下行 GUI->Agent: {type:"command", seq, node_id, cmd:{type, params, timeout_sec}}
//   上行 Agent->GUI: {type:"result",  seq, node_id, result:{status, stdout(base64), ...}}
//   心跳:            {type:"ping"} / {type:"pong"}
//   遥测:            {type:"telemetry", node_id, metrics:{...}}
//
// status 沿用 ResultStatus 数字枚举：OK=1 FAILED=2 NEEDS_PRIVILEGE=3
// TIMEOUT=4 REJECTED=5；stdout/stderr 沿用 protojson 行为用 base64。
package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/agent/internal/cache"
	"ecp.dev/ecp/agent/internal/caps"
	"ecp.dev/ecp/agent/internal/collector"
	"ecp.dev/ecp/agent/internal/config"
	"ecp.dev/ecp/agent/internal/executor"
	"ecp.dev/ecp/agent/internal/register"
)

// 与 executor.ResultStatus / proto 枚举对齐的数字常量（前端/协议共同遵守）。
const (
	statusOK            = 1
	statusFailed        = 2
	statusNeedsPrivilege = 3
	statusTimeout       = 4
	statusRejected      = 5
)

const (
	heartbeatInterval = 30 * time.Second
	telemetryInterval = 10 * time.Second
	writeTimeout      = 10 * time.Second
	maxBackoff        = 60 * time.Second
)

// Conn 是紧急通道连接器。
type Conn struct {
	cfg  *config.Config
	log  *slog.Logger
	exec *executor.Executor
	coll *collector.Collector
	ch   *cache.Cache
}

// New 构造紧急通道连接器。
func New(cfg *config.Config, log *slog.Logger, exec *executor.Executor, c *caps.Set, ch *cache.Cache) *Conn {
	return &Conn{cfg: cfg, log: log, exec: exec, coll: collector.New(c), ch: ch}
}

// Run 常驻运行：出站连接 Worker，处理 command 帧、回 result、推遥测与心跳。
// 阻塞直至 ctx 取消。断线自动退避重连。
func (c *Conn) Run(ctx context.Context) error {
	if !c.cfg.Relay.Enabled {
		return nil
	}
	if c.cfg.Relay.URL == "" {
		c.log.Warn("relay 已启用但未配置 url，紧急通道不启动")
		return nil
	}
	if c.cfg.Relay.Token == "" {
		c.log.Warn("relay 已启用但未配置 token（ECP_RELAY_TOKEN），紧急通道不启动")
		return nil
	}

	id, err := register.LoadOrCreate(c.cfg)
	if err != nil {
		return fmt.Errorf("加载身份失败: %w", err)
	}

	backoff := 1 * time.Second
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		ws, err := c.dial(ctx, id.NodeID)
		if err != nil {
			c.log.Warn("紧急通道连接失败", "url", c.cfg.Relay.URL, "err", err)
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = 1 * time.Second

		c.log.Info("紧急通道已连接", "url", c.cfg.Relay.URL)
		err = c.loop(ctx, ws, id.NodeID)
		_ = ws.Close()
		c.log.Info("紧急通道断开", "err", err)
		if !sleep(ctx, 1*time.Second) {
			return ctx.Err()
		}
	}
}

// dial 建立出站 WSS 连接，携带 Bearer token 与 node_id。
// scheme 只接受 wss（生产）；url 里写了 ws 也自动升级为 wss，避免明文传输。
func (c *Conn) dial(ctx context.Context, nodeID string) (*websocket.Conn, error) {
	u, err := url.Parse(c.cfg.Relay.URL)
	if err != nil {
		return nil, fmt.Errorf("relay url 非法: %w", err)
	}
	switch u.Scheme {
	case "ws", "wss":
		u.Scheme = "wss" // 强制 TSL，拒绝明文
	default:
		return nil, fmt.Errorf("relay url scheme 必须为 wss，收到 %q", u.Scheme)
	}
	q := u.Query()
	q.Set("node_id", nodeID)
	u.RawQuery = q.Encode()

	hdrs := map[string][]string{
		"Authorization": {"Bearer " + c.cfg.Relay.Token},
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	ws, _, err := websocket.DefaultDialer.DialContext(dialCtx, u.String(), hdrs)
	if err != nil {
		return nil, err
	}
	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(2 * heartbeatInterval))
		return nil
	})
	return ws, nil
}

// loop 单连接消息循环：读帧->分发；心跳与遥测定时器各一条。
// 读与写分属不同方向，gorilla 允许并发，安全。
func (c *Conn) loop(ctx context.Context, ws *websocket.Conn, nodeID string) error {
	readErr := make(chan error, 1)
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			c.handleFrame(ws, nodeID, data)
		}
	}()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	telemetry := time.NewTicker(telemetryInterval)
	defer telemetry.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case <-heartbeat.C:
			if err := c.writeJSON(ws, map[string]any{"type": "ping"}); err != nil {
				return err
			}
		case <-telemetry.C:
			if err := c.sendTelemetry(ws, nodeID); err != nil {
				return err
			}
		}
	}
}

// handleFrame 解析并分发一帧。
func (c *Conn) handleFrame(ws *websocket.Conn, nodeID string, data []byte) {
	var f struct {
		Type string `json:"type"`
		Seq  int64  `json:"seq"`
		Cmd  *struct {
			Type              string `json:"type"`
			Params            map[string]any `json:"params"`
			TimeoutSec        int32  `json:"timeout_sec"`
			RequiresPrivilege bool   `json:"requires_privilege"`
		} `json:"cmd"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		c.log.Warn("relay 帧解析失败", "err", err)
		return
	}

	switch f.Type {
	case "pong":
		// 心跳回执，无需处理（读超时已由 pong handler 管理）
		return
	case "command":
		if f.Cmd == nil {
			c.log.Warn("relay 收到空 command 帧")
			return
		}
		cmd, err := protoCommand(f.Cmd.Type, f.Cmd.Params, f.Cmd.TimeoutSec, f.Cmd.RequiresPrivilege)
		if err != nil {
			c.sendResult(ws, nodeID, f.Seq, &ecpv1.CommandResult{
				Status:  ecpv1.ResultStatus_RESULT_STATUS_FAILED,
				Message: err.Error(),
			})
			return
		}
		res := c.exec.Handle(cmd)
		c.sendResult(ws, nodeID, f.Seq, res)
	default:
		c.log.Debug("relay 忽略未知帧类型", "type", f.Type)
	}
}

// sendResult 把 executor 结果转成 result 帧回传（stdout/stderr 转 base64，对齐 protojson）。
func (c *Conn) sendResult(ws *websocket.Conn, nodeID string, seq int64, res *ecpv1.CommandResult) {
	frame := map[string]any{
		"type":    "result",
		"seq":     seq,
		"node_id": nodeID,
		"ts":      time.Now().Unix(),
		"result": map[string]any{
			"status":          int32(res.GetStatus()),
			"message":         res.GetMessage(),
			"stdout":          base64.StdEncoding.EncodeToString(res.GetStdout()),
			"stderr":          base64.StdEncoding.EncodeToString(res.GetStderr()),
			"exit_code":       res.GetExitCode(),
			"privilege_script": res.GetPrivilegeScript(),
			"privilege_hint":  res.GetPrivilegeHint(),
			"duration_ms":     res.GetDurationMs(),
		},
	}
	if err := c.writeJSON(ws, frame); err != nil {
		c.log.Warn("relay 回传结果失败", "err", err)
	}
}

// sendTelemetry 推送实时遥测（低频，供紧急模式下 GUI 直看状态），
// 同时落本地缓存：控制面恢复后经 UploadBacklog 补传入库（沿用 Telemetry 表）。
func (c *Conn) sendTelemetry(ws *websocket.Conn, nodeID string) error {
	tele := c.coll.Collect()
	if tele == nil {
		return nil
	}
	// 本地落缓存（与主通道心跳 collectAndBuildHeartbeat 对齐），幂等可重入
	if c.ch != nil {
		_ = c.ch.AppendSample(&cache.Sample{
			Ts:                 time.Now(),
			CPUPercent:         tele.GetCpuPercent(),
			MemTotalBytes:      tele.GetMemTotalBytes(),
			MemUsedBytes:       tele.GetMemUsedBytes(),
			DiskTotalBytes:     tele.GetDiskTotalBytes(),
			DiskUsedBytes:      tele.GetDiskUsedBytes(),
			NetRxBytes:         tele.GetNetRxBytes(),
			NetTxBytes:         tele.GetNetTxBytes(),
			Load1:              tele.GetLoad1(),
			TemperatureCelsius: tele.GetTemperatureCelsius(),
			ContainersRunning:  tele.GetContainerRunning(),
		})
	}
	return c.writeJSON(ws, map[string]any{
		"type":    "telemetry",
		"node_id": nodeID,
		"ts":      time.Now().Unix(),
		"metrics": map[string]any{
			"cpu":       tele.GetCpuPercent(),
			"mem_used":  tele.GetMemUsedBytes(),
			"mem_total": tele.GetMemTotalBytes(),
			"load1":     tele.GetLoad1(),
			"temp":      tele.GetTemperatureCelsius(),
			"containers": tele.GetContainerRunning(),
		},
	})
}

func (c *Conn) writeJSON(ws *websocket.Conn, v any) error {
	_ = ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	return ws.WriteJSON(v)
}

// protoCommand 把 relay 帧里的 cmd（字符串类型名+参数）映射为 proto Command。
// 复用控制面 commandType 语义：类型名取 COMMAND_TYPE_ 后缀（如 "SHELL"）。
func protoCommand(typeName string, params map[string]any, timeoutSec int32, requiresPriv bool) (*ecpv1.Command, error) {
	ct, ok := ecpv1.CommandType_value["COMMAND_TYPE_"+strings.ToUpper(typeName)]
	if !ok || ct == int32(ecpv1.CommandType_COMMAND_TYPE_UNSPECIFIED) {
		return nil, fmt.Errorf("不支持的指令类型: %s", typeName)
	}

	var p *structpb.Struct
	if len(params) > 0 {
		data, _ := json.Marshal(params)
		p = &structpb.Struct{}
		if err := protojson.Unmarshal(data, p); err != nil {
			return nil, fmt.Errorf("参数序列化失败: %w", err)
		}
	}

	return &ecpv1.Command{
		TraceId:           fmt.Sprintf("relay-%d", time.Now().UnixNano()),
		Type:              ecpv1.CommandType(ct),
		Params:            p,
		TimeoutSec:        timeoutSec,
		RequiresPrivilege: requiresPriv,
	}, nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > maxBackoff {
		next = maxBackoff
	}
	// 加一点抖动，避免多 Agent 同时重连打满 Worker
	return next + time.Duration(rand.Int63n(int64(time.Second)))
}