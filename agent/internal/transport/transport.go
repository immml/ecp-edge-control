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
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/agent/internal/alert"
	"ecp.dev/ecp/agent/internal/caps"
	"ecp.dev/ecp/agent/internal/collector"
	"ecp.dev/ecp/agent/internal/config"
	"ecp.dev/ecp/agent/internal/cache"
	"ecp.dev/ecp/agent/internal/executor"
	"ecp.dev/ecp/agent/internal/register"
)

// Transport 管理 Agent 到控制面的连接。
type Transport struct {
	cfg         *config.Config
	log         *slog.Logger
	exec        *executor.Executor
	cache       *cache.Cache
	alertEngine *alert.Engine
	coll        *collector.Collector
	term        *terminalManager
}

// New 构造传输管理器。cache 用于本地遥测落库与告警记录；
// 构造时探测能力并实例化管理器/采集器/告警引擎。
func New(cfg *config.Config, log *slog.Logger, ch *cache.Cache) *Transport {
	c := caps.Probe()
	t := &Transport{
		cfg:         cfg,
		log:         log,
		exec:        executor.New(cfg),
		cache:       ch,
		alertEngine: alert.New(cfg, ch, log),
		coll:        collector.New(c),
		term:        newTerminalManager(),
	}
	return t
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
		go t.openTunnel(ctx, conn, id) // 常驻端口转发隧道（1Panel 等）
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

	// 绑定当前流：告警事件通过它上报（流重连后自动换绑）
	t.alertEngine.OnEvent = func(kind, message string) {
		t.sendEvent(stream, id.NodeID, kind, message)
	}

	// 首帧：心跳 + 能力上报
	s := caps.Probe()
	first := &ecpv1.AgentMessage{
		NodeId: id.NodeID,
		Payload: &ecpv1.AgentMessage_Heartbeat{
			Heartbeat: t.collectAndBuildHeartbeat(),
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

	// 终端上行发送器绑定当前流（流重连后自动换绑）
	t.term.send = func(d *ecpv1.TerminalData) error {
		return stream.Send(&ecpv1.AgentMessage{
			NodeId:  id.NodeID,
			Payload: &ecpv1.AgentMessage_TerminalData{TerminalData: d},
		})
	}

	// 单一常驻接收协程：只在这里调用 stream.Recv()，避免并发 Recv 破坏流。
	// 指令回执由该协程内的 handleCommand 通过同一流 Send，Send 与 Recv 分属不同方向，合规。
	recvErr := make(chan error, 1)
	go func() {
	for {
		msg, err := stream.Recv()
		if err != nil {
				recvErr <- err
				return
			}
			if cmd := msg.GetCommand(); cmd != nil {
				go t.handleCommand(stream, id, cmd)
			}
			if sync := msg.GetAlertRules(); sync != nil {
				if err := t.alertEngine.ApplyRules(sync.GetRulesYaml()); err != nil {
					t.log.Warn("规则同步失败", "err", err)
				} else {
					t.log.Info("已应用控制面下发的告警规则", "version", sync.GetVersion())
				}
			}
			if tc := msg.GetTerminal(); tc != nil {
				go t.term.Handle(tc)
			}
		}
	}()

	// 心跳定时器
	ticker := time.NewTicker(t.cfg.Telemetry.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-recvErr:
			return err
		case <-ticker.C:
			if err := t.sendHeartbeat(stream, id.NodeID); err != nil {
				t.alertEngine.RecordHeartbeat(false)
				return err
			}
			t.alertEngine.RecordHeartbeat(true)
		}
	}
}

// handleCommand 执行控制面下发的指令并通过同一流回传结果。
func (t *Transport) handleCommand(stream ecpv1.AgentService_CommandStreamClient, id *register.Identity, cmd *ecpv1.Command) {
	res := t.exec.Handle(cmd)
	res.TraceId = cmd.TraceId
	_ = stream.Send(&ecpv1.AgentMessage{
		NodeId:  id.NodeID,
		Payload: &ecpv1.AgentMessage_Result{Result: res},
	})
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
		TailscaleIp:  detectTailscaleIP(),
	}
}

// detectTailscaleIP 探测本节点 Tailscale IPv4（100.64.0.0/10 段）。
// 优先 tailscale CLI，失败则扫网卡兜底；都不是返回空串。
func detectTailscaleIP() string {
	if out, err := exec.Command("tailscale", "ip", "-4").Output(); err == nil {
		first := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if strings.HasPrefix(first, "100.") {
			return first
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && ip.To4() != nil && isTailscaleIP(ip) {
				return ip.String()
			}
		}
	}
	return ""
}

// isTailscaleIP 判断是否 CGNAT 段 100.64.0.0/10。
func isTailscaleIP(ip net.IP) bool {
	b := ip.To4()
	if b == nil {
		return false
	}
	return b[0] == 100 && b[1] >= 64 && b[1] <= 127
}

// collectAndBuildHeartbeat 采集一次遥测并构造心跳帧。
func (t *Transport) collectAndBuildHeartbeat() *ecpv1.Heartbeat {
	tele := t.coll.Collect()
	// 本地落缓存：控制面离线期间也能追溯历史（UploadBacklog 补传）
	if t.cache != nil && tele != nil {
		_ = t.cache.AppendSample(&cache.Sample{
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
		// 本地阈值评估（控制面不在线也能告警）
		t.alertEngine.Evaluate(tele)
	}
	return &ecpv1.Heartbeat{
		NodeId:           "",
		Telemetry:        tele,
		AgentVersion:     "0.1.0",
		ControlPlaneSeen: true,
	}
}

// sendHeartbeat 发送一次带实时遥测的心跳。
func (t *Transport) sendHeartbeat(stream ecpv1.AgentService_CommandStreamClient, nodeID string) error {
	hb := t.collectAndBuildHeartbeat()
	return stream.Send(&ecpv1.AgentMessage{
		NodeId:  nodeID,
		Payload: &ecpv1.AgentMessage_Heartbeat{Heartbeat: hb},
	})
}

// sendEvent 向控制面上报一条节点事件（告警触发等），流不可用时静默丢弃。
func (t *Transport) sendEvent(stream ecpv1.AgentService_CommandStreamClient, nodeID, kind, message string) {
	_ = stream.Send(&ecpv1.AgentMessage{
		NodeId: nodeID,
		Payload: &ecpv1.AgentMessage_Event{
			Event: &ecpv1.NodeEvent{
				Ts:      timestamppb.Now(),
				Kind:    kind,
				Message: message,
			},
		},
	})
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
		PanelEntrance:      s.PanelEntrance,
	}
}
