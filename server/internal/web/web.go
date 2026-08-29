// Package web 内置前端静态资源。
//
// 构建时先把 web/dist 复制到本目录（server/internal/web/spa），再用 embed 打进 exe。
// 这样控制面是单文件、零外部依赖（架构 v2 §7.1）。目录名 spa 以区别于被 .gitignore 排除的构建产物 dist。
package web

import "embed"

// DistFS 是编译进二进制的控制台前端。
//
//go:embed all:spa
var DistFS embed.FS

// FS 返回内嵌的文件系统。
func FS() embed.FS { return DistFS }
