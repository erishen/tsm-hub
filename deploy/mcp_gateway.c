/* mcp_gateway.c — 轻量 MCP stdio→Streamable HTTP 网关（并行版）。
 *
 * 替代 npm 的 supergateway（它在镜像里占 66.6MB）。
 *
 * 并行模型（三层）：
 *   1. HTTP：每条连接一个线程（detach），/health 不占会话、不会被慢请求挡住。
 *   2. 会话：一个 mcp-session-id 对一个子进程；initialize 新建，DELETE 关闭，
 *      空闲 30 分钟回收；最多 4 个会话（每个会话是一个 node 进程，内存有代价）。
 *   3. 会话内：同一子进程允许并发在飞的请求 —— 把客户端 id 改写成内部自增 id
 *      再写进 stdin，读线程按 id 把回包分给对应等待者，最后把 id 改回原样。
 *
 * 协议契约（与 lume-tools/mcp_http_proxy.sh 对齐）：
 *   POST /mcp    body = 一行 JSON-RPC
 *                -> 200 application/json（body 为子进程回的那一行）
 *                -> initialize 响应带 mcp-session-id 头
 *                -> 通知类（无 id）回 202 空体
 *   GET  /health -> 200 ok（不依赖 node，容器探活用）
 *   DELETE /mcp  -> 200（关闭该会话的子进程）
 *   GET  /mcp    -> 405（服务端推送流未实现，协议里属可选通道）
 *
 * 用法：mcp_gateway <port> <stdio-command>
 *   例：mcp_gateway 8000 "node /mcp/node_modules/fetch-mcp-server.cjs"
 */
#define _GNU_SOURCE
#include <arpa/inet.h>
#include <ctype.h>
#include <errno.h>
#include <fcntl.h>
#include <netinet/in.h>
#include <poll.h>
#include <pthread.h>
#include <signal.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define REQ_MAX 65536
#define LINE_MAX 262144
#define MAX_SESSIONS 4
#define IDLE_TTL_SEC 1800
#define CALL_TIMEOUT_SEC 300

static char g_cmd[4096];

static void logf_(const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    vfprintf(stderr, fmt, ap);
    va_end(ap);
    fputc('\n', stderr);
}

/* musl 的默认线程栈只有 128KB；连接线程里有两个 64KB 缓冲，必须显式加栈。 */
#define THREAD_STACK (1024 * 1024)
static int spawn_detached(pthread_t *th, void *(*fn)(void *), void *arg) {
    pthread_attr_t attr;
    pthread_attr_init(&attr);
    pthread_attr_setstacksize(&attr, THREAD_STACK);
    int rc = pthread_create(th, &attr, fn, arg);
    pthread_attr_destroy(&attr);
    if (rc == 0) pthread_detach(*th);
    return rc;
}

static long long now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (long long)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

/* ===================== 待回包表 ===================== */

typedef struct pending {
    long long key;
    char *orig_id;
    char *body;
    size_t body_len;
    int done;
    int matched;
    pthread_cond_t cv;
    struct pending *next;
} pending_t;

/* ===================== 会话 ===================== */

typedef struct session {
    char sid[64];
    pid_t pid;
    int in_fd;
    int out_fd;
    pthread_mutex_t wlock;
    pthread_mutex_t plock;
    pthread_t reader;
    pending_t *pend;
    int inflight;
    int alive;
    time_t last_used;
    struct session *next;
} session_t;

static session_t *g_sessions = NULL;
static pthread_mutex_t g_slock = PTHREAD_MUTEX_INITIALIZER;
static long long g_id_seq = 0;
static pthread_mutex_t g_idlock = PTHREAD_MUTEX_INITIALIZER;

static long long next_internal_id(void) {
    pthread_mutex_lock(&g_idlock);
    long long v = ++g_id_seq;
    pthread_mutex_unlock(&g_idlock);
    return v;
}

static session_t *session_find(const char *sid) {
    session_t *s = g_sessions;
    while (s) {
        if (strcmp(s->sid, sid) == 0) return s;
        s = s->next;
    }
    return NULL;
}

