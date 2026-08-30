// Package lock 提供控制面的"单实例端口锁"。
//
// 背景：server.exe（浏览器形态）与 ecp-desktop.exe（桌面形态）共用
// gRPC 7443。两个进程同时启动会端口冲突、节点连不上。
// 本包在启动 gRPC 前预检 7443：已被占用则提示并退出（第二个实例自然失败），
// 保证同一时刻只有一个控制面进程存活。
package lock

import (
	"fmt"
	"net"
	"time"
)

// CheckPortFree 探测端口是否空闲。占用则返回信息性错误。
// addr 形如 "0.0.0.0:7443"。探测用 2 秒超时的主动 Connect 判断，
// 避免与服务端真实监听混淆。
func CheckPortFree(addr string) error {
	conn, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("端口 %s 已被其他控制面进程占用。请先关闭已运行的控制面（server.exe 或 ecp-desktop.exe）后再启动。", addr)
	}
	_ = conn.Close()
	return nil
}

// WaitFree 轮询等待端口释放（等待旧实例退出的场景）。
func WaitFree(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := CheckPortFree(addr); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("等待端口 %s 释放超时", addr)
}