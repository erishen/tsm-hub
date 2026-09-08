// Command mockupstream 是一个假的 OpenAI 兼容服务，用于本地联调与冒烟测试。
//
// 用法：mockupstream -addr :8799 -mode ok|stream|500
// 它不调用任何真实模型，只按 OpenAI 协议返回固定内容，适合验证路由与用量统计。
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", ":8799", "listen address")
	mode := flag.String("mode", "ok", "ok | stream | 500")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if *mode == "500" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		writeJSON(w, map[string]any{
			"object": "list",
			"data": []any{
				map[string]any{"id": "mock-model", "object": "model", "owned_by": "mock", "context_length": 131072},
				map[string]any{"id": "mock-extra", "object": "model", "owned_by": "mock", "context_length": 262144, "is_free": true},
			},
		})
	})
	mux.HandleFunc("/v1/users/me/balance", func(w http.ResponseWriter, r *http.Request) {
		// Moonshot 风格余额：模拟"全赠送额度"（券余额>0、现金=0 → 免费额度）
		writeJSON(w, map[string]any{
			"code": 0,
			"data": map[string]any{
				"available_balance": 14.99736,
				"voucher_balance":   14.99736,
				"cash_balance":      0,
			},
			"scode":  "0x0",
			"status": true,
		})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		model, _ := req["model"].(string)
		stream, _ := req["stream"].(bool)

		if *mode == "500" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"mock upstream failure"}}`))
			return
		}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fl := w.(http.Flusher)
			for _, word := range []string{"你", "好", "，", "世", "界"} {
				chunk := map[string]any{
					"choices": []any{map[string]any{
						"delta": map[string]any{"content": word},
					}},
				}
				b, _ := json.Marshal(chunk)
				_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
				fl.Flush()
				time.Sleep(30 * time.Millisecond)
			}
			last := map[string]any{
				"choices": []any{},
				"usage":   map[string]any{"prompt_tokens": 9, "completion_tokens": 5, "total_tokens": 14},
			}
			b, _ := json.Marshal(last)
			_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			fl.Flush()
			return
		}
		writeJSON(w, map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion",
			"model":   model,
			"created": time.Now().Unix(),
			"choices": []any{map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "你好，世界"},
			}},
			"usage": map[string]any{"prompt_tokens": 9, "completion_tokens": 5, "total_tokens": 14},
		})
	})

	log.Printf("mock upstream listening on %s (mode=%s)", *addr, *mode)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
