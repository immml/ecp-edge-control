// Package transport 负责 Agent 与控制面的链路：端点探测、注册、mTLS 连接、
// 退避重连、心跳上报与指令收发。Agent 常在线，控制面按需上线，
// 因此断线后必须能自动重连——这是砍掉 Worker 后的核心简化前提。
package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/agent/internal/caps"
	"ecp.dev/ecp/agent/internal/config"
	"ecp.dev/ecp/agent/internal/executor"
	"ecp.dev/ecp/agent/internal/register"
)

// Transport 管理 Agent 到控制面的连接。
type Transport struct {
	cfg  *config.Config
	log  *slog.Logger
	exec *executor.Executor
}

// New 构造传输管理器。
func New(cfg *config.Config, log *slog.Logger) *Transport {
	return &Transport{cfg: cfg, log: log, exec: executor.New()}
}

// Run 是 Agent 常驻主循环。阻塞直至 ctx 取消。会自动注册、建流、重连。
func (t *Transport) Run(ctx context.Context) error {
	id, err := register.LoadOrCreate(t.cfg)
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

		eps := t.candidateEndpoints()
		if len(eps) == 0 {
			t.log.Warn("没有可用的控制面端点，等待重试", "timeout", backoff)
			t.sleep(backoff)
			backoff = t.nextBackoff(backoff)
			continue
		}

		conn, err := t.dial(ctx, eps[0], id)
		if err != nil {
			t.log.Warn("连接控制面失败", "addr", eps[0], "err", err)
			t.sleep(backoff)
			backoff = t.nextBackoff(backoff)
			continue
		}
		backoff = 1 * time.Second // 连上即重置退避

		t.log.Info("已连接到控制面", "addr", eps[0])
		err = t.streamLoop(ctx, conn, id)
		t.log.Info("连接断开", "err", err)
		_ = conn.Close()
	}
}

// candidateEndpoints 合并种子端点与本地已知端点，返回待探测的地址列表。
func (t *Transport) candidateEndpoints() []string {
	out := append([]string{}, t.cfg.ControlPlane.Endpoints...)
	// 已下发的 known_endpoints 优先（控制面换了地址也能自愈）
	if b, err := os.ReadFile(t.cfg.ControlPlane.KnownEndpointsFile); err == nil {
		var known struct {
			Endpoints []struct {
				Addr string `json:"addr"`
			} `json:"known_endpoints"`
		}
		if json.Unmarshal(b, &known) == nil {
			for _, e := range known.Endpoints {
				out = append([]string{e.Addr}, out...) // 已知地址前置
			}
		}
	}
	return out
}

// dial 建立 TLS 连接（可选客户端证书）。
func (t *Transport) dial(ctx context.Context, addr string, id *register.Identity) (*grpc.ClientConn, error) {
	tlsCfg := &tls.Config{ServerName: "ecp-control"}
	if id.CAPEM != nil {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM(id.CAPEM) {
			tlsCfg.RootCAs = pool
		}
	}
	if id.CertPEM != nil && id.Key != nil {
		kc, err := tls.X509KeyPair(id.CertPEM, marshalKey(id.Key))
		if err == nil {
			tlsCfg.Certificates = []tls.Certificate{kc}
		}
	}
	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return grpc.DialContext(ctx2, addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)), grpc.WithBlock())
}

