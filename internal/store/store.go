package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaskedSecretMarker 是脱敏回显时插入的省略号标记。
// 编辑请求里若 api_key 含该标记，说明是管理台回显的脱敏值，后端应保留原值而不是覆盖。
const MaskedSecretMarker = "…"

// Store 是配置的内存态 + 落盘器。所有读操作走内存索引，写操作加锁后原子落盘。
type Store struct {
	path string
	// unavailablePath 是冷却状态的独立落盘文件（data/unavailable.json），
	// 与 config.json 分离，避免高频冷却写入频繁重写主配置。
	unavailablePath string

	mu  sync.RWMutex
	cfg Config
	// 索引，避免每次线性扫描。
	providers map[string]int // id -> index in cfg.Providers
	routes    map[string]int // model -> index in cfg.Routes
	keys      map[string]int // key id -> index in cfg.Keys
	keyHash   map[string]int // sha256 hex -> index in cfg.Keys

	// unavailable 是「某 provider 上某模型被上游判为不可用」的运行时状态。
	// providerID -> model -> 原因+到期。变更即写 unavailable.json，重启后恢复。
	unavailable map[string]map[string]UnavailableModel
}

// New 打开（必要时创建）指定路径的配置。
func New(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	s := &Store{
		path:            path,
		unavailablePath: filepath.Join(filepath.Dir(path), "unavailable.json"),
		unavailable:     map[string]map[string]UnavailableModel{},
	}
	if err := s.loadUnavailable(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		s.cfg = defaultConfig()
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	} else {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := json.Unmarshal(raw, &s.cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	s.applyDefaults()
	s.reindex()
	return s, nil
}

func defaultConfig() Config {
	return Config{
		Version: CurrentVersion,
		Settings: Settings{
			Listen:           ":9070",
			AdminToken:       DefaultAdminToken,
			DefaultTimeoutMS: 120000,
			MaxBodyBytes:     8 << 20,
			FailThreshold:    3,
			CooldownSec:      60,
			Pricing: map[string]Price{
				"default": {InputPer1K: 0.001, OutputPer1K: 0.002},
			},
		},
	}
}

func (s *Store) applyDefaults() {
	if s.cfg.Version == 0 {
		s.cfg.Version = CurrentVersion
	}
	st := &s.cfg.Settings
	if st.Listen == "" {
		st.Listen = ":9070"
	}
	if st.AdminToken == "" {
		st.AdminToken = DefaultAdminToken
	}
	if st.DefaultTimeoutMS <= 0 {
		st.DefaultTimeoutMS = 120000
	}
	if st.MaxBodyBytes <= 0 {
		st.MaxBodyBytes = 8 << 20
	}
	if st.FailThreshold <= 0 {
		st.FailThreshold = 3
	}
	if st.CooldownSec <= 0 {
		st.CooldownSec = 60
	}
	if st.Pricing == nil {
		st.Pricing = map[string]Price{"default": {InputPer1K: 0.001, OutputPer1K: 0.002}}
	}
	if !st.Sandbox.Enabled && st.Sandbox.TimeoutSec == 0 && st.Sandbox.MemoryMB == 0 {
		st.Sandbox.TimeoutSec = 30
		st.Sandbox.MemoryMB = 512
		st.Sandbox.CPUs = 1
		st.Sandbox.MaxOutputKB = 100
	}
	if !st.Fastpath.Enabled {
		// 默认启用：内置快路径零成本，codegen 会消耗一次 LLM，默认也开（复用免费 chat）。
		st.Fastpath.Enabled = true
		st.Fastpath.Codegen = true
	}
}

func (s *Store) reindex() {
	s.providers = make(map[string]int, len(s.cfg.Providers))
	for i, p := range s.cfg.Providers {
		s.providers[p.ID] = i
	}
	s.routes = make(map[string]int, len(s.cfg.Routes))
	for i, r := range s.cfg.Routes {
		s.routes[r.Model] = i
	}
	s.keys = make(map[string]int, len(s.cfg.Keys))
	s.keyHash = make(map[string]int, len(s.cfg.Keys))
	for i, k := range s.cfg.Keys {
		s.keys[k.ID] = i
		s.keyHash[k.Hash] = i
	}
}

// Path 返回配置文件路径。
func (s *Store) Path() string { return s.path }

// DataDir 返回数据目录（config.json 所在目录，派生数据文件如 memory.db 放这里）。
func (s *Store) DataDir() string { return filepath.Dir(s.path) }

func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Settings
}

// saveLocked 原子落盘：先写同目录临时文件，再 rename 覆盖。
func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp config: %w", err)
	}
	return os.Rename(tmpName, s.path)
}

