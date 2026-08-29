// Package ca 实现控制面内置的证书颁发机构（CA）。
//
// 设计要点：
//   - 控制面是一个单文件进程，"零外部依赖"是本项目的硬约束，
//     所以 CA直接用 Go 标准库 crypto/x509 实现，不引任何第三方 PKI 框架。
//   - CA 私钥与证书持久化到 runtime/certs（ca.key / ca.crt），进程重启后复用，
//     避免每次重启都让所有节点被迫重连。
//   - 节点证书 CN = node_id，这是"证书即身份"的基础；
//     服务端在 mTLS interceptor 里校验 CN 与请求里的 node_id 一致。
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CA 是控制面的内置证书颁发机构。
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	keyPEM  []byte
}

const (
	caCertFile = "ca.crt"
	caKeyFile  = "ca.key"
)

// LoadOrCreate 加载已有 CA；不存在则生成一份并落盘。
//
// certsDir 通常是 config.Server.DataDir/certs。调用前应确保目录存在。
func LoadOrCreate(certsDir string) (*CA, error) {
	certPath := filepath.Join(certsDir, caCertFile)
	keyPath := filepath.Join(certsDir, caKeyFile)

	if certPEM, err := os.ReadFile(certPath); err == nil {
		if keyPEM, err2 := os.ReadFile(keyPath); err2 == nil {
			ca, err := parseCA(certPEM, keyPEM)
			if err == nil {
				return ca, nil
			}
			// 解析失败（文件损坏）则重建，避免直接崩溃
			return nil, fmt.Errorf("解析已有 CA 失败，建议备份后删除 %s 重建: %w", certPath, err)
		}
	}
	return generate(certsDir)
}

func parseCA(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, fmt.Errorf("ca.crt 不是合法 PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, fmt.Errorf("ca.key 不是合法 PEM")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM}, nil
}

func generate(certsDir string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成 CA 密钥失败: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ecp-control-ca", Organization: []string{"ecp"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("创建 CA 证书失败: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	cb := &pem.Block{Type: "CERTIFICATE", Bytes: der}
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("编码 CA 私钥失败: %w", err)
	}
	kb := &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}
	certPEM := pem.EncodeToMemory(cb)
	keyPEM := pem.EncodeToMemory(kb)
	if err := os.WriteFile(filepath.Join(certsDir, caCertFile), certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("写 ca.crt 失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, caKeyFile), keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("写 ca.key 失败: %w", err)
	}
	return &CA{cert: cert, key: key, certPEM: certPEM, keyPEM: keyPEM}, nil
}

// CertPEM 返回 CA 证书 PEM，供 Agent 写入 ca.crt 校验控制面身份。
func (c *CA) CertPEM() []byte { return c.certPEM }

// SignClientCert 用 CSR 为指定 node_id 签发一张客户端证书。
//
// CSR 必须由调用方基于本节点私钥生成，私钥不出节点。签发时强制回填
// CN = node_id（忽略 CSR 自带的 Subject，防止节点自拟身份）。
func (c *CA) SignClientCert(nodeID string, csrPEM []byte, ttl time.Duration) ([]byte, error) {
	cb, _ := pem.Decode(csrPEM)
	if cb == nil {
		return nil, fmt.Errorf("CSR 不是合法 PEM")
	}
	csr, err := x509.ParseCertificateRequest(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析 CSR 失败: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("CSR 签名校验失败: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: nodeID, Organization: []string{"ecp-node"}},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(ttl),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, fmt.Errorf("签发客户端证书失败: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// SignClientCertReturnSerial 与 SignClientCert 相同，额外返回证书序列号（十进制字符串），
// 供控制面登记凭证台账与后续吊销查询使用。
func (c *CA) SignClientCertReturnSerial(nodeID string, csrPEM []byte, ttl time.Duration) (serial string, certPEM []byte, err error) {
	certPEM, err = c.SignClientCert(nodeID, csrPEM, ttl)
	if err != nil {
		return "", nil, err
	}
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return "", nil, fmt.Errorf("签发的证书不是合法 PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return "", nil, err
	}
	return cert.SerialNumber.String(), certPEM, nil
}

// SignServerCert 为控制面自身签发一张服务端证书（HTTPS 与 gRPC 共用）。
//
// SAN 包含 localhost 与常见回环地址，方便本机与 Tailscale 域名访问。
func (c *CA) SignServerCert(dnsNames []string, ttl time.Duration) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("生成服务端密钥失败: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "ecp-control", Organization: []string{"ecp"}},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, fmt.Errorf("签发服务端证书失败: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("编码服务端私钥失败: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	return certPEM, keyPEM, nil
}