// streamLoop 建立 CommandStream，首次无证书则先注册，然后心跳 + 指令收发。
func (t *Transport) streamLoop(ctx context.Context, conn *grpc.ClientConn, id *register.Identity) error {
	client := ecpv1.NewAgentServiceClient(conn)

	if id.CertPEM == nil {
		if err := t.register(client, id); err != nil {
			return fmt.Errorf("注册失败: %w", err)
		}
	}

	stream, err := client.CommandStream(ctx)
	if err != nil {
		return err
	}

	// 首帧：心跳 + 能力上报
	s := caps.Probe()
	first := &ecpv1.AgentMessage{
		NodeId: id.NodeID,
		Payload: &ecpv1.AgentMessage_Heartbeat{
			Heartbeat: t.newHeartbeat(),
		},
	}
	if err := stream.Send(first); err != nil {
		return err
	}

	// 能力上报（独立帧）
	_ = stream.Send(&ecpv1.AgentMessage{
		NodeId:  id.NodeID,
		Payload: &ecpv1.AgentMessage_Capabilities{Capabilities: capsToProto(s)},
	})

	// 心跳定时器
	ticker := time.NewTicker(t.cfg.Telemetry.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := stream.Send(&ecpv1.AgentMessage{
				NodeId:  id.NodeID,
				Payload: &ecpv1.AgentMessage_Heartbeat{Heartbeat: t.newHeartbeat()},
			}); err != nil {
				return err
			}
		default:
			// 非阻塞读取服务端下发（本桩不请求指令，仅心跳）
		}

		// 尝试接收下行消息（带超时，避免永久阻塞）
		recvCh := make(chan error, 1)
		go func() {
			_, err := stream.Recv()
			recvCh <- err
		}()
		select {
		case err := <-recvCh:
			if err != nil {
				return err
			}
			// 收到下行（T3 接入指令分发），本期忽略
		case <-time.After(200 * time.Millisecond):
			// 心跳周期内无事发生，继续
		}
	}
}

// register 调用 Register RPC 完成首次签发并落盘证书。
func (t *Transport) register(client ecpv1.AgentServiceClient, id *register.Identity) error {
	csr, err := id.CSR()
	if err != nil {
		return err
	}
	fp, err := register.Fingerprint()
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	req := &ecpv1.RegisterRequest{
		RegistrationKey: t.registrationKey(),
		Fingerprint:     fp,
		Csr:             csr,
		NodeInfo:        t.nodeInfo(host),
		Capabilities:    capsToProto(caps.Probe()),
	}
	resp, err := client.Register(context.Background(), req)
	if err != nil {
		return err
	}
	if !resp.Accepted {
		return fmt.Errorf("注册被拒绝: %s (%s)", resp.RejectReason, resp.Reason)
	}
	if err := id.ApplyResponse(resp, t.cfg); err != nil {
		return err
	}
	t.log.Info("注册成功", "node_id", id.NodeID, "reason", resp.Reason)
	return nil
}

func (t *Transport) registrationKey() string {
	v, _ := t.cfg.RegistrationKeyValue()
	return v
}

func (t *Transport) nodeInfo(host string) *ecpv1.NodeInfo {
	return &ecpv1.NodeInfo{
		Hostname:     host,
		Arch:         runtime.GOARCH,
		Os:           runtime.GOOS,
		AgentVersion: "0.1.0",
	}
}

func (t *Transport) newHeartbeat() *ecpv1.Heartbeat {
	return &ecpv1.Heartbeat{
		NodeId:        "",
		AgentVersion:  "0.1.0",
		UptimeSec:     0,
		ControlPlaneSeen: true,
	}
}

func (t *Transport) sleep(d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-context.Background().Done():
	}
}

func (t *Transport) nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > 30*time.Second {
		next = 30 * time.Second
	}
	return next
}

// marshalKey 把 EC 私钥编码为 PEM，供 tls.X509KeyPair 使用。
func marshalKey(key *ecdsa.PrivateKey) []byte {
	b, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b})
}

// capsToProto 把能力探测结果映射到 proto。
func capsToProto(s *caps.Set) *ecpv1.CapabilityReport {
	return &ecpv1.CapabilityReport{
		CanReadSystemStats: s.CanReadSystemStats,
		CanTerminal:        s.CanTerminal,
		CanManageFiles:     s.CanManageFiles,
		CanReadDocker:      s.CanReadDocker,
		CanWriteDocker:     s.CanWriteDocker,
		CanManageTailscale: s.CanManageTailscale,
		CanManageNetwork:   s.CanManageNetwork,
		CanManageSystemd:   s.CanManageSystemd,
		CanSelfUpgrade:     s.CanSelfUpgrade,
		CanReadNetConfig:   s.CanReadNetConfig,
		RunAsUid:           int32(s.RunAsUID),
		RunAsUser:          s.RunAsUser,
		MissingTools:       s.MissingTools,
	}
}
