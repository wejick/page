// Package config loads service configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	MiB = 1 << 20
)

// Mode selects which planes an instance serves: ModeServe (page serving
// only), ModeAdmin (upload UI + admin API only), or ModeAll (everything).
type Mode string

const (
	ModeServe Mode = "serve"
	ModeAdmin Mode = "admin"
	ModeAll   Mode = "all"
)

// Config is the fully-parsed service configuration.
type Config struct {
	Addr        string
	Mode        Mode // which planes this instance serves
	DatabaseURL string
	AuthToken   string
	CacheTTL    time.Duration // entry-HTML cache revalidation interval

	Storage      Storage
	Caps         Caps
	KeepExternal KeepExternal
}

// Storage configures the storage driver. Driver is "s3compat" or "mem".
type Storage struct {
	Driver    string
	Endpoint  string
	Secure    bool
	PathStyle bool
	Bucket    string
	AccessKey string
	SecretKey string
}

// Caps bounds ingest work so uploads stay synchronous and finite.
type Caps struct {
	MaxRawBytes          int64
	MaxDecompressedBytes int64
	MaxFiles             int
	MaxAssetBytes        int64
	FetchTimeout         time.Duration
	FetchBudget          time.Duration
	FetchConcurrency     int
}

// KeepExternal is the categorized keep-external allowlist (D3).
type KeepExternal struct {
	Fonts []string
	JS    []string
	Icons []string
	Misc  []string
}

// Load reads configuration from the environment. get is injectable for tests.
func Load(get func(string) string) (Config, error) {
	var cfg Config
	var errs []error

	cfg.Addr = str(get, "ADDR", ":8080")
	cfg.Mode = ModeAll
	if v := get("SERVER_MODE"); v != "" {
		switch Mode(v) {
		case ModeServe, ModeAdmin, ModeAll:
			cfg.Mode = Mode(v)
		default:
			errs = append(errs, fmt.Errorf("SERVER_MODE must be serve, admin, or all, got %q", v))
		}
	}
	cfg.DatabaseURL = get("DATABASE_URL")
	cfg.AuthToken = get("AUTH_TOKEN")
	cfg.CacheTTL = dur(get, "HTML_CACHE_TTL", 60*time.Second)
	if cfg.Mode != ModeServe {
		if cfg.DatabaseURL == "" {
			errs = append(errs, fmt.Errorf("DATABASE_URL is required"))
		}
		if cfg.AuthToken == "" {
			errs = append(errs, fmt.Errorf("AUTH_TOKEN is required"))
		}
	}

	cfg.Storage.Driver = str(get, "STORAGE_DRIVER", "s3compat")
	cfg.Storage.Bucket = str(get, "S3_BUCKET", "pages")
	cfg.Storage.Endpoint = get("S3_ENDPOINT")
	cfg.Storage.Secure = boolean(get, "S3_SECURE", false)
	cfg.Storage.PathStyle = boolean(get, "S3_PATH_STYLE", true)
	cfg.Storage.AccessKey = get("S3_ACCESS_KEY")
	cfg.Storage.SecretKey = get("S3_SECRET_KEY")

	switch cfg.Storage.Driver {
	case "s3compat":
		for k, v := range map[string]string{
			"S3_ENDPOINT":   cfg.Storage.Endpoint,
			"S3_ACCESS_KEY": cfg.Storage.AccessKey,
			"S3_SECRET_KEY": cfg.Storage.SecretKey,
		} {
			if v == "" {
				errs = append(errs, fmt.Errorf("%s is required when STORAGE_DRIVER=s3compat", k))
			}
		}
	case "mem":
	default:
		errs = append(errs, fmt.Errorf("STORAGE_DRIVER must be s3compat or mem, got %q", cfg.Storage.Driver))
	}

	cfg.Caps = Caps{
		MaxRawBytes:          int64(num(get, "UPLOAD_MAX_RAW_BYTES", 25*MiB)),
		MaxDecompressedBytes: int64(num(get, "UPLOAD_MAX_DECOMPRESSED_BYTES", 100*MiB)),
		MaxFiles:             num(get, "UPLOAD_MAX_FILES", 2000),
		MaxAssetBytes:        int64(num(get, "ASSET_MAX_BYTES", 10*MiB)),
		FetchTimeout:         dur(get, "FETCH_TIMEOUT", 10*time.Second),
		FetchBudget:          dur(get, "FETCH_BUDGET", 60*time.Second),
		FetchConcurrency:     num(get, "FETCH_CONCURRENCY", 8),
	}

	cfg.KeepExternal = KeepExternal{
		Fonts: list(get, "KEEP_EXTERNAL_FONTS",
			"fonts.googleapis.com,fonts.gstatic.com,use.typekit.net"),
		JS: list(get, "KEEP_EXTERNAL_JS",
			"cdn.jsdelivr.net,unpkg.com,cdnjs.cloudflare.com,esm.sh"),
		Icons: list(get, "KEEP_EXTERNAL_ICONS",
			"use.fontawesome.com"),
		Misc: list(get, "KEEP_EXTERNAL_MISC",
			"www.googletagmanager.com,plausible.io"),
	}

	if len(errs) > 0 {
		return cfg, fmt.Errorf("invalid config: %w", join(errs))
	}
	return cfg, nil
}

// Load reads configuration from the real environment.
func LoadEnv() (Config, error) { return Load(os.Getenv) }

func str(get func(string) string, key, def string) string {
	if v := get(key); v != "" {
		return v
	}
	return def
}

func boolean(get func(string) string, key string, def bool) bool {
	v := get(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func num(get func(string) string, key string, def int) int {
	v := get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func dur(get func(string) string, key string, def time.Duration) time.Duration {
	v := get(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func list(get func(string) string, key, def string) []string {
	v := get(key)
	if v == "" {
		v = def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func join(errs []error) error {
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}
