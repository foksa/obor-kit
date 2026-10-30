package config

import (
	"encoding/json"
	"path/filepath"
)

// Fallback is the config of root with every default and nothing from its files: what a hook guards with when
// .obor/config.json exists but does not load.
func Fallback(root string) *Config {
	c := &Config{Root: root, Data: Defaults()}
	c.Data.derive(filepath.Base(root))
	c.json, _ = json.Marshal(c.Data)
	return c
}
