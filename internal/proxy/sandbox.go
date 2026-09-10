package proxy

// Docker 沙箱（execute_code 工具）：在一次性容器中执行代码。
// 安全特性移植自 spring-harness DockerSandboxExecutor：
//   - --rm 执行完自动销毁；--cap-drop ALL + no-new-privileges 最小权限
//   - --network none 禁用网络；--read-only 只读根文件系统，仅 /tmp 可写
//   - 内存/CPU/进程数/文件描述符限制；超时自动 kill 并清理容器
// 支持语言：python / javascript / shell / java / go / rust / c / cpp。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// sandboxLang 描述一种语言的执行方式。
type sandboxLang struct {
	image    string // Docker 镜像
	command  string // 容器内执行命令（经 sh -c）
	filename string // 容器内代码文件名
}

var sandboxLanguages = map[string]sandboxLang{
	"python":     {"python:3.11-slim", "python3 /tmp/code.py", "code.py"},
	"javascript": {"node:20-slim", "node /tmp/code.js", "code.js"},
	"shell":      {"alpine:3.19", "sh /tmp/code.sh", "code.sh"},
	"java":       {"eclipse-temurin:17-jdk", "java /tmp/Main.java", "Main.java"},
	"go":         {"golang:1.22-alpine", "env GOPATH=/tmp/gopath GOCACHE=/tmp/gocache go run /tmp/code.go", "code.go"},
	"rust":       {"rust:1.75-alpine", "rustc /tmp/code.rs -o /tmp/code && /tmp/code", "code.rs"},
	"c":          {"sandbox-gcc:alpine", "gcc /tmp/code.c -o /tmp/code && /tmp/code", "code.c"},
	"cpp":        {"sandbox-gcc:alpine", "g++ /tmp/code.cpp -o /tmp/code && /tmp/code", "code.cpp"},
}

// normalizeLang 规范化语言名（支持常见别名）。
func normalizeLang(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "py", "python3":
		return "python"
	case "js", "node", "nodejs":
		return "javascript"
	case "sh", "bash", "zsh":
		return "shell"
	case "c++", "cxx", "cplusplus":
		return "cpp"
	case "rs":
		return "rust"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

// sandboxResult 是一次沙箱执行的结果。
type sandboxResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
	TimedOut bool
}

// dockerAvailability 缓存 docker 可用性检查（60s）。
type dockerAvailability struct {
	mu      sync.Mutex
	ok      bool
	checked time.Time
}

var dockerAvail = &dockerAvailability{}

// dockerAvailable 检查本机 docker daemon 是否可用（结果缓存 60s）。
func dockerAvailable() bool {
	dockerAvail.mu.Lock()
	defer dockerAvail.mu.Unlock()
	if time.Since(dockerAvail.checked) < 60*time.Second {
		return dockerAvail.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "docker", "info").Run()
	dockerAvail.ok = err == nil
	dockerAvail.checked = time.Now()
	return dockerAvail.ok
}