static int session_count(void) {
    int n = 0;
    for (session_t *s = g_sessions; s; s = s->next) n++;
    return n;
}

static void session_unlink(session_t *s) {
    session_t **pp = &g_sessions;
    while (*pp) {
        if (*pp == s) { *pp = s->next; return; }
        pp = &(*pp)->next;
    }
}

/* ===================== JSON 小工具 ===================== */

static const char *skip_ws(const char *p) {
    while (*p && isspace((unsigned char)*p)) p++;
    return p;
}

static const char *skip_string(const char *p) {
    if (*p != '"') return NULL;
    p++;
    while (*p) {
        if (*p == '\\') {
            p++;
            if (!*p) return NULL;
            p++;
            continue;
        }
        if (*p == '"') return p + 1;
        p++;
    }
    return NULL;
}

/* 找 depth==1 的 "id" 值 token 区间 [*start, *end)；字符串 token 含两侧引号。 */
static int find_toplevel_id(const char *s, const char **start, const char **end) {
    const char *p = skip_ws(s);
    if (*p != '{') return -1;
    p++;
    for (;;) {
        p = skip_ws(p);
        if (!*p || *p == '}') return -1;
        if (*p == ',') { p++; continue; }
        if (*p != '"') return -1;
        const char *kstart = p;
        const char *kend = skip_string(p);
        if (!kend) return -1;
        size_t klen = (size_t)(kend - kstart) - 2;
        p = skip_ws(kend);
        if (*p != ':') return -1;
        p = skip_ws(p + 1);

        if (klen == 2 && kstart[1] == 'i' && kstart[2] == 'd') {
            *start = p;
            if (*p == '"') {
                const char *e = skip_string(p);
                if (!e) return -1;
                *end = e;
            } else {
                const char *e = p;
                while (*e && *e != ',' && *e != '}' && !isspace((unsigned char)*e)) e++;
                if (e == p) return -1;
                *end = e;
            }
            return 0;
        }

        if (*p == '{' || *p == '[') {
            char open = *p, close = (open == '{') ? '}' : ']';
            int d = 1;
            p++;
            while (*p && d > 0) {
                if (*p == '"') {
                    const char *e = skip_string(p);
                    if (!e) return -1;
                    p = e;
                    continue;
                }
                if (*p == open) d++;
                else if (*p == close) d--;
                p++;
            }
            if (*p == ',') p++;
            continue;
        }
        if (*p == '"') {
            const char *e = skip_string(p);
            if (!e) return -1;
            p = e;
        } else {
            while (*p && *p != ',' && *p != '}' && !isspace((unsigned char)*p)) p++;
        }
        if (*p == ',') p++;
    }
}

static char *replace_toplevel_id(const char *body, const char *repl, size_t *out_len) {
    const char *s = NULL, *e = NULL;
    if (find_toplevel_id(body, &s, &e) != 0) return NULL;
    size_t prefix = (size_t)(s - body);
    size_t suffix_len = strlen(e);
    size_t total = prefix + strlen(repl) + suffix_len + 1;
    char *out = malloc(total);
    if (!out) return NULL;
    memcpy(out, body, prefix);
    memcpy(out + prefix, repl, strlen(repl));
    memcpy(out + prefix + strlen(repl), e, suffix_len + 1);
    if (out_len) *out_len = total - 1;
    return out;
}

/* ===================== 子进程与会话 ===================== */

