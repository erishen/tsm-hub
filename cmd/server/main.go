// Command tsm-hub 启动一个 LLM Token Router 服务：
// 对外签发自制 Token Key（sk-tr-…），把 OpenAI 兼容请求智能路由到配置好的上游模型。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/erishen/tsm-hub/internal/api"
	"github.com/erishen/tsm-hub/internal/config"
	"github.com/erishen/tsm-hub/internal/proxy"
	"github.com/erishen/tsm-hub/internal/quota"
	"github.com/erishen/tsm-hub/internal/router"
	"github.com/erishen/tsm-hub/internal/skills"
	"github.com/erishen/tsm-hub/internal/store"
)

// version 由 Makefile 通过 -ldflags 注入。
var version = "dev"

func main() {
	// 先加载 .env 文件（不会覆盖已存在的环境变量）
	config.LoadDefaultDotEnv("data")

	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, config.ErrFlagParse) {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}
	if cfg.Version {
		fmt.Printf("tsm-hub %s\n", version)
		return
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(cfg config.Config) error {
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	st, err := store.New(cfg.ConfigFile())
	if err != nil {
		return err
	}
	settings := st.Settings()

	// 命令行 / 环境变量覆盖配置文件。
	if cfg.AdminToken != "" {
		_ = st.Update(func(c *store.Config) error {
			c.Settings.AdminToken = cfg.AdminToken
			return nil
		})
		settings = st.Settings()
	}
	listen := settings.Listen
	if cfg.Listen != "" {
		listen = cfg.Listen
	}

	// 启动自检：把最容易踩的两个配置问题直接喊出来。
	if settings.AdminToken == "" || settings.AdminToken == store.DefaultAdminToken {
		logger.Warn("admin token is still the default placeholder; " +
			"change settings.admin_token in config.json or pass -admin-token")
	}
	if len(st.ListProviders()) == 0 {
		logger.Warn("no upstream provider configured yet; " +
			"add one in the admin console or via POST /api/admin/providers")
	}

	// 安全自检：config.json 权限和明文 API Key。
	checkConfigSecurity(cfg.ConfigFile(), st, logger)

	// TLS 配置检查。
	if settings.TLS.Enabled {
		if settings.TLS.CertFile == "" || settings.TLS.KeyFile == "" {
			return fmt.Errorf("TLS enabled but cert_file or key_file is empty")
		}
		if _, err := os.Stat(settings.TLS.CertFile); err != nil {
			return fmt.Errorf("TLS cert file not found: %s", settings.TLS.CertFile)
		}
		if _, err := os.Stat(settings.TLS.KeyFile); err != nil {
			return fmt.Errorf("TLS key file not found: %s", settings.TLS.KeyFile)
		}
		logger.Info("TLS enabled", "cert", settings.TLS.CertFile, "key", settings.TLS.KeyFile)
	} else {
		logger.Warn("TLS is disabled — API keys and user data are transmitted in plaintext. " +
			"Enable TLS in config.json (settings.tls) or put a reverse proxy (Nginx/Caddy) in front for production.")
	}

	rec, err := quota.NewRecorder(cfg.UsageDir())
	if err != nil {
		return err
	}
	defer rec.Close()
	// 启动时清理过期用量流水（usage_retention_days 配置，<=0 不清理）
	if settings.UsageRetentionDays > 0 {
		if n, err := rec.Purge(settings.UsageRetentionDays); err != nil {
			logger.Warn("purge expired usage failed", "error", err)
		} else if n > 0 {
			logger.Info("purged expired usage files", "count", n, "retention_days", settings.UsageRetentionDays)
		}
	}

	limiter := quota.NewLimiter(rec)
	tracker := router.NewTracker(settings.FailThreshold, settings.CooldownSec)
	rt := router.New(st, tracker)
	px := proxy.New(st, rt, tracker, rec, skills.New(settings.SkillsDir))
	// 常驻连接 MCP servers（断开自动重连），管理台打开即有状态、工具池稳定。
	px.StartMCP()

	srv := api.New(api.Options{
		Store:   st,
		Rec:     rec,
		Limiter: limiter,
		Router:  rt,
		Health:  tracker,
		Proxy:   px,
		Skills:  skills.New(settings.SkillsDir),
		Logger:  logger,
	})

	httpSrv := &http.Server{
		Addr:              listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		// 流式响应可能持续很久，不设 WriteTimeout。
		IdleTimeout: 120 * time.Second,
	}

	// config.json 热加载：Provider/Route/Key 变更即时生效，无需重启。
	go watchConfig(st, logger)

	// 优雅退出。
	errCh := make(chan error, 1)
	go func() {
		scheme := "http"
		if settings.TLS.Enabled {
			scheme = "https"
		}
		logger.Info("tsm-hub listening",
			"addr", listen, "scheme", scheme, "version", version,
			"config", cfg.ConfigFile(), "data", cfg.DataDir)
		logger.Info("admin console", "url", consoleURL(listen, settings.TLS.Enabled), "admin_token_set", settings.AdminToken != "")
		var err error
		if settings.TLS.Enabled {
			err = httpSrv.ListenAndServeTLS(settings.TLS.CertFile, settings.TLS.KeyFile)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		logger.Info("shutting down", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		return err
	}
	return rec.Close()
}

// consoleURL 把监听地址变成可点的 URL：":9070" → localhost:9070，"0.0.0.0:9070" → localhost:9070。
func consoleURL(listen string, tls bool) string {
	addr := strings.TrimSpace(listen)
	scheme := "http"
	if tls {
		scheme = "https"
	}
	if strings.HasPrefix(addr, ":") {
		return scheme + "://localhost" + addr
	}
	addr = strings.Replace(addr, "0.0.0.0", "localhost", 1)
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return scheme + "://" + addr
}

// checkConfigSecurity 检查配置文件权限和明文 API Key，发出安全警告。
func checkConfigSecurity(configFile string, st *store.Store, logger *slog.Logger) {
	// 检查 config.json 权限（建议 600）。
	if fi, err := os.Stat(configFile); err == nil {
		perm := fi.Mode().Perm()
		// 检查是否有 group/other 的读权限（044 表示 group 和 other 可读）。
		if perm&0044 != 0 {
			logger.Warn("config file is readable by group/others — it may contain plaintext API keys. "+
				"Run: chmod 600 "+configFile,
				"file", configFile, "permissions", perm.String())
		}
	}

	// 检查是否有明文存储的 Provider API Key（建议使用 env: 前缀引用环境变量）。
	plaintextKeys := 0
	envKeys := 0
	for _, p := range st.ListProviders() {
		if p.APIKey == "" {
			continue
		}
		if strings.HasPrefix(p.APIKey, store.EnvKeyPrefix) {
			envKeys++
		} else {
			plaintextKeys++
		}
	}
	if plaintextKeys > 0 {
		logger.Warn("some provider API keys are stored in plaintext in config.json. "+
			"Consider using 'env:VAR_NAME' prefix to reference environment variables instead. "+
			"See SECURITY.md for details.",
			"plaintext_count", plaintextKeys, "env_reference_count", envKeys)
	}
}

func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

// watchConfig 监听 config.json 变更并热重载。
// 覆盖 provider/route/key/settings 中参与路由的配置；MCP、skills 等
// 启动期装配的能力仍需要重启生效。
func watchConfig(st *store.Store, logger *slog.Logger) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Warn("config hot-reload disabled", "err", err)
		return
	}
	defer watcher.Close()
	path := st.Path()
	if err := watcher.Add(path); err != nil {
		logger.Warn("config hot-reload disabled", "err", err)
		return
	}
	debounce := time.NewTimer(time.Hour)
	if !debounce.Stop() {
		select {
		case <-debounce.C:
		default:
		}
	}
	pending := false
	reload := func() {
		if err := st.Reload(); err != nil {
			// 编辑器原子替换可能短暂出现半写/空文件：重读失败则稍后重试。
			logger.Warn("config reload failed, keeping previous config", "err", err)
			pending = true
			debounce.Reset(300 * time.Millisecond)
			return
		}
		logger.Info("config reloaded", "path", path)
	}
	for {
		select {
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			if !pending {
				pending = true
				debounce.Reset(300 * time.Millisecond)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logger.Warn("config watcher error", "err", err)
		case <-debounce.C:
			if !pending {
				continue
			}
			pending = false
			reload()
		}
	}
}
