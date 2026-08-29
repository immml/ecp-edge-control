// Command agent 是运行在边缘节点上的常驻守护进程。
//
// 设计约束：
//   - 单文件静态二进制，同时构建 linux/amd64 与 linux/arm64
//   - 默认以普通用户运行，需提权的操作降级为生成脚本
//   - 控制面离线期间按最后下发的配置自治运行
package main

import (
	"fmt"
	"os"
	"runtime"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// 由 -ldflags "-X main.version=..." 注入
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("ecp-agent %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		fmt.Printf("proto contract: %s\n", ecpv1.File_ecp_v1_ecp_proto.Path())
		return
	}
	fmt.Printf("ecp-agent %s (%s/%s) starting...\n", version, runtime.GOOS, runtime.GOARCH)
}