static ssize_t read_line_blocking(int fd, char *buf, size_t cap) {
    size_t off = 0;
    while (off + 1 < cap) {
        ssize_t n = read(fd, buf + off, 1);
        if (n < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        if (n == 0) return -1;
        if (buf[off] == '\n') {
            buf[off] = 0;
            if (off > 0 && buf[off - 1] == '\r') buf[off - 1] = 0;
            return (ssize_t)strlen(buf);
        }
        off++;
    }
    return -1;
}

static void fail_all_pending(session_t *s) {
    pthread_mutex_lock(&s->plock);
    for (pending_t *p = s->pend; p; p = p->next) {
        if (!p->matched) {
            p->matched = 1;
            p->done = 1;
            p->body = NULL;
            pthread_cond_signal(&p->cv);
        }
    }
    pthread_mutex_unlock(&s->plock);
}

static void *reader_main(void *arg) {
    session_t *s = (session_t *)arg;
    char *line = malloc(LINE_MAX);
    if (!line) return NULL;

    while (1) {
        ssize_t n = read_line_blocking(s->out_fd, line, LINE_MAX);
        if (n < 0) break;
        if (n == 0) continue;

        const char *ida = NULL, *idb = NULL;
        if (find_toplevel_id(line, &ida, &idb) != 0) {
            logf_("[%s] child line without id: %.60s", s->sid, line);
            continue;
        }
        long long key = atoll(ida);

        pthread_mutex_lock(&s->plock);
        pending_t *p = s->pend;
        while (p && p->key != key) p = p->next;
        if (p) {
            p->body = strdup(line);
            p->body_len = (size_t)n;
            p->matched = 1;
            p->done = 1;
            if (s->inflight > 0) s->inflight--;
            pthread_cond_signal(&p->cv);
        } else {
            logf_("[%s] unmatched response id=%lld", s->sid, key);
        }
        pthread_mutex_unlock(&s->plock);
    }

    logf_("[%s] reader exit (child gone)", s->sid);
    pthread_mutex_lock(&s->plock);
    s->alive = 0;
    pthread_mutex_unlock(&s->plock);
    fail_all_pending(s);
    free(line);
    return NULL;
}

static session_t *session_new(void) {
    session_t *s = calloc(1, sizeof(session_t));
    if (!s) return NULL;

    int in_pipe[2], out_pipe[2];
    if (pipe(in_pipe) != 0 || pipe(out_pipe) != 0) { free(s); return NULL; }

    pid_t pid = fork();
    if (pid < 0) { free(s); return NULL; }

    if (pid == 0) {
        dup2(in_pipe[0], STDIN_FILENO);
        dup2(out_pipe[1], STDOUT_FILENO);
        int devnull = open("/dev/null", O_WRONLY);
        if (devnull >= 0) dup2(devnull, STDERR_FILENO);
        close(in_pipe[1]);
        close(out_pipe[0]);
        execl("/bin/sh", "sh", "-c", g_cmd, (char *)NULL);
        _exit(127);
    }

    close(in_pipe[0]);
    close(out_pipe[1]);
    s->pid = pid;
    s->in_fd = in_pipe[1];
    s->out_fd = out_pipe[0];
    s->alive = 1;
    s->last_used = time(NULL);
    pthread_mutex_init(&s->wlock, NULL);
    pthread_mutex_init(&s->plock, NULL);

    unsigned long long r = (unsigned long long)time(NULL) ^ ((unsigned long long)pid << 24)
                         ^ ((unsigned long long)now_ms() << 8);
    snprintf(s->sid, sizeof s->sid, "%016llx", r);

    spawn_detached(&s->reader, reader_main, s);

    s->next = g_sessions;
    g_sessions = s;
    logf_("session %s spawned pid=%d cmd=%s", s->sid, (int)pid, g_cmd);
    return s;
}

/* 原型取舍：关闭后不 free(session)，因为读线程可能还在读它的字段；
 * 进程退出时由内核回收，量级是每会话一条，不至于涨。 */
static void session_close(session_t *s) {
    if (!s) return;
    session_unlink(s);
    if (s->in_fd >= 0) close(s->in_fd);
    if (s->out_fd >= 0) close(s->out_fd);
    if (s->pid > 0) {
        kill(s->pid, SIGTERM);
        waitpid(s->pid, NULL, 0);
    }
    fail_all_pending(s);
    logf_("session %s closed pid=%d", s->sid, (int)s->pid);
}

static void reap_idle(void) {
    time_t now = time(NULL);
    pthread_mutex_lock(&g_slock);
    session_t *s = g_sessions;
    while (s) {
        session_t *next = s->next;
        if (s->inflight == 0 && now - s->last_used > IDLE_TTL_SEC) session_close(s);
        s = next;
    }
    pthread_mutex_unlock(&g_slock);
}

/* ===================== HTTP ===================== */

static int send_all(int fd, const char *buf, size_t len) {
    size_t off = 0;
    while (off < len) {
        ssize_t n = send(fd, buf + off, len - off, 0);
        if (n < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        off += (size_t)n;
    }
    return 0;
}

static void respond(int fd, int status, const char *reason, const char *ctype,
                    const char *extra_headers, const char *body, size_t blen) {
    char head[1024];
    int n = snprintf(head, sizeof head,
                     "HTTP/1.1 %d %s\r\n"
                     "Content-Type: %s\r\n"
                     "Content-Length: %zu\r\n"
                     "%s"
                     "Connection: close\r\n\r\n",
                     status, reason, ctype, blen, extra_headers ? extra_headers : "");
    if (n > 0) send_all(fd, head, (size_t)n);
    if (blen) send_all(fd, body, blen);
}

static int header_value(const char *req, const char *name, char *out, size_t cap) {
    size_t nlen = strlen(name);
    const char *p = req;
    while ((p = strcasestr(p, name)) != NULL) {
        if ((p == req || p[-1] == '\n') && p[nlen] == ':') {
            p += nlen + 1;
            while (*p == ' ' || *p == '\t') p++;
            size_t i = 0;
            while (p[i] && p[i] != '\r' && p[i] != '\n' && i + 1 < cap) {
                out[i] = p[i];
                i++;
            }
            out[i] = 0;
            return 0;
        }
        p += nlen;
    }
    return -1;
}

static void handle_rpc(int fd, const char *req_headers, const char *body, size_t blen) {
    char sidbuf[128] = {0};
    int has_sid = header_value(req_headers, "mcp-session-id", sidbuf, sizeof sidbuf) == 0;

    pthread_mutex_lock(&g_slock);
    session_t *s = has_sid ? session_find(sidbuf) : NULL;
    if (!s) {
        if (session_count() >= MAX_SESSIONS) {
            session_t *victim = NULL;
            for (session_t *it = g_sessions; it; it = it->next)
                if (it->inflight == 0 && (!victim || it->last_used < victim->last_used)) victim = it;
            if (victim) session_close(victim);
        }
        if (session_count() >= MAX_SESSIONS) {
            pthread_mutex_unlock(&g_slock);
            static const char *err = "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32001,\"message\":\"gateway: too many sessions\"}}";
            respond(fd, 503, "Service Unavailable", "application/json", NULL, err, strlen(err));
            return;
        }
        s = session_new();
        if (!s) {
            pthread_mutex_unlock(&g_slock);
            static const char *err = "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32603,\"message\":\"gateway: spawn failed\"}}";
            respond(fd, 500, "Internal Server Error", "application/json", NULL, err, strlen(err));
            return;
        }
    }
    s->last_used = time(NULL);
    pthread_mutex_unlock(&g_slock);

    const char *ida = NULL, *idb = NULL;
    if (find_toplevel_id(body, &ida, &idb) != 0) {
        pthread_mutex_lock(&s->wlock);
        int wr = (write(s->in_fd, body, blen) >= 0) && (write(s->in_fd, "\n", 1) == 1);
        pthread_mutex_unlock(&s->wlock);
        if (!wr) {
            static const char *err = "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32603,\"message\":\"gateway: child write failed\"}}";
            respond(fd, 502, "Bad Gateway", "application/json", NULL, err, strlen(err));
            return;
        }
        respond(fd, 202, "Accepted", "application/json", NULL, "", 0);
        return;
    }

    char orig_id[256];
    size_t orig_len = (size_t)(idb - ida);
    if (orig_len >= sizeof orig_id) orig_len = sizeof orig_id - 1;
    memcpy(orig_id, ida, orig_len);
    orig_id[orig_len] = 0;

    long long key = next_internal_id();
    char keybuf[32];
    snprintf(keybuf, sizeof keybuf, "%lld", key);

    char *newbody = replace_toplevel_id(body, keybuf, NULL);
    if (!newbody) {
        static const char *err = "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"message\":\"gateway: bad id\"}}";
        respond(fd, 400, "Bad Request", "application/json", NULL, err, strlen(err));
        return;
    }

    pending_t p;
    memset(&p, 0, sizeof p);
    p.key = key;
    pthread_cond_init(&p.cv, NULL);

    pthread_mutex_lock(&s->plock);
    p.next = s->pend;
    s->pend = &p;
    s->inflight++;
    pthread_mutex_unlock(&s->plock);

    pthread_mutex_lock(&s->wlock);
    int wr = (write(s->in_fd, newbody, strlen(newbody)) >= 0) &&
             (write(s->in_fd, "\n", 1) == 1);
    pthread_mutex_unlock(&s->wlock);
    free(newbody);

    if (!wr) {
        pthread_mutex_lock(&s->plock);
        pending_t **pp = &s->pend;
        while (*pp && *pp != &p) pp = &(*pp)->next;
        if (*pp) *pp = p.next;
        if (s->inflight > 0) s->inflight--;
        pthread_mutex_unlock(&s->plock);
        pthread_cond_destroy(&p.cv);
        static const char *err = "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32603,\"message\":\"gateway: child write failed\"}}";
        respond(fd, 502, "Bad Gateway", "application/json", NULL, err, strlen(err));
        return;
    }

    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    ts.tv_sec += CALL_TIMEOUT_SEC;

    pthread_mutex_lock(&s->plock);
    int timed_out = 0;
    while (!p.done) {
        int rc = pthread_cond_timedwait(&p.cv, &s->plock, &ts);
        if (rc == ETIMEDOUT) { timed_out = 1; break; }
    }
    int have = p.done && p.matched && p.body != NULL;
    char *resp = have ? p.body : NULL;
    size_t resp_len = have ? p.body_len : 0;

    pending_t **pp = &s->pend;
    while (*pp && *pp != &p) pp = &(*pp)->next;
    if (*pp) *pp = p.next;
    if (timed_out && s->inflight > 0) s->inflight--;
    pthread_mutex_unlock(&s->plock);
    pthread_cond_destroy(&p.cv);

    if (!have) {
        char errbuf[512];
        snprintf(errbuf, sizeof errbuf,
                 "{\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":%d,\"message\":\"%s\"}}",
                 orig_id[0] ? orig_id : "null",
                 timed_out ? -32000 : -32002,
                 timed_out ? "gateway: tool call timeout" : "gateway: child unavailable");
        if (resp) free(resp);
        respond(fd, timed_out ? 504 : 502, timed_out ? "Gateway Timeout" : "Bad Gateway",
                "application/json", NULL, errbuf, strlen(errbuf));
        return;
    }

    size_t out_len = 0;
    char *out = replace_toplevel_id(resp, orig_id, &out_len);
    char hdrs[256] = {0};
    snprintf(hdrs, sizeof hdrs, "mcp-session-id: %s\r\n", s->sid);
    if (out) {
        respond(fd, 200, "OK", "application/json", hdrs, out, out_len);
        free(out);
    } else {
        respond(fd, 200, "OK", "application/json", hdrs, resp, resp_len);
    }
    free(resp);
}

static void *conn_main(void *arg) {
    int fd = *(int *)arg;
    free(arg);

    char req[REQ_MAX];
    size_t off = 0;
    long long deadline = now_ms() + 10000;

    while (off + 1 < sizeof req) {
        long long left = deadline - now_ms();
        if (left <= 0) break;
        struct pollfd pfd = { .fd = fd, .events = POLLIN };
        if (poll(&pfd, 1, (int)left) <= 0) break;
        ssize_t n = recv(fd, req + off, sizeof req - 1 - off, 0);
        if (n <= 0) break;
        off += (size_t)n;
        req[off] = 0;
        if (strstr(req, "\r\n\r\n")) break;
    }
    req[off] = 0;

    char method[16] = {0}, path[256] = {0};
    if (sscanf(req, "%15s %255s", method, path) != 2) {
        respond(fd, 400, "Bad Request", "text/plain", NULL, "bad request", 11);
        close(fd);
        return NULL;
    }

    if (strcmp(method, "GET") == 0 && strcmp(path, "/health") == 0) {
        respond(fd, 200, "OK", "text/plain", NULL, "ok", 2);
        close(fd);
        return NULL;
    }
    if (strcmp(method, "GET") == 0 && strcmp(path, "/mcp") == 0) {
        respond(fd, 405, "Method Not Allowed", "text/plain", "Allow: POST, DELETE\r\n",
                "gateway: GET /mcp (server push) not implemented", 46);
        close(fd);
        return NULL;
    }
    if (strcmp(method, "DELETE") == 0) {
        char sidbuf[128] = {0};
        if (header_value(req, "mcp-session-id", sidbuf, sizeof sidbuf) == 0) {
            pthread_mutex_lock(&g_slock);
            session_t *s = session_find(sidbuf);
            if (s) session_close(s);
            pthread_mutex_unlock(&g_slock);
        }
        respond(fd, 200, "OK", "text/plain", NULL, "ok", 2);
        close(fd);
        return NULL;
    }
    if (!(strcmp(method, "POST") == 0 && strcmp(path, "/mcp") == 0)) {
        respond(fd, 404, "Not Found", "text/plain", NULL, "not found", 9);
        close(fd);
        return NULL;
    }

    char clen[32] = {0};
    size_t body_len = 0;
    if (header_value(req, "content-length", clen, sizeof clen) == 0)
        body_len = (size_t)strtoul(clen, NULL, 10);

    const char *body_start = strstr(req, "\r\n\r\n");
    if (!body_start) {
        respond(fd, 400, "Bad Request", "text/plain", NULL, "no headers", 10);
        close(fd);
        return NULL;
    }
    body_start += 4;

    char body[REQ_MAX];
    if (body_len + 1 > sizeof body) {
        respond(fd, 413, "Payload Too Large", "text/plain", NULL, "too large", 9);
        close(fd);
        return NULL;
    }
    size_t already = off - (size_t)(body_start - req);
    size_t got = already < body_len ? already : body_len;
    memcpy(body, body_start, got);
    while (got < body_len) {
        ssize_t n = recv(fd, body + got, body_len - got, 0);
        if (n <= 0) break;
        got += (size_t)n;
    }
    body[got] = 0;

    handle_rpc(fd, req, body, got);
    close(fd);
    return NULL;
}

int main(int argc, char **argv) {
    if (argc < 3) {
        fprintf(stderr, "usage: %s <port> <stdio-command>\n", argv[0]);
        return 2;
    }
    int port = atoi(argv[1]);
    snprintf(g_cmd, sizeof g_cmd, "%s", argv[2]);

    signal(SIGPIPE, SIG_IGN);

    int ls = socket(AF_INET, SOCK_STREAM, 0);
    if (ls < 0) { perror("socket"); return 1; }
    int one = 1;
    setsockopt(ls, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);

    struct sockaddr_in addr;
    memset(&addr, 0, sizeof addr);
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = htonl(INADDR_ANY);
    addr.sin_port = htons((uint16_t)port);
    if (bind(ls, (struct sockaddr *)&addr, sizeof addr) != 0) { perror("bind"); return 1; }
    if (listen(ls, 64) != 0) { perror("listen"); return 1; }
    logf_("mcp_gateway listening on :%d cmd=%s", port, g_cmd);

    int ticks = 0;
    for (;;) {
        int cfd = accept(ls, NULL, NULL);
        if (cfd < 0) {
            if (errno == EINTR) continue;
            break;
        }
        if (++ticks % 256 == 0) reap_idle();

        int *arg = malloc(sizeof(int));
        if (!arg) { close(cfd); continue; }
        *arg = cfd;
        pthread_t th;
        if (spawn_detached(&th, conn_main, arg) != 0) { free(arg); close(cfd); }
    }
    return 0;
}
