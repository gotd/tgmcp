package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthConfig(t *testing.T) {
	for _, tt := range []struct {
		name    string
		token   string
		header  string
		enabled bool
		wantHdr string
		wantVal string
	}{
		{
			name: "no token leaves auth off",
		},
		{
			// The operator sets a token, not a header value, so the scheme is
			// added for them.
			name:    "default header takes a bearer token",
			token:   "sekret",
			enabled: true,
			wantHdr: "Authorization",
			wantVal: "Bearer sekret",
		},
		{
			name:    "authorization is matched case-insensitively",
			token:   "sekret",
			header:  "authorization",
			enabled: true,
			wantHdr: "authorization",
			wantVal: "Bearer sekret",
		},
		{
			// Naming a header is how a caller asks for a bare value: prefixing
			// one would make X-Api-Key carry an OAuth scheme.
			name:    "custom header carries the token as-is",
			token:   "sekret",
			header:  "X-Api-Key",
			enabled: true,
			wantHdr: "X-Api-Key",
			wantVal: "sekret",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := authConfig(tt.token, tt.header)

			require.Equal(t, tt.enabled, cfg.Enabled)
			require.Equal(t, tt.wantHdr, cfg.Header)
			require.Equal(t, tt.wantVal, cfg.Value)
			require.NoError(t, cfg.Validate())
		})
	}
}

// A tunnel makes the listener public, so the loopback bind stops being the
// access control and the absence of a credential has to be fatal.
func TestLoadServeConfigTunnelNeedsAuth(t *testing.T) {
	t.Setenv("MCP_EXPOSE_PROVIDER", "cloudflared")

	cfg := Config{HTTPAddr: "127.0.0.1:8080"}
	require.ErrorContains(t, loadServeConfig(&cfg), "MCP_AUTH_TOKEN")

	t.Setenv("MCP_AUTH_TOKEN", "sekret")
	require.NoError(t, loadServeConfig(&cfg))
	require.True(t, cfg.Auth.Enabled)
	require.Equal(t, "cloudflared", cfg.Transport.ExposeProvider)
}

func TestLoadServeConfigDefaults(t *testing.T) {
	cfg := Config{HTTPAddr: "127.0.0.1:9000"}
	require.NoError(t, loadServeConfig(&cfg))

	require.False(t, cfg.Auth.Enabled, "auth is opt-in")
	require.Equal(t, "streamable-http", cfg.Transport.Transport)
	require.Equal(t, "127.0.0.1:9000", cfg.Transport.Addr)
	require.Empty(t, cfg.Transport.ExposeProvider)
}
