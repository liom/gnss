// Package cache handles reading and writing the last fix position cache.
// File: /var/lib/gnss-probe/last_fix.json (spec section 3.4)
package cache

import (
	"encoding/json"
	"os"
	"time"
)

const DefaultPath = "/var/lib/gnss-probe/last_fix.json"

type Fix struct {
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Alt       float64 `json:"alt"`
	Timestamp int64   `json:"timestamp"`
	HDOP      float64 `json:"hdop"`
}

// Age returns how long ago the fix was recorded.
func (f *Fix) Age() time.Duration {
	return time.Since(time.Unix(f.Timestamp, 0))
}

// Load reads the cache file. Returns nil, nil if file doesn't exist.
func Load(path string) (*Fix, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f Fix
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Save writes the fix to the cache file atomically (write tmp → rename).
func Save(path string, f *Fix) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