// ListExternalTools 返回已录用的外部工具列表。
func (s *Store) ListExternalTools() []ExternalTool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExternalTool, len(s.cfg.ExternalTools))
	copy(out, s.cfg.ExternalTools)
	return out
}

// ExternalTool 返回某个已录用外部工具。
func (s *Store) ExternalTool(name string) (ExternalTool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.cfg.ExternalTools {
		if t.Name == name {
			return t, true
		}
	}
	return ExternalTool{}, false
}

// AdoptExternalTool 录用/更新一个外部工具（upsert 后落盘）。
func (s *Store) AdoptExternalTool(t ExternalTool) error {
	return s.Update(func(c *Config) error {
		for i := range c.ExternalTools {
			if c.ExternalTools[i].Name == t.Name {
				c.ExternalTools[i] = t
				return nil
			}
		}
		c.ExternalTools = append(c.ExternalTools, t)
		return nil
	})
}

// DeleteExternalTool 取消录用某个外部工具。
func (s *Store) DeleteExternalTool(name string) error {
	return s.Update(func(c *Config) error {
		for i := range c.ExternalTools {
			if c.ExternalTools[i].Name == name {
				c.ExternalTools = append(c.ExternalTools[:i], c.ExternalTools[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("external tool %q not adopted", name)
	})
}

// Update 在锁内修改配置并落盘。fn 返回 error 时不写盘。
func (s *Store) Update(fn func(c *Config) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft := s.cfg
	// 深拷贝切片，避免 fn 失败时留下半成品。
	draft.Providers = append([]Provider(nil), s.cfg.Providers...)
	draft.Routes = append([]Route(nil), s.cfg.Routes...)
	draft.Keys = append([]APIKey(nil), s.cfg.Keys...)
	if err := fn(&draft); err != nil {
		return err
	}
	draft.Version = CurrentVersion
	s.cfg = draft
	s.reindex()
	return s.saveLocked()
}

// ---------- Provider ----------

// loadUnavailable 从 unavailable.json 恢复冷却状态（过期项丢弃）。
// 文件不存在视为全新启动。
func (s *Store) loadUnavailable() error {
	b, err := os.ReadFile(s.unavailablePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read unavailable: %w", err)
	}
	var m map[string]map[string]UnavailableModel
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("parse unavailable: %w", err)
	}
	now := time.Now()
	for pid, mm := range m {
		for model, u := range mm {
			if now.After(u.Until) {
				delete(mm, model)
			}
		}
		if len(mm) == 0 {
			delete(m, pid)
		}
	}
	s.unavailable = m
	if s.unavailable == nil {
		s.unavailable = map[string]map[string]UnavailableModel{}
	}
	return nil
}

// persistUnavailable 把未过期的冷却状态写回 unavailable.json（调用方持有锁）。
// 写失败仅忽略：冷却是运行时优化，丢失只会导致重启后重试一次坏 provider。
func (s *Store) persistUnavailable() {
	now := time.Now()
	out := map[string]map[string]UnavailableModel{}
	for pid, mm := range s.unavailable {
		for model, u := range mm {
			if now.After(u.Until) {
				continue
			}
			if out[pid] == nil {
				out[pid] = map[string]UnavailableModel{}
			}
			out[pid][model] = u
		}
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.unavailablePath, b, 0o600)
}

// MarkModelUnavailable 记录某 provider 上某模型被上游判为不可用（如 404 model not found），
// 有效期 ttl，期间 ModelUnavailable 返回原因。变更即持久化，重启后恢复。
func (s *Store) MarkModelUnavailable(providerID, model, reason string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.unavailable[providerID]
	if m == nil {
		m = map[string]UnavailableModel{}
		s.unavailable[providerID] = m
	}
	m[model] = UnavailableModel{Reason: reason, Until: time.Now().Add(ttl)}
	s.persistUnavailable()
}

// ModelUnavailable 判断某 provider 上某模型当前是否被标记为不可用（未过期）。
// 返回原因与 true；未标记或已过期返回 "" 与 false。
func (s *Store) ModelUnavailable(providerID, model string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.unavailable[providerID]
	if !ok {
		return "", false
	}
	u, ok := m[model]
	if !ok || time.Now().After(u.Until) {
		return "", false
	}
	return u.Reason, true
}

// UnavailableSnapshot 导出当前全部不可用模型（未过期的），供管理台展示/排查。
func (s *Store) UnavailableSnapshot() []UnavailableModelView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]UnavailableModelView, 0)
	for pid, m := range s.unavailable {
		for model, u := range m {
			if time.Now().After(u.Until) {
				continue
			}
			out = append(out, UnavailableModelView{ProviderID: pid, Model: model, Reason: u.Reason, Until: u.Until})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProviderID != out[j].ProviderID {
			return out[i].ProviderID < out[j].ProviderID
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func (s *Store) ListProviders() []Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Provider(nil), s.cfg.Providers...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) GetProvider(id string) (Provider, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.providers[id]
	if !ok {
		return Provider{}, false
	}
	return s.cfg.Providers[i], true
}

// UpsertProvider 新增或整体替换一个 provider（id 已存在则覆盖）。
func (s *Store) UpsertProvider(p Provider) error {
	now := time.Now()
	return s.Update(func(c *Config) error {
		if p.ID == "" {
			return fmt.Errorf("provider id is required")
		}
		if p.Weight <= 0 {
			p.Weight = 100
		}
		if p.TimeoutMS <= 0 {
			p.TimeoutMS = c.Settings.DefaultTimeoutMS
		}
		p.UpdatedAt = now
		for i := range c.Providers {
			if c.Providers[i].ID == p.ID {
				p.CreatedAt = c.Providers[i].CreatedAt
				// 编辑时回显的 api_key 是脱敏值（含省略号），必须保留原 key，
				// 否则"读出来再写回去"会把真实 Key 覆盖成脱敏串。
				if strings.Contains(p.APIKey, MaskedSecretMarker) {
					p.APIKey = c.Providers[i].APIKey
				}
				c.Providers[i] = p
				return nil
			}
		}
		p.CreatedAt = now
		c.Providers = append(c.Providers, p)
		return nil
	})
}

func (s *Store) DeleteProvider(id string) error {
	return s.Update(func(c *Config) error {
		kept := c.Providers[:0]
		found := false
		for _, p := range c.Providers {
			if p.ID == id {
				found = true
				continue
			}
			kept = append(kept, p)
		}
		if !found {
			return fmt.Errorf("provider %q not found", id)
		}
		c.Providers = kept
		// 同步清理路由表里指向它的候选。
		for i := range c.Routes {
			t := c.Routes[i].Targets[:0]
			for _, tg := range c.Routes[i].Targets {
				if tg.ProviderID == id {
					continue
				}
				t = append(t, tg)
			}
			c.Routes[i].Targets = t
		}
		c.Routes = dropEmptyRoutes(c.Routes)
		return nil
	})
}

func dropEmptyRoutes(in []Route) []Route {
	out := in[:0]
	for _, r := range in {
		if len(r.Targets) == 0 {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ---------- Route ----------

func (s *Store) ListRoutes() []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Route(nil), s.cfg.Routes...)
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

func (s *Store) UpsertRoute(r Route) error {
	return s.Update(func(c *Config) error {
		if r.Strategy == "" {
			r.Strategy = "weighted"
		}
		if r.Strategy != "weighted" && r.Strategy != "failover" && r.Strategy != "smart" {
			return fmt.Errorf("unknown strategy %q", r.Strategy)
		}
		// Model 留空 = 通配兜底路由（全局唯一）：任何未命中显式路由的模型都走它。
		for i := range c.Routes {
			if c.Routes[i].Model == r.Model {
				c.Routes[i] = r
				return nil
			}
		}
		c.Routes = append(c.Routes, r)
		return nil
	})
}

func (s *Store) DeleteRoute(model string) error {
	return s.Update(func(c *Config) error {
		kept := c.Routes[:0]
		found := false
		for _, r := range c.Routes {
			if r.Model == model {
				found = true
				continue
			}
			kept = append(kept, r)
		}
		if !found {
			return fmt.Errorf("route %q not found", model)
		}
		c.Routes = kept
		return nil
	})
}

// ---------- Key ----------

func (s *Store) ListKeys() []APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]APIKey(nil), s.cfg.Keys...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) AddKey(k APIKey) error {
	return s.Update(func(c *Config) error {
		if k.ID == "" {
			return fmt.Errorf("key id is required")
		}
		if _, dup := s.keyHash[k.Hash]; dup {
			return fmt.Errorf("key hash already exists")
		}
		c.Keys = append(c.Keys, k)
		return nil
	})
}

func (s *Store) DeleteKey(id string) error {
	return s.Update(func(c *Config) error {
		kept := c.Keys[:0]
		found := false
		for _, k := range c.Keys {
			if k.ID == id {
				found = true
				continue
			}
			kept = append(kept, k)
		}
		if !found {
			return fmt.Errorf("key %q not found", id)
		}
		c.Keys = kept
		return nil
	})
}

// SetKeyEnabled 启用 / 停用某个 Key。
func (s *Store) SetKeyEnabled(id string, enabled bool) error {
	return s.Update(func(c *Config) error {
		for i := range c.Keys {
			if c.Keys[i].ID == id {
				c.Keys[i].Enabled = enabled
				return nil
			}
		}
		return fmt.Errorf("key %q not found", id)
	})
}

// UpdateKey 更新已有 key 的可编辑字段（Name/Models/Quota/InjectSkills）。
func (s *Store) UpdateKey(id string, name string, models []string, quota Quota, injectSkills string, agentDisabled bool) error {
	return s.Update(func(c *Config) error {
		for i := range c.Keys {
			if c.Keys[i].ID == id {
				c.Keys[i].Name = name
				c.Keys[i].Models = models
				c.Keys[i].Quota = quota
				c.Keys[i].InjectSkills = injectSkills
				c.Keys[i].AgentDisabled = agentDisabled
				return nil
			}
		}
		return fmt.Errorf("key %q not found", id)
	})
}

// LookupKeyHash 用 sha256 哈希查 Key，热路径（每次代理请求都会走）。
func (s *Store) LookupKeyHash(hash string) (APIKey, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.keyHash[hash]
	if !ok {
		return APIKey{}, false
	}
	return s.cfg.Keys[i], true
}

// Reload 从磁盘重新读取 config.json 并重建索引（热加载入口）。
// 解析失败时保持原配置不变并返回错误；成功则 Provider/Route/Key 立即生效。
func (s *Store) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", s.path, err)
	}
	cfg.Version = CurrentVersion
	s.cfg = cfg
	s.applyDefaults()
	s.reindex()
	return nil
}
