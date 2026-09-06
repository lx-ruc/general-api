package main

import (
	"embed"
	"io/fs"
)

// 前端构建产物嵌入（embed 路径相对本文件，须放在仓库根）。
// web/dist 由 build.sh / make build 生成；仓库内提交占位 index.html 以便 clone 后可直接 go build。
//
//go:embed all:web/dist
var webDistEmbed embed.FS

// webDistFS web/dist 子目录视图
var webDistFS, _ = fs.Sub(webDistEmbed, "web/dist")
