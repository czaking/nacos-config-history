package main

import (
	"encoding/json"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration, sourced from environment variables
// with sensible defaults for local development.
type Config struct {
	// Aliyun access
	AccessKeyID     string
	AccessKeySecret string
	SecurityToken   string // set when using temporary STS/OAuth credentials
	Endpoint        string // MSE OpenAPI endpoint, e.g. mse.us-west-1.aliyuncs.com
	RegionID        string
	InstanceID      string // MSE Nacos instance id

	// Storage
	DBDriver string // "sqlite" (local) or "pgx" (prod Postgres)
	DBDSN    string // sqlite file path or postgres DSN

	// Poller
	PollInterval     time.Duration
	UserSyncInterval time.Duration
	SyncOnce         bool // run one sync then exit (for testing)
	ServeOnly        bool // serve API+SPA without starting the background poller

	// HTTP
	Addr string
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loadAliyunCredsFromCLI reads credentials from ~/.aliyun/config.json (the
// aliyun CLI profile) as a fallback when env vars are not set. It picks the
// "current" profile and returns its access key, secret and (for STS/OAuth
// profiles) the security token. Convenient for local dev.
func loadAliyunCredsFromCLI() (ak, sk, token string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", ""
	}
	b, err := os.ReadFile(home + "/.aliyun/config.json")
	if err != nil {
		return "", "", ""
	}
	var cfg struct {
		Current  string `json:"current"`
		Profiles []struct {
			Name            string `json:"name"`
			AccessKeyID     string `json:"access_key_id"`
			AccessKeySecret string `json:"access_key_secret"`
			StsToken        string `json:"sts_token"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", "", ""
	}
	for _, p := range cfg.Profiles {
		if p.Name == cfg.Current && p.AccessKeyID != "" {
			return p.AccessKeyID, p.AccessKeySecret, p.StsToken
		}
	}
	// fall back to the first profile that carries a usable key
	for _, p := range cfg.Profiles {
		if p.AccessKeyID != "" {
			return p.AccessKeyID, p.AccessKeySecret, p.StsToken
		}
	}
	return "", "", ""
}

func LoadConfig() *Config {
	ak := os.Getenv("ALIYUN_ACCESS_KEY_ID")
	sk := os.Getenv("ALIYUN_ACCESS_KEY_SECRET")
	token := os.Getenv("ALIYUN_SECURITY_TOKEN")
	if ak == "" || sk == "" {
		if cak, csk, ctok := loadAliyunCredsFromCLI(); cak != "" {
			ak, sk, token = cak, csk, ctok
		}
	}

	interval := 5 * time.Minute
	if v := os.Getenv("POLL_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = time.Duration(n) * time.Second
		}
	}

	userInterval := time.Hour
	if v := os.Getenv("USER_SYNC_INTERVAL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			userInterval = time.Duration(n) * time.Second
		}
	}

	region := getenv("ALIYUN_REGION", "us-west-1")
	return &Config{
		AccessKeyID:     ak,
		AccessKeySecret: sk,
		SecurityToken:   token,
		Endpoint:        getenv("MSE_ENDPOINT", "mse."+region+".aliyuncs.com"),
		RegionID:        region,
		InstanceID:      getenv("MSE_INSTANCE_ID", "mse-your-instance-id"),
		DBDriver:        getenv("DB_DRIVER", "sqlite"),
		DBDSN:           getenv("DB_DSN", "./nacoshist.db"),
		PollInterval:     interval,
		UserSyncInterval: userInterval,
		SyncOnce:         os.Getenv("SYNC_ONCE") == "1",
		ServeOnly:        os.Getenv("SERVE_ONLY") == "1",
		Addr:            getenv("ADDR", ":8080"),
	}
}
