// Command server 是边缘计算节点控制平台的控制面进程。
//
// 设计约束：控制面按需上线，不保证 7×24 在线。
// 因此它必须做到启动即可用、退出不丢状态——节点侧持有全部自治能力。
package main

import (
	"fmt"
	"os"

	ecpv1 "ecp.dev/ecp/proto/gen/ecp/v1"
)

// 由 -ldflags "-X main.version=..." 注入
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("ecp-server %s\n", version)
		fmt.Printf("proto contract: %s\n", ecpv1.File_ecp_v1_ecp_proto.Path())
		return
	}
	fmt.Printf("ecp-server %s starting...\n", version)
}
