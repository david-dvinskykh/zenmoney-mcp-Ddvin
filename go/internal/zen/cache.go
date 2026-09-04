package zen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// CacheVersion 2: reminders and their markers joined the snapshot. A version 1
// file has none of them, and an incremental sync from its timestamp would never
// report the ones that already exist, so old snapshots are dropped rather than
// read.
const CacheVersion = 2

// CacheData is the on-disk snapshot. Field names match the TypeScript server's
// file byte for byte so both implementations can share one cache directory.
type CacheData struct {
	Version int `json:"version"`
	// SavedAt is the unix second the snapshot was written.
	SavedAt         int64            `json:"savedAt"`
	ServerTimestamp int64            `json:"serverTimestamp"`
	Accounts        []Account        `json:"accounts"`
	Tags            []Tag            `json:"tags"`
	Merchants       []Merchant       `json:"merchants"`
	Companies       []Company        `json:"companies"`
	Instruments     []Instrument     `json:"instruments"`
	Transactions    []Transaction    `json:"transactions"`
	Users           []User           `json:"users"`
	Reminders       []Reminder       `json:"reminders"`
	ReminderMarkers []ReminderMarker `json:"reminderMarkers"`
}

// TokenHash is a stable, non-reversible id for a token. It names the cache file
// so separate accounts never share a snapshot and the token itself is never
// written to disk.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:32]
}

// CacheBaseDir mirrors the TypeScript server's directory choice, including the
// ZENMONEY_CACHE_DIR override, so a snapshot written by either implementation
// is found by the other.
func CacheBaseDir() string {
	if override := os.Getenv("ZENMONEY_CACHE_DIR"); override != "" {
		return override
	}
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "zenmoney-mcp")
		}
	}
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "zenmoney-mcp")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".cache", "zenmoney-mcp")
}

// Cache is a file-backed snapshot of the synced state, keyed by the hash of the
// API token. It lets short-lived processes reuse a previous full sync instead of
// re-downloading everything on every launch.
type Cache struct {
	Path string
	// Writes are serialized so concurrent callers cannot interleave into a
	// half-written file.
	mu sync.Mutex
}

func NewCache(token string) *Cache {
	return NewCacheIn(token, CacheBaseDir())
}

func NewCacheIn(token, dir string) *Cache {
	return &Cache{Path: filepath.Join(dir, TokenHash(token)+".json")}
}

// Load returns the snapshot, or nil on a cache miss, a version mismatch or a
// corrupted file — all of which a full sync overwrites anyway.
func (c *Cache) Load() *CacheData {
	raw, err := os.ReadFile(c.Path)
	if err != nil {
		return nil
	}
	var data CacheData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil
	}
	if data.Version != CacheVersion {
		return nil
	}
	return &data
}

// Save atomically replaces the snapshot.
func (c *Cache) Save(data CacheData) error {
	data.Version = CacheVersion
	data.SavedAt = time.Now().Unix()

	c.mu.Lock()
	defer c.mu.Unlock()

	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", c.Path, os.Getpid())
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.Path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.Remove(c.Path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
