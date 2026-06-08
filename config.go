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
}

// LoadConfig reads configuration from the environment, optionally sourcing a
// ".env" file in the working directory first.
//
// Required variables:
//
//	APP_ID, APP_HASH - obtained from https://my.telegram.org/apps
//	TG_PHONE         - phone number in international format, e.g. +4123456789
//
// Optional:
//
//	TG_SESSION_DIR   - directory to store the session (default: "./session")
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
	if cfg.Phone == "" {
		return Config{}, errors.New("TG_PHONE is not set")
	}

	cfg.SessionDir = os.Getenv("TG_SESSION_DIR")
	if cfg.SessionDir == "" {
		cfg.SessionDir = "session"
	}
	cfg.SessionDir = filepath.Join(cfg.SessionDir, sessionFolder(cfg.Phone))

	return cfg, nil
}

// sessionFolder derives a stable directory name from a phone number, keeping
// only its digits.
func sessionFolder(phone string) string {
	var out []rune
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			out = append(out, r)
		}
	}
	return "phone-" + string(out)
}