// SandboxStatus 返回沙箱状态：配置 + docker 可用性 + 支持语言（管理台展示用）。
func (p *Proxy) SandboxStatus() map[string]any {
	cfg := p.store.Settings().Sandbox
	timeoutSec, memMB, cpus, maxOutKB := cfg.TimeoutSec, cfg.MemoryMB, cfg.CPUs, cfg.MaxOutputKB
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	if memMB <= 0 {
		memMB = 512
	}
	if cpus <= 0 {
		cpus = 1
	}
	if maxOutKB <= 0 {
		maxOutKB = 100
	}
	langs := make([]string, 0, len(sandboxLanguages))
	for l := range sandboxLanguages {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	return map[string]any{
		"enabled":       cfg.Enabled,
		"docker_ok":     dockerAvailable(),
		"timeout_sec":   timeoutSec,
		"memory_mb":     memMB,
		"cpus":          cpus,
		"max_output_kb": maxOutKB,
		"languages":     langs,
	}
}

// toolExecuteCode 在 Docker 沙箱中执行代码（execute_code 工具实现）。
func (p *Proxy) toolExecuteCode(args toolArgs) (string, error) {
	cfg := p.store.Settings().Sandbox
	if !cfg.Enabled {
		return "", fmt.Errorf("execute_code 未启用（settings.sandbox.enabled=true 开启，需本机 docker）")
	}
	if !dockerAvailable() {
		return "", fmt.Errorf("docker 不可用：请确认 Docker daemon 已启动（本机为 OrbStack/Docker Desktop）")
	}
	lang := normalizeLang(args.str("language"))
	sb, ok := sandboxLanguages[lang]
	if !ok {
		return "", fmt.Errorf("不支持的语言 %q（支持：python/javascript/shell/java/go/rust/c/cpp）", args.str("language"))
	}
	code := args.str("code")
	if strings.TrimSpace(code) == "" {
		return "", fmt.Errorf("missing 'code'")
	}
	timeoutSec := cfg.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	if t := int(args.num("timeout")); t > 0 && t <= 300 {
		timeoutSec = t
	}
	memMB := cfg.MemoryMB
	if memMB <= 0 {
		memMB = 512
	}
	cpus := cfg.CPUs
	if cpus <= 0 {
		cpus = 1
	}
	maxOutKB := cfg.MaxOutputKB
	if maxOutKB <= 0 {
		maxOutKB = 100
	}

	// 写临时代码文件。
	tmp, err := os.CreateTemp("", "llm-sandbox-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(code); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()

	containerName := fmt.Sprintf("llm-sb-%d", time.Now().UnixNano())
	cmdArgs := []string{
		"run", "--rm",
		"--name", containerName,
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--network", "none",
		"--read-only",
		"--memory", fmt.Sprintf("%dm", memMB),
		"--cpus", fmt.Sprintf("%v", cpus),
		"--pids-limit", "100",
		"--ulimit", "nofile=64:64",
		"-v", tmp.Name() + ":/tmp/" + sb.filename + ":ro",
		"--tmpfs", "/tmp:rw,size=128m,exec",
		"--stop-timeout", fmt.Sprintf("%d", min(timeoutSec, 10)),
		sb.image,
		"sh", "-c", sb.command,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", cmdArgs...)
	var outBuf, errBuf limitedBuffer
	outBuf.max = maxOutKB * 1024
	errBuf.max = maxOutKB * 1024
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	started := time.Now()
	err = cmd.Run()
	duration := time.Since(started)
	timedOut := ctx.Err() == context.DeadlineExceeded
	if timedOut {
		// docker run 客户端被杀后容器可能残留，主动清理。
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = exec.CommandContext(cctx, "docker", "rm", "-f", containerName).Run()
		cancel()
	}
	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// docker 本身成功执行，容器内命令退出码非零：属正常结果，透传退出码。
		exitCode = exitErr.ExitCode()
	} else if err != nil && !timedOut {
		return "", fmt.Errorf("docker exec failed: %v", err)
	}
	var sb2 strings.Builder
	if outBuf.String() != "" {
		fmt.Fprintf(&sb2, "[stdout]\n%s\n", outBuf.String())
	}
	if errBuf.String() != "" {
		fmt.Fprintf(&sb2, "[stderr]\n%s\n", errBuf.String())
	}
	if timedOut {
		fmt.Fprintf(&sb2, "[执行超时（%ds），已强制终止容器]\n", timeoutSec)
	} else {
		fmt.Fprintf(&sb2, "[exit=%d, %s]\n", exitCode, duration.Round(time.Millisecond))
	}
	return sb2.String(), nil
}

// limitedBuffer 带字节上限的输出缓冲。
type limitedBuffer struct {
	max int
	buf []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(b.buf) >= b.max {
		return len(p), nil
	}
	room := b.max - len(b.buf)
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		b.buf = append(b.buf, []byte("\n...[输出超过限制，已截断]")...)
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *limitedBuffer) String() string { return string(b.buf) }
