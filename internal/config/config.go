// Package config 解析进程启动参数（命令行 + 环境变量 + .env 文件）。
//
// 优先级：命令行 flag > 环境变量 > .env 文件 > data/config.json 里的 settings。
//
// 环境变量前缀：TSM_HUB_*（最新），兼容 TSM_HUB_* 和 LLM_ROUTER_*（按优先级 fallback）。
package config

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// envOrFallback 按优先级读取环境变量：TSM_HUB_* → TSM_HUB_* → LLM_ROUTER_*，都没有则返回默认值。
func envOrFallback(hubKey, gatewayKey, routerKey, def string) string {
	if v := os.Getenv(hubKey); v != "" {
		return v
	}
	if v := os.Getenv(gatewayKey); v != "" {
		return v
	}
	if v := os.Getenv(routerKey); v != "" {
		return v
	}
	return def
}

// DefaultDataDir 返回默认数据目录：环境变量 TSM_HUB_DATA_DIR（兼容 TSM_HUB_DATA_DIR、LLM_ROUTER_DATA_DIR），否则 ./data。
func DefaultDataDir() string {
	return envOrFallback("TSM_HUB_DATA_DIR", "TSM_HUB_DATA_DIR", "LLM_ROUTER_DATA_DIR", "data")
}

// Parse 解析命令行参数与环境变量。
func Parse(args []string) (Config, error) {
	c := Config{
		DataDir:    DefaultDataDir(),
		AdminToken: envOrFallback("TSM_HUB_ADMIN_TOKEN", "TSM_HUB_ADMIN_TOKEN", "LLM_ROUTER_ADMIN_TOKEN", ""),
		LogLevel:   envOrFallback("TSM_HUB_LOG_LEVEL", "TSM_HUB_LOG_LEVEL", "LLM_ROUTER_LOG_LEVEL", "info"),
	}
	fs := flag.NewFlagSet("tsm-hub", flag.ContinueOnError)
	fs.StringVar(&c.Listen, "addr", envOrFallback("TSM_HUB_ADDR", "TSM_HUB_ADDR", "LLM_ROUTER_ADDR", ""), "监听地址，如 :9070（覆盖配置文件）")
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
	return `tsm-hub — Tools + Skills + MCPs 能力中心

用法:
  tsm-hub [flags]

flags:
  -addr string         监听地址（默认读 data/config.json 的 settings.listen）
  -data string         数据目录（默认 ./data 或 $TSM_HUB_DATA_DIR）
  -admin-token string  管理口令（也可用 $TSM_HUB_ADMIN_TOKEN）
  -log-level string    日志级别 debug|info|warn|error（默认 info）
  -version             打印版本

示例:
  tsm-hub -data ./data -addr :9070
  TSM_HUB_ADMIN_TOKEN=s3cret tsm-hub

环境变量兼容：新前缀 TSM_HUB_* 优先，兼容 TSM_HUB_* 和 LLM_ROUTER_*。
`
}

// LoadDotEnv 从指定路径加载 .env 文件，设置环境变量（不会覆盖已存在的环境变量）。
// 支持的格式：
//   - KEY=VALUE
//   - KEY="VALUE WITH SPACES"
//   - KEY='VALUE WITH SPACES'
//   - # 注释行
//   - 空行
//
// 如果文件不存在，静默返回（不报错）。
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // .env 文件不存在是正常的
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 解析 KEY=VALUE
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		// 去除引号
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		// 不覆盖已存在的环境变量
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

// LoadDefaultDotEnv 从默认路径加载 .env 文件：当前目录 .env，然后数据目录 ../.env。
func LoadDefaultDotEnv(dataDir string) {
	// 优先当前目录
	if err := LoadDotEnv(".env"); err == nil {
		return
	}
	// 其次数据目录的上级目录（项目根目录）
	parent := filepath.Dir(dataDir)
	if err := LoadDotEnv(filepath.Join(parent, ".env")); err == nil {
		return
	}
}
