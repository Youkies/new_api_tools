package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/new-api-tools/backend/internal/config"
)

const logMatchUploadKeyFile = "log_match_upload_key.json"

// LogMatchUploadKeyConfig stores the upload-only key used by external scripts.
type LogMatchUploadKeyConfig struct {
	Key       string `json:"key"`
	UpdatedAt int64  `json:"updated_at"`
}

// LogMatchUploadKeyDataDir returns the persistent data directory for upload-key storage.
func LogMatchUploadKeyDataDir() string {
	dataDir := strings.TrimSpace(config.Get().DataDir)
	if dataDir == "" {
		return "./data"
	}
	return dataDir
}

func logMatchUploadKeyPath() string {
	return filepath.Join(LogMatchUploadKeyDataDir(), logMatchUploadKeyFile)
}

// GenerateLogMatchUploadKey creates a new upload-only key.
func GenerateLogMatchUploadKey() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "lmu_" + hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return "lmu_" + hex.EncodeToString(buf)
}

// GetLogMatchUploadKeyConfig loads the persisted upload-only key.
func GetLogMatchUploadKeyConfig() (LogMatchUploadKeyConfig, bool, error) {
	var cfg LogMatchUploadKeyConfig
	data, err := os.ReadFile(logMatchUploadKeyPath())
	if os.IsNotExist(err) {
		return cfg, false, nil
	}
	if err != nil {
		return cfg, false, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, false, err
	}
	cfg.Key = strings.TrimSpace(cfg.Key)
	return cfg, cfg.Key != "", nil
}

// SaveLogMatchUploadKey persists the upload-only key in DATA_DIR.
func SaveLogMatchUploadKey(key string) (LogMatchUploadKeyConfig, error) {
	cfg := LogMatchUploadKeyConfig{
		Key:       strings.TrimSpace(key),
		UpdatedAt: time.Now().Unix(),
	}
	if cfg.Key == "" {
		return cfg, ClearLogMatchUploadKey()
	}
	if err := os.MkdirAll(LogMatchUploadKeyDataDir(), 0o700); err != nil {
		return cfg, err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return cfg, err
	}
	return cfg, os.WriteFile(logMatchUploadKeyPath(), data, 0o600)
}

// ClearLogMatchUploadKey removes the upload-only key.
func ClearLogMatchUploadKey() error {
	err := os.Remove(logMatchUploadKeyPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// VerifyLogMatchUploadKey checks whether the provided key matches the upload-only key.
func VerifyLogMatchUploadKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	cfg, found, err := GetLogMatchUploadKeyConfig()
	if err != nil || !found {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(key), []byte(cfg.Key)) == 1
}
