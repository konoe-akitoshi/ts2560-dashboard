package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config は設定ファイル（JSON）と環境変数から読む。環境変数があればそちらを優先する。
type Config struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`

	Bind string `json:"bind"`
	Port int    `json:"port"`

	Interval     int      `json:"interval"`      // 状態画面を読む間隔（秒）
	SlowInterval int      `json:"slow_interval"` // 機器情報・クライアント・ログを読む間隔（秒）
	ReferenceNTP []string `json:"reference_ntp"` // 時刻を比べる公開NTPサーバー

	MaxStratum        int     `json:"max_stratum"`
	MaxRefDiffMs      float64 `json:"max_ref_diff_ms"`
	JammingRise       int     `json:"jamming_rise"`
	HistoryPoints     int     `json:"history_points"`
	GNSSHoldoverHours float64 `json:"gnss_holdover_hours"`

	CORSOrigin string `json:"cors_origin"` // API を別のオリジンから読ませるとき（例: "*"）
	LogFormat  string `json:"log_format"`  // "text"（既定）か "json"

	DataDir string `json:"-"`
	Demo    bool   `json:"-"`
}

func defaultConfig() Config {
	return Config{
		Bind:              "127.0.0.1",
		Port:              8560,
		Interval:          10,
		SlowInterval:      60,
		ReferenceNTP:      []string{"ntp.nict.jp", "time.google.com", "ntp.jst.mfeed.ad.jp"},
		MaxStratum:        1,
		MaxRefDiffMs:      5,
		JammingRise:       40,
		HistoryPoints:     720,
		GNSSHoldoverHours: 24,
		DataDir:           ".",
	}
}

// loadConfig は path（空なら TS2560_CONFIG、それもなければ ./config.json）を読み、環境変数で上書きする。
// ファイルがなくても、環境変数だけで動かせる。
func loadConfig(path string, demo bool) (Config, error) {
	cfg := defaultConfig()
	cfg.Demo = demo
	if path == "" {
		path = env("TS2560_CONFIG", "config.json")
	}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return cfg, fmt.Errorf("%s を読めません: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}

	cfg.Host = env("TS2560_HOST", cfg.Host)
	cfg.Username = env("TS2560_USERNAME", cfg.Username)
	cfg.Password = env("TS2560_PASSWORD", cfg.Password)
	cfg.Bind = env("TS2560_BIND", cfg.Bind)
	cfg.DataDir = env("TS2560_DATA", cfg.DataDir)
	cfg.CORSOrigin = env("TS2560_CORS_ORIGIN", cfg.CORSOrigin)
	cfg.LogFormat = env("TS2560_LOG_FORMAT", cfg.LogFormat)
	if v := os.Getenv("TS2560_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Port = n
		}
	}
	if v := os.Getenv("TS2560_REFERENCE_NTP"); v != "" {
		cfg.ReferenceNTP = nil
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				cfg.ReferenceNTP = append(cfg.ReferenceNTP, h)
			}
		}
	}
	if os.Getenv("TS2560_DEMO") == "1" {
		cfg.Demo = true
	}

	if cfg.Demo {
		cfg.Host = "demo"
		cfg.Interval = 3
	} else if cfg.Host == "" {
		return cfg, errors.New("機器のアドレスがありません。config.json の host か、環境変数 TS2560_HOST を指定してください")
	}
	if cfg.Interval < 1 {
		cfg.Interval = 1
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
