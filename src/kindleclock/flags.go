package main

import (
	"flag"
	"fmt"
	"runtime"
)

// buildVersion 由 -ldflags 注入。
var buildVersion = "dev"

type options struct {
	configPath string
	previewTo  string
	fixturesTo string
	once       bool
	showVer    bool
	toastText  string
}

func parseFlags(args []string) (*options, error) {
	fs := flag.NewFlagSet("kindleclock", flag.ContinueOnError)
	o := &options{}

	fs.StringVar(&o.configPath, "config", "/mnt/us/kindleclock/config.toml", "配置文件路径")
	fs.StringVar(&o.previewTo, "preview", "", "只渲染一帧并写出 PNG，不接触设备")
	fs.StringVar(&o.fixturesTo, "fixtures", "", "从 JSON 快照读取数据（配合 -preview）")
	fs.BoolVar(&o.once, "once", false, "渲染并显示一帧后退出")
	fs.BoolVar(&o.showVer, "version", false, "打印版本后退出")
	fs.StringVar(&o.toastText, "toast", "", "把这段文字画成图贴到屏幕正中后退出（KUAL 提示用）")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	// 预览图的数据全部来自 fixtures；没有它就只能联网抓取，
	// 那已经不是「不接触设备」的预览了。
	if o.previewTo != "" && o.fixturesTo == "" {
		return nil, fmt.Errorf("-preview 需要配合 -fixtures 使用（否则会去联网抓取）")
	}
	return o, nil
}

// printVersion 只负责打印，退出由调用方做 —— 这样测试可以直接调用
// 它并捕获 stdout（若函数内部 os.Exit(0)，测试会 panic 退出）。
func printVersion() {
	fmt.Printf("kindleclock %s (%s/%s, %s)\n",
		buildVersion, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
