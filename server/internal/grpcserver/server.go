// Package grpcserver 实现控制面侧的 gRPC 接入层。
//
// 安全模型（见架构 v2 §8.2）：
//   - 服务端 TLS：VerifyClientCertIfGiven —— 因为 Register 方法放行（靠注册 Key 鉴权），
//     其余方法强制要求客户端证书，并校验 CN == node_id。
//   - 证书由内置 CA 签发，私钥不出节点，CN 绑定硬件指纹（经注册通道绑定）。
package grpcserver

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/yaml.v3"

	"github.com/google/uuid"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/server/internal/ca"
	"ecp.dev/ecp/server/internal/config"
	"ecp.dev/ecp/server/internal/session"
	"ecp.dev/ecp/server/internal/store"
	"ecp.dev/ecp/server/internal/store/model"
)

// sessionTTL 控制面判定节点离线的心跳窗口（秒）。
const sessionTTL = 90 * time.Second

// Server 是控制面 gRPC 服务。
type Server struct {
	ecpv1.UnimplementedAgentServiceServer
	store    *store.Store
	ca       *ca.CA
	cfg      *config.Config
	sessions *session.Manager
	log      *slog.Logger
}

// New 构造 gRPC 服务。
func New(st *store.Store, ca *ca.CA, cfg *config.Config, log *slog.Logger) *Server {
	return &Server{
		store:    st,
		ca:       ca,
		cfg:      cfg,
		sessions: session.New(sessionTTL),
		log:      log,
	}
}

// Sessions 返回会话管理器，供 REST 层与指令分发器共用。
func (s *Server) Sessions() *session.Manager { return s.sessions }

// Register 处理节点注册。这是唯一免客户端证书的入口。
//
// 用注册 Key 哈希 + 硬件指纹换取客户端证书；首次绑定与重认证走同一接口，
// 全程留审计。
func (s *Server) Register(ctx context.Context, req *ecpv1.RegisterRequest) (*ecpv1.RegisterResponse, error) {
	hash := sha256.Sum256([]byte(req.RegistrationKey))
	keyHash := hex.EncodeToString(hash[:])

	key, err := s.store.GetKeyByHash(keyHash)
	if err != nil {
		s.audit("", "registry.key.reject", "registration_key_not_found", "注册 Key 不存在")
		return &ecpv1.RegisterResponse{Accepted: false, RejectReason: "registration key invalid"}, nil
	}
	if key.RevokedAt != nil {
		s.audit("", "registry.key.reject", "key_revoked", "注册 Key 已吊销")
		return &ecpv1.RegisterResponse{Accepted: false, Reason: ecpv1.RegisterReason_REGISTER_REASON_KEY_REVOKED, RejectReason: "key revoked"}, nil
	}
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		s.audit("", "registry.key.reject", "key_expired", "注册 Key 已过期")
		return &ecpv1.RegisterResponse{Accepted: false, Reason: ecpv1.RegisterReason_REGISTER_REASON_KEY_EXPIRED, RejectReason: "key expired"}, nil
	}

	var (
		nodeID string
		reason ecpv1.RegisterReason
	)

	if key.BoundNodeID == "" {
		// 首次绑定
		nodeID = "n-" + uuid8()
		if err := s.store.BindKeyToNode(keyHash, nodeID); err != nil {
			s.log.Error("绑定 Key 失败", "err", err)
			return &ecpv1.RegisterResponse{Accepted: false, RejectReason: "internal error"}, nil
		}
		if err := s.store.BindFingerprint(nodeID, req.Fingerprint); err != nil {
			s.log.Error("绑定指纹失败", "err", err)
			return &ecpv1.RegisterResponse{Accepted: false, RejectReason: "internal error"}, nil
		}
		reason = ecpv1.RegisterReason_REGISTER_REASON_FIRST_BIND
		s.audit(nodeID, "node.register.first_bind", "ok", "首次绑定")
	} else {
		// 重认证：校验指纹一致
		fp, err := s.store.GetFingerprint(key.BoundNodeID)
		if err != nil {
			return &ecpv1.RegisterResponse{Accepted: false, RejectReason: "fingerprint lookup failed"}, nil
		}
		if fp.Fingerprint != req.Fingerprint {
			s.audit(key.BoundNodeID, "node.register.reject", "fingerprint_mismatch", "指纹不匹配，疑似 Key 泄露")
			return &ecpv1.RegisterResponse{
				Accepted:     false,
				NodeId:       key.BoundNodeID,
				Reason:       ecpv1.RegisterReason_REGISTER_REASON_FINGERPRINT_MISMATCH,
				RejectReason: "fingerprint mismatch",
			}, nil
		}
		nodeID = key.BoundNodeID
		reason = ecpv1.RegisterReason_REGISTER_REASON_REAUTH
		s.audit(nodeID, "node.register.reauth", "ok", "重认证")
	}

	ttl := 720 * time.Hour
	serial, certPEM, err := s.ca.SignClientCertReturnSerial(nodeID, req.Csr, ttl)
	if err != nil {
		s.log.Error("签发客户端证书失败", "err", err)
		return &ecpv1.RegisterResponse{Accepted: false, RejectReason: "sign failed: " + err.Error()}, nil
	}
	if err := s.store.IssueCredential(nodeID, serial, time.Now().Add(ttl)); err != nil {
		s.log.Error("登记凭证失败", "err", err)
	}

	node := &model.Node{
		ID:           nodeID,
		Hostname:     req.NodeInfo.Hostname,
		Arch:         req.NodeInfo.Arch,
		OS:           req.NodeInfo.Os,
		OSVersion:    req.NodeInfo.OsVersion,
		Kernel:       req.NodeInfo.Kernel,
		AgentVersion: req.NodeInfo.AgentVersion,
		TailscaleIP:  req.NodeInfo.TailscaleIp,
		Status:       model.StatusOnline,
		RegisteredAt: time.Now(),
	}
	if err := s.store.UpsertNode(node); err != nil {
		s.log.Error("写入节点失败", "err", err)
	}

	initCfg := s.buildInitialConfig()

	return &ecpv1.RegisterResponse{
		Accepted:      true,
		NodeId:        nodeID,
		Reason:        reason,
		ClientCert:    certPEM,
		CaCert:        s.ca.CertPEM(),
		InitialConfig: initCfg,
		CertExpiresAt: time.Now().Add(ttl).Unix(),
	}, nil
}

