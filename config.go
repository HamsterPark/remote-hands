package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// config is remembered between runs, so that the two connection lines stay
// the same and the AI computers do not have to be set up again.
type config struct {
	Token string `json:"token"`
	Port  int    `json:"port"`
	Name  string `json:"name,omitempty"`
}

func configPath() (string, error) {
	if p := os.Getenv("REMOTE_HANDS_CONFIG"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "remote-hands", "config.json"), nil
}

// loadConfig never fails: a missing or damaged file gives an empty config.
func loadConfig(path string) *config {
	var c config
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return &c
}

func saveConfig(path string, c *config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// newToken returns 128 random bits as 32 lower-case hex digits.
func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func validToken(t string) bool {
	if len(t) != 32 {
		return false
	}
	_, err := hex.DecodeString(t)
	return err == nil
}
