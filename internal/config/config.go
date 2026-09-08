// Package config 解析进程启动参数（命令行 + 环境变量）。
//
// 优先级：命令行 flag > 环境变量 > data/config.json 里的 settings。
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// ErrFlagParse 表示命令行参数解析失败（含 -h），调用方据此选择退出码。
var ErrFlagParse = errors.New("flag parse error")

// Config 是进程级运行参数。
type Config struct {
	Listen     string
	DataDir    string
	AdminToken string
	LogLevel   string
	Version    bool
}

// DefaultDataDir 返回默认数据目录：环境变量 LLM_ROUTER_DATA_DIR，否则 ./data。
func DefaultDataDir() string {
	if v := os.Getenv("LLM_ROUTER_DATA_DIR"); v != "" {
		return v
	}
	return "data"
}

// Parse 解析命令行参数与环境变量。
func Parse(args []string) (Config, error) {
	c := Config{
		DataDir:    DefaultDataDir(),
		AdminToken: os.Getenv("LLM_ROUTER_ADMIN_TOKEN"),
		LogLevel:   envOr("LLM_ROUTER_LOG_LEVEL", "info"),
	}
	fs := flag.NewFlagSet("llm-router", flag.ContinueOnError)
	fs.StringVar(&c.Listen, "addr", envOr("LLM_ROUTER_ADDR", ""), "监听地址，如 :9070（覆盖配置文件）")
	fs.StringVar(&c.DataDir, "data", c.DataDir, "数据目录，存放 config.json 与 usage/")
	fs.StringVar(&c.AdminToken, "admin-token", c.AdminToken, "管理口令（覆盖配置文件）")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "日志级别：debug|info|warn|error")
	fs.BoolVar(&c.Version, "version", false, "打印版本并退出")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.DataDir == "" {
		return c, fmt.Errorf("data dir cannot be empty")
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return c, err
	}
	c.DataDir = abs
	return c, nil
}

// ConfigFile 返回数据目录下的配置文件路径。
func (c Config) ConfigFile() string { return filepath.Join(c.DataDir, "config.json") }

// UsageDir 返回用量流水目录。
func (c Config) UsageDir() string { return filepath.Join(c.DataDir, "usage") }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Usage 打印帮助。
func Usage() string {
	return `llm-router — 自制 Token Key + 智能路由的 LLM 网关

用法:
  llm-router [flags]

flags:
  -addr string         监听地址（默认读 data/config.json 的 settings.listen）
  -data string         数据目录（默认 ./data 或 $LLM_ROUTER_DATA_DIR）
  -admin-token string  管理口令（也可用 $LLM_ROUTER_ADMIN_TOKEN）
  -log-level string    日志级别 debug|info|warn|error（默认 info）
  -version             打印版本

示例:
  llm-router -data ./data -addr :9070
  LLM_ROUTER_ADMIN_TOKEN=s3cret llm-router
`
}
