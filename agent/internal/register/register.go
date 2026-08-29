// Package register 负责 Agent 的身份与注册。
//
// 职责：首次运行时生成密钥对（私钥不出节点），用注册 Key + 硬件指纹换取
// 客户端证书；重连时直接用本地已有证书。证书落盘到 /etc/ecp 下，权限 0600。
package register

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"strings"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
	"ecp.dev/ecp/agent/internal/config"
)

// Identity 是 Agent 的身份材料。私钥只在本地，绝不外传。
type Identity struct {
	NodeID    string
	Key       *ecdsa.PrivateKey
	CertPEM   []byte // 服务端签发的客户端证书
	CAPEM     []byte // 控制面 CA，用于校验服务端
}

// LoadOrCreate 加载本地身份；若证书缺失则生成密钥对并生成 CSR 所需材料。
//
// 注意：CSR 在每次注册时基于私钥即时生成，CN 字段会被控制面强制改写为 node_id，
// 这里填占位即可。
func LoadOrCreate(cfg *config.Config) (*Identity, error) {
	id := &Identity{}

	// 加载 CA（用于校验控制面）
	if cfg.ControlPlane.CACert != "" {
		if b, err := os.ReadFile(cfg.ControlPlane.CACert); err == nil {
			id.CAPEM = b
		}
	}
	// 加载已有客户端证书
	if cfg.ControlPlane.ClientCert != "" {
		if b, err := os.ReadFile(cfg.ControlPlane.ClientCert); err == nil {
			id.CertPEM = b
		}
	}
	// 加载或生成私钥
	if cfg.ControlPlane.ClientKey != "" {
		if b, err := os.ReadFile(cfg.ControlPlane.ClientKey); err == nil {
			if k, err2 := parseECKey(b); err2 == nil {
				id.Key = k
			}
		}
	}
	if id.Key == nil {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("生成密钥对失败: %w", err)
		}
		id.Key = k
		if cfg.ControlPlane.ClientKey != "" {
			if err := writeECKey(cfg.ControlPlane.ClientKey, id.Key); err != nil {
				return nil, err
			}
		}
	}
	return id, nil
}

// CSR 生成证书签名请求（CN 填 node_id 占位，控制面会强制改写）。
func (id *Identity) CSR() ([]byte, error) {
	tmpl := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "placeholder"},
		DNSNames: []string{"ecp-node"},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, id.Key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// ApplyResponse 把注册响应落地：写客户端证书与 CA，回填 node_id。
func (id *Identity) ApplyResponse(resp *ecpv1.RegisterResponse, cfg *config.Config) error {
	id.NodeID = resp.NodeId
	id.CertPEM = resp.ClientCert
	id.CAPEM = resp.CaCert
	if cfg.ControlPlane.ClientCert != "" {
		if err := writeFile0600(cfg.ControlPlane.ClientCert, resp.ClientCert); err != nil {
			return err
		}
	}
	if cfg.ControlPlane.CACert != "" {
		if err := writeFile0600(cfg.ControlPlane.CACert, resp.CaCert); err != nil {
			return err
		}
	}
	return nil
}

// Fingerprint 计算硬件指纹：sha256(machine_id + board_serial + mac)，全小写 hex。
//
// 指纹用于"上线即控"——同一份 Key 只能绑定同一台设备；重装系统后只要
// /etc/ecp 被保留或 Key 一致，指纹不变即可自动重认证。
func Fingerprint() (string, error) {
	parts := []string{"", "", ""}

	// machine-id
	if b, err := os.ReadFile("/etc/machine-id"); err == nil {
		parts[0] = strings.TrimSpace(string(b))
	} else if b, err := os.ReadFile("/var/lib/dbus/machine-id"); err == nil {
		parts[0] = strings.TrimSpace(string(b))
	}

	// board serial（树莓派/Orange Pi 等常用）
	if b, err := os.ReadFile("/sys/class/dmi/id/board_serial"); err == nil {
		parts[1] = strings.TrimSpace(string(b))
	}

	// 首个非回环 MAC
	if mac, err := firstMAC(); err == nil {
		parts[2] = mac
	}

	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:]), nil
}

func writeECKey(path string, key *ecdsa.PrivateKey) error {
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("编码私钥失败: %w", err)
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	return writeFile0600(path, b)
}

func writeFile0600(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func parseECKey(b []byte) (*ecdsa.PrivateKey, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("不是合法 PEM")
	}
	return x509.ParseECPrivateKey(blk.Bytes)
}

func firstMAC() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(ifc.HardwareAddr) > 0 {
			return ifc.HardwareAddr.String(), nil
		}
	}
	return "", fmt.Errorf("无可用网卡")
}