// CommandStream 是双向流：Agent 上报心跳与执行结果，控制面下发指令与配置。
func (s *Server) CommandStream(stream ecpv1.AgentService_CommandStreamServer) error {
	ctx := stream.Context()

	// 强制客户端证书：必须存在、由本 CA 签发且未过期
	nodeID, err := verifyClientCert(ctx)
	if err != nil {
		s.log.Warn("CommandStream 拒绝未授权连接", "err", err)
		return err
	}

	// 读取首帧（心跳或能力），拿到 node_id 并登记会话
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.NodeId != nodeID {
		s.log.Warn("CommandStream node_id 与证书 CN 不一致", "cert", nodeID, "msg", first.NodeId)
		return fmt.Errorf("node_id mismatch")
	}

	sess := s.sessions.Attach(nodeID, stream)
	defer s.sessions.Detach(nodeID)
	s.log.Info("节点接入", "node_id", nodeID)

	// 免注册直连（重启后带证书直接接入）时确保节点落库，控制台列表才可见
	if _, err := s.store.GetNode(nodeID); err != nil {
		if uerr := s.store.UpsertNode(&model.Node{
			ID:           nodeID,
			Status:       model.StatusOnline,
			AgentVersion: "unknown",
			RegisteredAt: time.Now(),
		}); uerr != nil {
			s.log.Warn("节点接入落库失败", "node_id", nodeID, "err", uerr)
		}
	}

	for {
		msg, err := stream.Recv()
		if err != nil {
			s.log.Info("节点断开", "node_id", nodeID, "err", err)
			return nil
		}
		s.sessions.Touch(nodeID)
		s.handleAgentMessage(sess, msg)
	}
}

