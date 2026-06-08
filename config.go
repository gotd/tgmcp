package main

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-faster/errors"
	"github.com/joho/godotenv"
)

// Config holds the credentials and paths required to run the server.
type Config struct {
	AppID      int
	AppHash    string
	Phone      string
	SessionDir string
	HTTPAddr   string
}

// LoadConfig reads configuration from the environment, optionally sourcing a
// ".env" file in the working directory first.
//
// Required variables:
//
//	APP_ID, APP_HASH - obtained from https://my.telegram.org/apps
//
// Optional:
//
//	TG_PHONE         - phone number in international format (used only to name the session folder)
//	TG_SESSION_DIR   - directory to store the session (default: "./session")
//	MCP_ADDR         - address for the MCP HTTP server to listen on (default: "127.0.0.1:8080")
func LoadConfig() (Config, error) {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return Config{}, errors.Wrap(err, "load .env")
	}

	var cfg Config

	appID, err := strconv.Atoi(os.Getenv("APP_ID"))
	if err != nil {
		return Config{}, errors.Wrap(err, "parse APP_ID")
	}
	cfg.AppID = appID

	cfg.AppHash = os.Getenv("APP_HASH")
	if cfg.AppHash == "" {
		return Config{}, errors.New("APP_HASH is not set")
	}

	cfg.Phone = os.Getenv("TG_PHONE")

	cfg.SessionDir = os.Getenv("TG_SESSION_DIR")
	if cfg.SessionDir == "" {
		cfg.SessionDir = "session"
	}
	cfg.SessionDir = filepath.Join(cfg.SessionDir, sessionFolder(cfg.Phone))

	cfg.HTTPAddr = os.Getenv("MCP_ADDR")
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = "127.0.0.1:8080"
	}

	return cfg, nil
}

// sessionFolder derives a stable subdirectory name from a phone number.
// When phone is empty, "default" is returned.
func sessionFolder(phone string) string {
	if phone == "" {
		return "default"
	}

	var out []rune
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			out = append(out, r)
		}
	}

	return "phone-" + string(out)
}
