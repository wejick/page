package config

import (
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
			name:    "missing DATABASE_URL",
			env:     map[string]string{"AUTH_TOKEN": "t"},
			wantErr: true,
		},
		{
			name:    "missing AUTH_TOKEN",
			env:     map[string]string{"DATABASE_URL": "postgres://x"},
			wantErr: true,
		},
		{
			name:    "unknown driver",
			env:     map[string]string{"DATABASE_URL": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "disk"},
			wantErr: true,
		},
		{
			name:    "s3compat requires endpoint and creds",
			env:     map[string]string{"DATABASE_URL": "p", "AUTH_TOKEN": "t"},
			wantErr: true,
		},
		{
			name: "mem driver needs no S3 vars",
			env: map[string]string{
				"DATABASE_URL": "postgres://x", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem",
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
				"DATABASE_URL": "postgres://x", "AUTH_TOKEN": "t",
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
				"DATABASE_URL": "postgres://x", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem",
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
			env:      map[string]string{"DATABASE_URL": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem"},
			wantMode: ModeAll,
		},
		{
			name:     "explicit all",
			env:      map[string]string{"SERVER_MODE": "all", "DATABASE_URL": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem"},
			wantMode: ModeAll,
		},
		{
			name:     "admin with full config",
			env:      map[string]string{"SERVER_MODE": "admin", "DATABASE_URL": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem"},
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
			errOmits:    []string{"DATABASE_URL", "AUTH_TOKEN"},
		},
		{
			name:        "admin requires DATABASE_URL",
			env:         map[string]string{"SERVER_MODE": "admin", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem"},
			wantErr:     true,
			errContains: []string{"DATABASE_URL"},
		},
		{
			name:        "admin requires AUTH_TOKEN",
			env:         map[string]string{"SERVER_MODE": "admin", "DATABASE_URL": "p", "STORAGE_DRIVER": "mem"},
			wantErr:     true,
			errContains: []string{"AUTH_TOKEN"},
		},
		{
			name:        "all requires DATABASE_URL",
			env:         map[string]string{"SERVER_MODE": "all", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem"},
			wantErr:     true,
			errContains: []string{"DATABASE_URL"},
		},
		{
			name:        "all requires AUTH_TOKEN",
			env:         map[string]string{"SERVER_MODE": "all", "DATABASE_URL": "p", "STORAGE_DRIVER": "mem"},
			wantErr:     true,
			errContains: []string{"AUTH_TOKEN"},
		},
		{
			name: "unknown mode fails naming the valid modes",
			env: map[string]string{
				"SERVER_MODE": "both", "DATABASE_URL": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem",
			},
			wantErr:     true,
			errContains: []string{"SERVER_MODE", "serve", "admin", "all"},
		},
		{
			name: "mode value is case-sensitive",
			env: map[string]string{
				"SERVER_MODE": "Serve", "DATABASE_URL": "p", "AUTH_TOKEN": "t", "STORAGE_DRIVER": "mem",
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
