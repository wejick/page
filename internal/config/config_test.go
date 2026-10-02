package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
		check   func(t *testing.T, c Config)
	}{
		{
			name:    "missing SQLITE_PATH",
			env:     map[string]string{"AUTH_TOKEN": "t"},
			wantErr: true,
		},
		{
			name:    "missing AUTH_TOKEN",
			env:     map[string]string{"SQLITE_PATH": "page.db"},
			wantErr: true,
		},
		{
			name:    "unknown driver",
			env:     map[string]string{"SQLITE_PATH": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "disk"},
			wantErr: true,
		},
		{
			name:    "s3compat requires endpoint and creds",
			env:     map[string]string{"SQLITE_PATH": "p", "AUTH_TOKEN": "t"},
			wantErr: true,
		},
		{
			name: "mem driver needs no S3 vars",
			env: map[string]string{
				"SQLITE_PATH": "page.db", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem",
			},
			check: func(t *testing.T, c Config) {
				if c.Storage.Driver != "mem" {
					t.Fatalf("driver = %q, want mem", c.Storage.Driver)
				}
				if c.Addr != ":8080" {
					t.Fatalf("addr default = %q", c.Addr)
				}
			},
		},
		{
			name: "s3compat full",
			env: map[string]string{
				"SQLITE_PATH": "page.db", "AUTH_TOKEN": "t",
				"S3_ENDPOINT": "localhost:9000", "S3_ACCESS_KEY": "k", "S3_SECRET_KEY": "s",
			},
			check: func(t *testing.T, c Config) {
				if c.Storage.Endpoint != "localhost:9000" || !c.Storage.PathStyle || c.Storage.Secure {
					t.Fatalf("storage = %+v", c.Storage)
				}
				if c.Storage.Bucket != "pages" {
					t.Fatalf("bucket default = %q", c.Storage.Bucket)
				}
				if c.Caps.MaxRawBytes != 25*MiB || c.Caps.MaxFiles != 2000 {
					t.Fatalf("caps defaults = %+v", c.Caps)
				}
				if c.Caps.FetchTimeout != 10*time.Second || c.Caps.FetchConcurrency != 8 {
					t.Fatalf("fetch caps = %+v", c.Caps)
				}
				if len(c.KeepExternal.Fonts) == 0 || c.KeepExternal.Fonts[0] != "fonts.googleapis.com" {
					t.Fatalf("fonts allowlist default = %v", c.KeepExternal.Fonts)
				}
			},
		},
		{
			name: "overrides and allowlist trimming",
			env: map[string]string{
				"SQLITE_PATH": "page.db", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem",
				"ADDR": ":9090", "UPLOAD_MAX_FILES": "5",
				"KEEP_EXTERNAL_JS": " Cdn.JsDelivr.net , unpkg.com ",
			},
			check: func(t *testing.T, c Config) {
				if c.Addr != ":9090" || c.Caps.MaxFiles != 5 {
					t.Fatalf("overrides not applied: %+v", c)
				}
				if c.KeepExternal.JS[0] != "cdn.jsdelivr.net" || len(c.KeepExternal.JS) != 2 {
					t.Fatalf("js allowlist = %v", c.KeepExternal.JS)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(func(key string) string { return tt.env[key] })
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() err = %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

// TestLoadObservability covers the LOG_LEVEL and OTEL_* knobs
// (observability D1, D5): info is the default, level names are validated
// eagerly, and only the http/protobuf OTLP protocol is accepted.
func TestLoadObservability(t *testing.T) {
	base := map[string]string{"SQLITE_PATH": "p", "STORAGE_DRIVER": "mem", "AUTH_TOKEN": "t"}
	tests := []struct {
		name    string
		extra   map[string]string
		wantErr bool
		check   func(t *testing.T, c Config)
	}{
		{
			name: "defaults",
			check: func(t *testing.T, c Config) {
				if c.LogLevel != slog.LevelInfo {
					t.Fatalf("log level = %v, want info", c.LogLevel)
				}
				if c.OTelEndpoint != "" || c.OTelProtocol != "" {
					t.Fatalf("otel = %q/%q, want unset", c.OTelEndpoint, c.OTelProtocol)
				}
			},
		},
		{
			name:  "valid level",
			extra: map[string]string{"LOG_LEVEL": "debug"},
			check: func(t *testing.T, c Config) {
				if c.LogLevel != slog.LevelDebug {
					t.Fatalf("log level = %v, want debug", c.LogLevel)
				}
			},
		},
		{
			name:    "invalid level",
			extra:   map[string]string{"LOG_LEVEL": "loud"},
			wantErr: true,
		},
		{
			name:  "otel endpoint set",
			extra: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318"},
			check: func(t *testing.T, c Config) {
				if c.OTelEndpoint != "http://collector:4318" {
					t.Fatalf("endpoint = %q", c.OTelEndpoint)
				}
			},
		},
		{
			name: "http/protobuf protocol accepted",
			extra: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
				"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
			},
			check: func(t *testing.T, c Config) {},
		},
		{
			name: "grpc protocol rejected",
			extra: map[string]string{
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
				"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range base {
				env[k] = v
			}
			for k, v := range tt.extra {
				env[k] = v
			}
			cfg, err := Load(func(key string) string { return env[key] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

// TestLoadAuthMode covers the AUTH_MODE validation matrix (auth-modes D1):
// token is the fail-closed default, none strictly forbids a token, oidc
// requires its IdP wiring, and serve mode requires none of it.
func TestLoadAuthMode(t *testing.T) {
	adminEnv := func(extra map[string]string) map[string]string {
		env := map[string]string{"SQLITE_PATH": "p", "STORAGE_DRIVER": "mem"}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	oidcEnv := func(extra map[string]string) map[string]string {
		env := adminEnv(map[string]string{
			"AUTH_MODE":          "oidc",
			"OIDC_ISSUER":        "https://idp.example.com",
			"OIDC_CLIENT_ID":     "page",
			"OIDC_CLIENT_SECRET": "s3cret",
			"OIDC_REDIRECT_URL":  "https://pages.example.com/auth/callback",
			"SESSION_SECRET":     "0123456789abcdef",
		})
		for k, v := range extra {
			if v == "" {
				delete(env, k)
			} else {
				env[k] = v
			}
		}
		return env
	}

	tests := []struct {
		name        string
		env         map[string]string
		wantErr     bool
		errContains []string
		check       func(t *testing.T, c Config)
	}{
		{
			name:        "default token mode requires AUTH_TOKEN",
			env:         adminEnv(nil),
			wantErr:     true,
			errContains: []string{"AUTH_TOKEN", "AUTH_MODE=token"},
		},
		{
			name: "explicit token mode with token boots",
			env:  adminEnv(map[string]string{"AUTH_MODE": "token", "AUTH_TOKEN": "t"}),
			check: func(t *testing.T, c Config) {
				if c.AuthMode != AuthModeToken {
					t.Fatalf("auth mode = %q", c.AuthMode)
				}
			},
		},
		{
			name:        "none mode rejects a configured token",
			env:         adminEnv(map[string]string{"AUTH_MODE": "none", "AUTH_TOKEN": "t"}),
			wantErr:     true,
			errContains: []string{"AUTH_TOKEN", "none"},
		},
		{
			name: "none mode boots without any credentials",
			env:  adminEnv(map[string]string{"AUTH_MODE": "none"}),
			check: func(t *testing.T, c Config) {
				if c.AuthMode != AuthModeNone || c.AuthToken != "" {
					t.Fatalf("config = mode %q token %q", c.AuthMode, c.AuthToken)
				}
			},
		},
		{
			name:        "oidc mode missing every variable names them all",
			env:         adminEnv(map[string]string{"AUTH_MODE": "oidc"}),
			wantErr:     true,
			errContains: []string{"OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_REDIRECT_URL", "SESSION_SECRET"},
		},
		{
			name:        "oidc mode missing only the session secret",
			env:         oidcEnv(map[string]string{"SESSION_SECRET": ""}),
			wantErr:     true,
			errContains: []string{"SESSION_SECRET"},
		},
		{
			name: "oidc mode boots with an optional machine token",
			env:  oidcEnv(map[string]string{"AUTH_TOKEN": "machine"}),
			check: func(t *testing.T, c Config) {
				if c.OIDC.Issuer != "https://idp.example.com" || c.SessionSecret == "" || c.AuthToken != "machine" {
					t.Fatalf("oidc config = %+v token %q", c.OIDC, c.AuthToken)
				}
			},
		},
		{
			name:        "invalid auth mode names the valid values",
			env:         adminEnv(map[string]string{"AUTH_MODE": "kerberos", "AUTH_TOKEN": "t"}),
			wantErr:     true,
			errContains: []string{"AUTH_MODE", "token", "none", "oidc"},
		},
		{
			name: "serve mode ignores auth requirements entirely",
			env: map[string]string{
				"SERVER_MODE": "serve", "STORAGE_DRIVER": "mem", "AUTH_MODE": "oidc",
			},
			check: func(t *testing.T, c Config) {
				if c.Mode != ModeServe || c.AuthMode != AuthModeOIDC {
					t.Fatalf("mode = %q auth = %q", c.Mode, c.AuthMode)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(func(key string) string { return tt.env[key] })
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() err = nil, want error")
				}
				for _, s := range tt.errContains {
					if !strings.Contains(err.Error(), s) {
						t.Fatalf("Load() err = %q, want it to mention %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() err = %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestLoadMode(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantMode    Mode
		wantErr     bool
		errContains []string // substrings the error must mention
		errOmits    []string // substrings the error must not mention
	}{
		{
			name:     "SERVER_MODE unset defaults to all",
			env:      map[string]string{"SQLITE_PATH": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem"},
			wantMode: ModeAll,
		},
		{
			name:     "explicit all",
			env:      map[string]string{"SERVER_MODE": "all", "SQLITE_PATH": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem"},
			wantMode: ModeAll,
		},
		{
			name:     "admin with full config",
			env:      map[string]string{"SERVER_MODE": "admin", "SQLITE_PATH": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem"},
			wantMode: ModeAdmin,
		},
		{
			name:     "serve with mem storage only, no database or token",
			env:      map[string]string{"SERVER_MODE": "serve", "STORAGE_DRIVER": "mem"},
			wantMode: ModeServe,
		},
		{
			name: "serve with s3compat storage only, no database or token",
			env: map[string]string{
				"SERVER_MODE": "serve",
				"S3_ENDPOINT": "localhost:9000", "S3_ACCESS_KEY": "k", "S3_SECRET_KEY": "s",
			},
			wantMode: ModeServe,
		},
		{
			name:        "serve still requires storage settings",
			env:         map[string]string{"SERVER_MODE": "serve"},
			wantErr:     true,
			errContains: []string{"S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY"},
			errOmits:    []string{"SQLITE_PATH", "AUTH_TOKEN"},
		},
		{
			name:        "admin requires SQLITE_PATH",
			env:         map[string]string{"SERVER_MODE": "admin", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem"},
			wantErr:     true,
			errContains: []string{"SQLITE_PATH"},
		},
		{
			name:        "admin requires AUTH_TOKEN",
			env:         map[string]string{"SERVER_MODE": "admin", "SQLITE_PATH": "p", "STORAGE_DRIVER": "mem"},
			wantErr:     true,
			errContains: []string{"AUTH_TOKEN"},
		},
		{
			name:        "all requires SQLITE_PATH",
			env:         map[string]string{"SERVER_MODE": "all", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem"},
			wantErr:     true,
			errContains: []string{"SQLITE_PATH"},
		},
		{
			name:        "all requires AUTH_TOKEN",
			env:         map[string]string{"SERVER_MODE": "all", "SQLITE_PATH": "p", "STORAGE_DRIVER": "mem"},
			wantErr:     true,
			errContains: []string{"AUTH_TOKEN"},
		},
		{
			name: "unknown mode fails naming the valid modes",
			env: map[string]string{
				"SERVER_MODE": "both", "SQLITE_PATH": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem",
			},
			wantErr:     true,
			errContains: []string{"SERVER_MODE", "serve", "admin", "all"},
		},
		{
			name: "mode value is case-sensitive",
			env: map[string]string{
				"SERVER_MODE": "Serve", "SQLITE_PATH": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem",
			},
			wantErr:     true,
			errContains: []string{"SERVER_MODE"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(func(key string) string { return tt.env[key] })
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() err = nil, want error")
				}
				for _, s := range tt.errContains {
					if !strings.Contains(err.Error(), s) {
						t.Fatalf("Load() err = %q, want it to mention %q", err, s)
					}
				}
				for _, s := range tt.errOmits {
					if strings.Contains(err.Error(), s) {
						t.Fatalf("Load() err = %q, want it to not mention %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() err = %v", err)
			}
			if cfg.Mode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", cfg.Mode, tt.wantMode)
			}
		})
	}
}
