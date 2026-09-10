// Package config 解析进程启动参数（命令行 + 环境变量）。
//
// 优先级：命令行 flag > 环境变量 > data/config.json 里的 settings。
//
// 环境变量前缀：TSM_GATEWAY_*（新），兼容旧前缀 LLM_ROUTER_*（先读新的，再读旧的）。
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

// envOrFallback 先读新前缀 TSM_GATEWAY_*，再读旧前缀 LLM_ROUTER_*，都没有则返回默认值。
func envOrFallback(newKey, oldKey, def string) string {
	if v := os.Getenv(newKey); v != "" {
		return v
	}
	if v := os.Getenv(oldKey); v != "" {
		return v
	}
	return def
}

// DefaultDataDir 返回默认数据目录：环境变量 TSM_GATEWAY_DATA_DIR（兼容 LLM_ROUTER_DATA_DIR），否则 ./data。
func DefaultDataDir() string {
	return envOrFallback("TSM_GATEWAY_DATA_DIR", "LLM_ROUTER_DATA_DIR", "data")
}

// Parse 解析命令行参数与环境变量。
func Parse(args []string) (Config, error) {
	c := Config{
		DataDir:    DefaultDataDir(),
		AdminToken: envOrFallback("TSM_GATEWAY_ADMIN_TOKEN", "LLM_ROUTER_ADMIN_TOKEN", ""),
		LogLevel:   envOrFallback("TSM_GATEWAY_LOG_LEVEL", "LLM_ROUTER_LOG_LEVEL", "info"),
	}
	fs := flag.NewFlagSet("tsm-gateway", flag.ContinueOnError)
	fs.StringVar(&c.Listen, "addr", envOrFallback("TSM_GATEWAY_ADDR", "LLM_ROUTER_ADDR", ""), "监听地址，如 :9070（覆盖配置文件）")
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
	return `tsm-gateway — Tools + Skills + MCPs 能力网关

用法:
  tsm-gateway [flags]

flags:
  -addr string         监听地址（默认读 data/config.json 的 settings.listen）
  -data string         数据目录（默认 ./data 或 $TSM_GATEWAY_DATA_DIR）
  -admin-token string  管理口令（也可用 $TSM_GATEWAY_ADMIN_TOKEN）
  -log-level string    日志级别 debug|info|warn|error（默认 info）
  -version             打印版本

示例:
  tsm-gateway -data ./data -addr :9070
  TSM_GATEWAY_ADMIN_TOKEN=s3cret tsm-gateway

环境变量兼容：旧前缀 LLM_ROUTER_* 仍可用（如 LLM_ROUTER_ADMIN_TOKEN），新前缀 TSM_GATEWAY_* 优先。
`
}
