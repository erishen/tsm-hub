// Package quota 负责用量记录、聚合与限流。
//
// 用量以 JSONL 追加写入 data/usage/YYYY-MM-DD.jsonl（每行一条请求），
// 进程启动时回放最近 N 天的流水重建内存聚合，用于额度判断与看板展示。
package quota

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/erishen/llm-router/internal/store"
)

// Agg 是一组用量指标的累加值。
type Agg struct {
	Requests        int     `json:"requests"`
	PromptTokens    int64   `json:"prompt_tokens"`
	CompletionToken int64   `json:"completion_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CostUSD         float64 `json:"cost_usd"`
	Errors          int     `json:"errors"`
}

func (a *Agg) add(r store.UsageRecord) {
	a.Requests++
	a.PromptTokens += int64(r.PromptTokens)
	a.CompletionToken += int64(r.CompletionToken)
	a.TotalTokens += int64(r.TotalTokens)
	a.CostUSD += r.CostUSD
	if r.Error != "" || r.Status >= 400 {
		a.Errors++
	}
}

func (a Agg) Add(o Agg) Agg {
	return Agg{
		Requests:        a.Requests + o.Requests,
		PromptTokens:    a.PromptTokens + o.PromptTokens,
		CompletionToken: a.CompletionToken + o.CompletionToken,
		TotalTokens:     a.TotalTokens + o.TotalTokens,
		CostUSD:         a.CostUSD + o.CostUSD,
		Errors:          a.Errors + o.Errors,
	}
}

// Recorder 记录用量流水并维护内存聚合。
type Recorder struct {
	dir string

	mu     sync.Mutex
	file   *os.File
	day    string
	writer *bufio.Writer

	totals map[string]*Agg            // keyID -> 累计
	daily  map[string]map[string]*Agg // YYYY-MM-DD -> keyID -> 累计
	models map[string]*Agg            // model -> 累计
}

// NewRecorder 打开用量目录并回放历史（默认最近 90 天）。
func NewRecorder(dir string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create usage dir: %w", err)
	}
	r := &Recorder{
		dir:    dir,
		totals: map[string]*Agg{},
		daily:  map[string]map[string]*Agg{},
		models: map[string]*Agg{},
	}
	if err := r.replay(90); err != nil {
		return nil, err
	}
	return r, nil
}

func dayKey(t time.Time) string { return t.Format("2006-01-02") }

// replay 扫描最近 maxDays 天的 JSONL 重建聚合。
func (r *Recorder) replay(maxDays int) error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return fmt.Errorf("read usage dir: %w", err)
	}
	cutoff := time.Now().AddDate(0, 0, -maxDays).Format("2006-01-02")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".jsonl") {
			continue
		}
		d := strings.TrimSuffix(n, ".jsonl")
		if d < cutoff {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := r.replayFile(filepath.Join(r.dir, n)); err != nil {
			return err
		}
	}
	return nil
}

func (r *Recorder) replayFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec store.UsageRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue // 跳过损坏行，不阻塞启动
		}
		r.accumulate(rec)
	}
	return sc.Err()
}

func (r *Recorder) accumulate(rec store.UsageRecord) {
	a := r.totals[rec.KeyID]
	if a == nil {
		a = &Agg{}
		r.totals[rec.KeyID] = a
	}
	a.add(rec)

	d := dayKey(rec.TS)
	dm := r.daily[d]
	if dm == nil {
		dm = map[string]*Agg{}
		r.daily[d] = dm
	}
	da := dm[rec.KeyID]
	if da == nil {
		da = &Agg{}
		dm[rec.KeyID] = da
	}
	da.add(rec)

	m := r.models[rec.Model]
	if m == nil {
		m = &Agg{}
		r.models[rec.Model] = m
	}
	m.add(rec)
}

// Record 追加一条用量（先更新内存再落盘，落盘失败不影响内存态）。
func (r *Recorder) Record(rec store.UsageRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	r.accumulate(rec)
	return r.appendLocked(rec)
}

func (r *Recorder) appendLocked(rec store.UsageRecord) error {
	d := dayKey(rec.TS)
	if r.file == nil || r.day != d {
		if err := r.rotateLocked(d); err != nil {
			return err
		}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := r.writer.Write(append(line, '\n')); err != nil {
		return err
	}
	return r.writer.Flush()
}

func (r *Recorder) rotateLocked(day string) error {
	if r.writer != nil {
		_ = r.writer.Flush()
		_ = r.file.Close()
		r.writer, r.file, r.day = nil, nil, ""
	}
	f, err := os.OpenFile(filepath.Join(r.dir, day+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open usage file: %w", err)
	}
	r.file, r.day, r.writer = f, day, bufio.NewWriterSize(f, 64*1024)
	return nil
}

// Close 落盘并关闭当前文件句柄。
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writer == nil {
		return nil
	}
	err := r.writer.Flush()
	r.writer, r.day = nil, ""
	if cerr := r.file.Close(); err == nil {
		err = cerr
	}
	r.file = nil
	return err
}

// Total 返回某个 Key 的历史累计用量。
func (r *Recorder) Total(keyID string) Agg {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.totals[keyID]; ok {
		return *a
	}
	return Agg{}
}

// Today 返回某个 Key 当天的用量。
func (r *Recorder) Today(keyID string) Agg {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := dayKey(time.Now())
	if dm, ok := r.daily[d]; ok {
		if a, ok := dm[keyID]; ok {
			return *a
		}
	}
	return Agg{}
}

// ByModel 返回模型维度的累计用量。
func (r *Recorder) ByModel() map[string]Agg {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]Agg, len(r.models))
	for k, v := range r.models {
		out[k] = *v
	}
	return out
}

// Daily 返回最近 n 天（含今天）的全局用量，按日期升序。
func (r *Recorder) Daily(n int) []DailyPoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	out := make([]DailyPoint, 0, n)
	for i := n - 1; i >= 0; i-- {
		d := dayKey(now.AddDate(0, 0, -i))
		var sum Agg
		for _, a := range r.daily[d] {
			sum = sum.Add(*a)
		}
		out = append(out, DailyPoint{Date: d, Agg: sum})
	}
	return out
}

// DailyPoint 是某天的用量点。
type DailyPoint struct {
	Date string `json:"date"`
	Agg
}

// PerKey 返回每个 Key 的累计用量。
func (r *Recorder) PerKey() map[string]Agg {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]Agg, len(r.totals))
	for k, v := range r.totals {
		out[k] = *v
	}
	return out
}

// Recent 返回最近 limit 条流水（从当天往前扫）。
func (r *Recorder) Recent(limit int) []store.UsageRecord {
	r.mu.Lock()
	days := make([]string, 0, len(r.daily))
	for d := range r.daily {
		days = append(days, d)
	}
	r.mu.Unlock()
	sort.Sort(sort.Reverse(sort.StringSlice(days)))

	out := make([]store.UsageRecord, 0, limit)
	for _, d := range days {
		f, err := os.Open(filepath.Join(r.dir, d+".jsonl"))
		if err != nil {
			continue
		}
		var lines []string
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
		f.Close()
		for i := len(lines) - 1; i >= 0 && len(out) < limit; i-- {
			var rec store.UsageRecord
			if err := json.Unmarshal([]byte(lines[i]), &rec); err != nil {
				continue
			}
			out = append(out, rec)
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}
