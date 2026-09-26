package main

import (
	"os"
	"strings"

	"github.com/go-faster/errors"

	"github.com/go-faster/gooners/mcpauth"
	"github.com/go-faster/gooners/mcpcmd"
)

// defaultAuthHeader carries the credential when none is named. Bearer in the
// Authorization header is what an MCP client sends without being taught
// anything server-specific.
const defaultAuthHeader = "Authorization"

// loadServeConfig reads how the MCP endpoint is exposed and who may reach it.
//
// Every part is off unless set: tgmcp's default posture is an unauthenticated
// server on loopback, which is safe only because it is on loopback.
func loadServeConfig(cfg *Config) error {
	cfg.Transport = mcpcmd.TransportFlags{
		Transport:                  "streamable-http",
		Addr:                       cfg.HTTPAddr,
		TLSCertFile:                os.Getenv("MCP_TLS_CERT_FILE"),
		TLSKeyFile:                 os.Getenv("MCP_TLS_KEY_FILE"),
		TLSClientCAFile:            os.Getenv("MCP_TLS_CLIENT_CA_FILE"),
		ExposeProvider:             os.Getenv("MCP_EXPOSE_PROVIDER"),
		ExposeType:                 os.Getenv("MCP_EXPOSE_TYPE"),
		ExposeConfig:               os.Getenv("MCP_EXPOSE_CONFIG"),
		ExposeName:                 os.Getenv("MCP_EXPOSE_NAME"),
		DisableLocalhostProtection: os.Getenv("MCP_DISABLE_LOCALHOST_PROTECTION") == "true",
	}
	cfg.Auth = authConfig(os.Getenv("MCP_AUTH_TOKEN"), os.Getenv("MCP_AUTH_HEADER"))

	if err := cfg.Auth.Validate(); err != nil {
		return err
	}

	// A tunnel makes the listener reachable from the internet, and the loopback
	// bind that was doing the access control stops meaning anything. Refusing
	// is the only honest response: the alternative is publishing an
	// unauthenticated Telegram account and logging a warning about it.
	if cfg.Transport.ExposeProvider != "" && !cfg.Auth.Enabled {
		return errors.New("MCP_EXPOSE_PROVIDER publishes this server: set MCP_AUTH_TOKEN")
	}

	return nil
}

// authConfig builds the credential check from the token and header names.
//
// The token is what the operator generated, not a header value, so the Bearer
// scheme is added for the Authorization header. A custom header carries the
// token as-is: naming one is how a caller says it wants a bare value.
func authConfig(token, header string) mcpauth.Config {
	if token == "" {
		return mcpauth.Config{}
	}
	if header == "" {
		header = defaultAuthHeader
	}

	value := token
	if strings.EqualFold(header, defaultAuthHeader) {
		value = "Bearer " + token
	}

	return mcpauth.Config{
		Enabled: true,
		Header:  header,
		Value:   value,
	}
}