// handleAgentMessage 处理 Agent 上行的各类消息。
func (s *Server) handleAgentMessage(sess *session.Session, msg *ecpv1.AgentMessage) {
	switch p := msg.Payload.(type) {
	case *ecpv1.AgentMessage_Heartbeat:
		s.sessions.PutTelemetry(msg.NodeId, p.Heartbeat.Telemetry)
		if p.Heartbeat.Telemetry != nil {
			if err := s.store.SaveTelemetry(msg.NodeId, p.Heartbeat.Telemetry); err != nil {
				s.log.Warn("遥测落库失败", "node_id", msg.NodeId, "err", err)
			}
		}
		if v := p.Heartbeat.AgentVersion; v != "" {
			if err := s.store.UpdateAgentVersion(msg.NodeId, v); err != nil {
				s.log.Warn("Agent 版本落库失败", "node_id", msg.NodeId, "err", err)
			}
		}
		ack := &ecpv1.ControlMessage{
			TraceId: msg.NodeId,
			Payload: &ecpv1.ControlMessage_HeartbeatAck{
				HeartbeatAck: &ecpv1.HeartbeatAck{
					ServerTs:         timestamppb.Now(),
					HasPendingConfig: false,
				},
			},
		}
		_ = sess.Stream.Send(ack)
	case *ecpv1.AgentMessage_Result:
		s.log.Info("收到指令结果", "node_id", msg.NodeId, "trace", p.Result.TraceId, "status", p.Result.Status)
		s.sessions.DeliverResult(p.Result.TraceId, p.Result)
	case *ecpv1.AgentMessage_Capabilities:
		s.log.Info("收到能力上报", "node_id", msg.NodeId)
		if data, err := json.Marshal(p.Capabilities); err == nil {
			if err := s.store.UpdateCapabilities(msg.NodeId, string(data)); err != nil {
				s.log.Warn("能力落库失败", "node_id", msg.NodeId, "err", err)
			}
		}
	case *ecpv1.AgentMessage_TerminalData:
		s.sessions.DeliverTerminal(p.TerminalData)
	case *ecpv1.AgentMessage_Event:
		s.log.Info("收到节点事件", "node_id", msg.NodeId, "kind", p.Event.Kind)
	}
}

// verifyClientCert 从流上下文提取并校验客户端证书：必须存在、由本 CA 签发、
// 未过期。
func verifyClientCert(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("missing peer info")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", fmt.Errorf("非 TLS 连接")
	}
	if len(tlsInfo.State.VerifiedChains) == 0 {
		return "", fmt.Errorf("客户端未提供证书")
	}
	leaf := tlsInfo.State.VerifiedChains[0][0]
	if leaf.NotAfter.Before(time.Now()) {
		return "", fmt.Errorf("客户端证书已过期")
	}
	return leaf.Subject.CommonName, nil
}

// buildInitialConfig 构造下发到节点的初始配置（YAML）。
func (s *Server) buildInitialConfig() []byte {
	type ep struct {
		Kind     string `yaml:"kind"`
		Addr     string `yaml:"addr"`
		Priority int    `yaml:"priority"`
	}
	type initCfg struct {
		AdvertiseEndpoints []ep `yaml:"advertise_endpoints"`
	}
	cfg := initCfg{}
	for i, addr := range s.cfg.Advertise.Endpoints {
		cfg.AdvertiseEndpoints = append(cfg.AdvertiseEndpoints, ep{Kind: "tailscale", Addr: addr, Priority: i})
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil
	}
	return data
}

// audit 写审计日志（轻量）。
func (s *Server) audit(nodeID, action, result, detail string) {
	_ = s.store.AppendAudit(&model.AuditLog{
		NodeID: nodeID,
		Action: action,
		Result: result,
		Detail: detail,
	})
}

// TLSConfig 构造服务端 TLS 配置：内置 CA 校验客户端证书（VerifyClientCertIfGiven）。
func (s *Server) TLSConfig() (*tls.Config, error) {
	serverCert, serverKey, err := s.ca.SignServerCert([]string{"localhost", "ecp-control"}, 8760*time.Hour)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(serverCert, serverKey)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(s.ca.CertPEM())
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}, nil
}

// uuid8 生成一个 8 位短 ID，用于节点 ID 前缀（人类可读）。
func uuid8() string { return uuid.NewString()[:8] }

// Serve 在指定地址启动 gRPC 服务（阻塞）。
func (s *Server) Serve(grpcAddr string) error {
	tlsCfg, err := s.TLSConfig()
	if err != nil {
		return err
	}
	lis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	ecpv1.RegisterAgentServiceServer(srv, s)
	s.log.Info("gRPC 监听", "addr", grpcAddr)
	return srv.Serve(lis)
}
