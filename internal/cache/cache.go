// Package cache ports estimate/src/cache.ts of the Node estimate (manager): the
// estimate's cache under ~/.cache/dormeto/estimate/<organization or account>/,
// byte-compatible with Node's files. A file is {"writtenAt": <ms since the
// epoch>, "value": ...}, written to a temporary name then renamed (an
// interrupted run never leaves a broken one); one that cannot be parsed is
// missing. prices.json is {"<key>": {"usd": number|null, "until": <ms>}}.
package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dormeto/cli/internal/prices"
)

const day = 24 * time.Hour

// rename is replaceable by tests to inject a failure.
var rename = os.Rename

var sequence atomic.Int64

// Dir is $XDG_CACHE_HOME/dormeto/estimate, else ~/.cache/dormeto/estimate, on every OS.
func Dir(env func(string) string) string {
	if x := env("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "dormeto", "estimate")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "dormeto", "estimate")
}

// FreshDiscovery: discovery is fresh a day.
func FreshDiscovery(now time.Time) func(time.Time) bool {
	return func(w time.Time) bool { return now.Sub(w) < day }
}

// FreshRequests: requests (and uptime) are fresh while their days (--days) end on the run's UTC day.
func FreshRequests(now time.Time) func(time.Time) bool {
	return func(w time.Time) bool { return w.UTC().Format("2006-01-02") == now.UTC().Format("2006-01-02") }
}

// Cache is the estimate's file cache.
type Cache struct {
	dir string
	now func() time.Time

	mu     sync.Mutex // guards the prices and their writes
	loaded bool
	prices map[string]priceEntry
}

type priceEntry struct {
	USD   *float64 `json:"usd"`
	Until int64    `json:"until"`
}

// New returns a cache under dir; now is the clock (time.Now when nil).
func New(dir string, now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{dir: dir, now: now}
}

func writeAtomically(file string, data []byte) (err error) {
	if err = os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.%d.tmp", file, os.Getpid(), sequence.Add(1))
	if err = os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err = rename(tmp, file); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Get reads path (relative to the cache directory) into out when the file is
// parsable, has a writtenAt and a value, and fresh accepts its time. Anything
// else, a missing or unparsable file included, is (false, nil).
func (c *Cache) Get(path string, fresh func(writtenAt time.Time) bool, out any) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(c.dir, path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, nil
	}
	var env struct {
		WrittenAt *float64        `json:"writtenAt"`
		Value     json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &env) != nil || env.WrittenAt == nil || env.Value == nil || !fresh(time.UnixMilli(int64(*env.WrittenAt))) {
		return false, nil
	}
	if json.Unmarshal(env.Value, out) != nil {
		return false, nil
	}
	return true, nil
}

// Put writes value with the time now, atomically.
func (c *Cache) Put(path string, value any) error {
	data, err := marshal(struct {
		WrittenAt int64 `json:"writtenAt"`
		Value     any   `json:"value"`
	}{c.now().UnixMilli(), value})
	if err != nil {
		return err
	}
	return writeAtomically(filepath.Join(c.dir, path), data)
}

// marshal is json.Marshal without HTML escaping, as JSON.stringify.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// Prices is the price cache kept in prices.json.
func (c *Cache) Prices() prices.Cache { return (*priceCache)(c) }

type priceCache Cache

func (p *priceCache) load() {
	if p.loaded {
		return
	}
	p.loaded = true
	p.prices = map[string]priceEntry{}
	if raw, err := os.ReadFile(filepath.Join(p.dir, "prices.json")); err == nil {
		var m map[string]priceEntry
		if json.Unmarshal(raw, &m) == nil && m != nil {
			p.prices = m
		}
	}
}

func (p *priceCache) GetPrice(key string) (*prices.Price, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	e, ok := p.prices[key]
	if !ok || e.Until <= p.now().UnixMilli() {
		return nil, nil
	}
	return &prices.Price{USD: e.USD}, nil
}

func (p *priceCache) PutPrice(key string, usd *float64, days int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.load()
	d := time.Duration(days) * day
	if usd == nil {
		d = day
	}
	p.prices[key] = priceEntry{usd, p.now().Add(d).UnixMilli()}
	data, err := marshal(p.prices)
	if err != nil {
		return err
	}
	return writeAtomically(filepath.Join(p.dir, "prices.json"), data)
}
