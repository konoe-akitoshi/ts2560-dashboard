// ts2560-dashboard は、セイコー TS-2560 タイムサーバーの状態を1画面で見るためのダッシュボード。
//
// 機器の Web 画面にログインして状態画面のテキストを読み、NTP(UDP 123) で公開NTPサーバーとの
// 時刻差も測る。結果は画面（web/index.html、実行ファイルに埋め込み）、JSON の API（/api/v1/...）、
// Server-Sent Events（/api/v1/events）、Prometheus のメトリクス（/metrics）で出す。
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/openapi.json
var openAPI []byte

func main() {
	demo := flag.Bool("demo", false, "実機なしのデモデータで起動する")
	config := flag.String("config", "", "設定ファイル（既定は環境変数 TS2560_CONFIG、なければ ./config.json）")
	port := flag.Int("port", 0, "待ち受けるポート（既定は設定の port）")
	bind := flag.String("bind", "", "待ち受けるアドレス（既定は設定の bind）")
	flag.Parse()

	cfg, err := loadConfig(*config, *demo)
	if err != nil {
		log.Fatal(err)
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *bind != "" {
		cfg.Bind = *bind
	}

	var h slog.Handler = slog.NewTextHandler(os.Stderr, nil)
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(os.Stderr, nil)
	}
	slog.SetDefault(slog.New(h)) // log.Printf もこの形式で出る

	col := newCollector(cfg)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { col.Run(stop); close(done) }()

	mux := http.NewServeMux()
	registerAPI(mux, col, cfg.CORSOrigin)
	registerMetrics(mux, col)
	registerHealth(mux, col)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(indexHTML)
	})

	addr := net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port))
	// SSE の長い接続を、止めるときにすぐ切れるようにする
	base, cancelBase := context.WithCancel(context.Background())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return base }}
	go func() {
		target := cfg.Host
		if cfg.Demo {
			target = "デモデータ"
		}
		slog.Info("TS-2560 dashboard", "url", "http://"+addr+"/", "target", target, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	// SIGTERM（k8s の停止）で、履歴を保存してから終わる
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancelBase()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	close(stop)
	select { // 履歴の保存を待つ
	case <-done:
	case <-time.After(5 * time.Second):
	}
}
