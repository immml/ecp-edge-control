// Package command 负责把控制台下发的指令通过 gRPC 流投递到节点，
// 并等待 Agent 回传结果，同时落指令台账（审计/回溯）。
package command

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/server/internal/session"
	"ecp.dev/ecp/server/internal/store"
	"ecp.dev/ecp/server/internal/store/model"
)

// 错误定义。
var (
	ErrOffline  = errors.New("节点离线，无法下发指令")
	ErrTimeout  = errors.New("指令超时未收到回执")
	defaultTimeout = 30 * time.Second
)

// Dispatcher 把指令下发到在线节点并等待结果。
type Dispatcher struct {
	sessions *session.Manager
	store    *store.Store
	log      func(string, ...any)
}

// New 构造指令分发器。
func New(sessions *session.Manager, st *store.Store, log func(string, ...any)) *Dispatcher {
	return &Dispatcher{sessions: sessions, store: st, log: log}
}

// Dispatch 下发一条指令并阻塞等待结果。trace_id 由本函数生成并全链路透传。
func (d *Dispatcher) Dispatch(ctx context.Context, userID uint, username, nodeID string, cmd *ecpv1.Command) (*ecpv1.CommandResult, error) {
	if !d.sessions.IsOnline(nodeID) {
		return nil, ErrOffline
	}

	traceID := uuid.NewString()[:8]
	cmd.TraceId = traceID

	timeout := defaultTimeout
	if cmd.TimeoutSec > 0 {
		timeout = time.Duration(cmd.TimeoutSec) * time.Second
	}

	ch := d.sessions.RegisterWaiter(traceID)
	defer d.sessions.CancelWaiter(traceID)

	msg := &ecpv1.ControlMessage{
		TraceId: traceID,
		Payload: &ecpv1.ControlMessage_Command{Command: cmd},
	}
	if err := d.sessions.Send(nodeID, msg); err != nil {
		return nil, fmt.Errorf("下发失败: %w", err)
	}

	// 先落一条 pending 台账
	_ = d.store.DB().Create(&model.Command{
		TraceID: traceID,
		NodeID:  nodeID,
		UserID:  userID,
		Type:    cmd.Type.String(),
		Params:  "", // params 含潜在敏感路径，暂不入审计明文；T5 可结构化
		Status:  "pending",
	})

	select {
	case res := <-ch:
		d.finish(traceID, res)
		return res, nil
	case <-time.After(timeout):
		d.finish(traceID, nil)
		return nil, ErrTimeout
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// finish 把结果写回指令台账。
func (d *Dispatcher) finish(traceID string, res *ecpv1.CommandResult) {
	status := "done"
	var output, privScript string
	if res != nil {
		status = res.Status.String()
		output = string(res.Stdout)
		privScript = res.PrivilegeScript
	} else {
		status = "timeout"
	}
	now := time.Now()
	_ = d.store.DB().Model(&model.Command{}).
		Where("trace_id = ?", traceID).
		Updates(map[string]any{
			"status":           status,
			"output":           output,
			"privilege_script": privScript,
			"finished_at":      &now,
		})
}
