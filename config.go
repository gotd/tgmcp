package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
	"github.com/go-faster/gooners/mcpauth"
	"github.com/go-faster/gooners/mcpcmd"
	"github.com/joho/godotenv"
)

// Config holds the credentials and paths required to run the server.
type Config struct {
	AppID            int
	AppHash          string
	Phone            string
	SessionDir       string
	HTTPAddr         string
	LogLevel         string
	FileRoot         string
	AllowSend        bool
	AllowProfileEdit bool
	AllowInlineMedia bool

	Attribution     attributionMode
	Strict          bool
	AgentFooter     string
	BotToken        string
	BotUsername     string
	BotAllowedUsers []int64

	// LogPayloads includes MCP request and response bodies in the debug log,
	// and therefore in any log exporter. That is the contents of chats.
	LogPayloads bool

	BlobBaseURL string
	BlobDir     string
	BlobTTL     time.Duration

	// S3-backed blob storage, which replaces the local HTTP store when
	// endpoint and bucket are both set.
	BlobS3Endpoint string
	BlobS3Bucket   string
	BlobS3Prefix   string
	BlobS3Region   string

	// Transport is how the MCP endpoint is served: the listener, TLS, and the
	// optional tunnel that publishes it. See [loadServeConfig].
	Transport mcpcmd.TransportFlags
	// Auth guards that endpoint. Disabled by default, which is only safe while
	// Transport keeps it on loopback.
	Auth mcpauth.Config
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
//	LOG_LEVEL        - log level: debug, info, warn, error (default: "info")
//	TG_FILE_ROOT     - directory from which send_file may read files (default: disabled)
//
// Agent attribution, see [attributionMode]:
//
//	TG_ATTRIBUTION        - off (default), footer, or bot
//	TG_ATTRIBUTION_STRICT - "true" fails the send when bot attribution is unavailable
//	TG_AGENT_FOOTER       - footer text (default: "sent by an agent")
//	TG_BOT_TOKEN          - echo bot token, required by "tgmcp echobot"
//	TG_BOT_USERNAME       - echo bot @username; read from echobot.json when unset
//	TG_BOT_ALLOWED_USERS  - extra user IDs allowed to claim inline payloads
//
// File delivery, see [newBlobStore]:
//
//	TG_BLOB_BASE_URL - externally reachable URL the blob handler is served under;
//	                   unset disables it and get_file inline fails with a clear error
//	TG_BLOB_DIR      - where stored objects live (default: <session>/blob)
//	TG_BLOB_TTL      - how long a URL works (default: 15m)
//
// Serving the MCP endpoint, see [loadServeConfig]:
//
//	MCP_AUTH_TOKEN   - credential callers must present; unset serves the endpoint
//	                   to anyone who reaches it, which is safe only on loopback
//	MCP_AUTH_HEADER  - header carrying it (default: "Authorization", as "Bearer <token>")
//	MCP_TLS_CERT_FILE, MCP_TLS_KEY_FILE       - serve HTTPS instead of HTTP
//	MCP_TLS_CLIENT_CA_FILE                    - additionally require a client certificate
//	MCP_EXPOSE_PROVIDER                       - publish through a tunnel: "cloudflared"
//	MCP_EXPOSE_CONFIG, MCP_EXPOSE_NAME        - cloudflared config file and tunnel name
//	MCP_EXPOSE_TYPE                           - tunnel type (default: "http")
//	MCP_DISABLE_LOCALHOST_PROTECTION          - "true" when reached through a tunnel
//
// Telemetry is configured by the standard OTEL_* variables read by
// [github.com/go-faster/sdk/app], see README.
//
// All rights are opt-in: set the variable to "true" to grant one.
//
//	TG_ALLOW_SEND         - enable send_message, send_file, send_reaction, send_chat_action, send_screenshot_notification
//	TG_ALLOW_PROFILE_EDIT - enable update_profile, update_profile_photo
//	TG_ALLOW_INLINE_MEDIA - allow get_file to return the file in the tool result, or a URL when it is too large
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

	cfg.LogLevel = os.Getenv("LOG_LEVEL")
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}

	cfg.FileRoot = os.Getenv("TG_FILE_ROOT")

	cfg.AllowSend = os.Getenv("TG_ALLOW_SEND") == "true"
	cfg.AllowProfileEdit = os.Getenv("TG_ALLOW_PROFILE_EDIT") == "true"
	cfg.AllowInlineMedia = os.Getenv("TG_ALLOW_INLINE_MEDIA") == "true"

	mode, err := parseAttributionMode(os.Getenv("TG_ATTRIBUTION"))
	if err != nil {
		return Config{}, err
	}
	cfg.Attribution = mode
	cfg.Strict = os.Getenv("TG_ATTRIBUTION_STRICT") == "true"

	cfg.AgentFooter = os.Getenv("TG_AGENT_FOOTER")
	if cfg.AgentFooter == "" {
		cfg.AgentFooter = defaultAgentFooter
	}

	cfg.BotToken = os.Getenv("TG_BOT_TOKEN")
	cfg.BotUsername = strings.TrimPrefix(os.Getenv("TG_BOT_USERNAME"), "@")

	allowed, err := parseUserIDs(os.Getenv("TG_BOT_ALLOWED_USERS"))
	if err != nil {
		return Config{}, errors.Wrap(err, "parse TG_BOT_ALLOWED_USERS")
	}
	cfg.BotAllowedUsers = allowed

	cfg.BlobBaseURL = strings.TrimSuffix(os.Getenv("TG_BLOB_BASE_URL"), "/")

	cfg.BlobDir = os.Getenv("TG_BLOB_DIR")
	if cfg.BlobDir == "" {
		cfg.BlobDir = filepath.Join(cfg.SessionDir, "blob")
	}

	if v := os.Getenv("TG_BLOB_TTL"); v != "" {
		ttl, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, errors.Wrap(err, "parse TG_BLOB_TTL")
		}
		cfg.BlobTTL = ttl
	}

	cfg.LogPayloads = os.Getenv("TG_LOG_PAYLOADS") == "true"

	cfg.BlobS3Endpoint = os.Getenv("TG_BLOB_S3_ENDPOINT")
	cfg.BlobS3Bucket = os.Getenv("TG_BLOB_S3_BUCKET")
	cfg.BlobS3Prefix = os.Getenv("TG_BLOB_S3_PREFIX")
	cfg.BlobS3Region = os.Getenv("TG_BLOB_S3_REGION")

	// Half a bucket configuration is a mistake, not a fallback: silently
	// serving over local HTTP instead would hand out URLs nothing can reach.
	if (cfg.BlobS3Endpoint == "") != (cfg.BlobS3Bucket == "") {
		return Config{}, errors.New("TG_BLOB_S3_ENDPOINT and TG_BLOB_S3_BUCKET must be set together")
	}

	if cfg.Attribution == attributionBot && cfg.BotToken == "" && cfg.BotUsername == "" {
		return Config{}, errors.New("TG_ATTRIBUTION=bot requires TG_BOT_TOKEN or TG_BOT_USERNAME")
	}

	if err := loadServeConfig(&cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// parseUserIDs parses a comma-separated list of Telegram user IDs. Empty
// entries are skipped, so trailing commas and blank values are tolerated.
func parseUserIDs(s string) ([]int64, error) {
	var out []int64
	for field := range strings.SplitSeq(s, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}

		id, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, errors.Wrapf(err, "user id %q", field)
		}
		out = append(out, id)
	}

	return out, nil
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
