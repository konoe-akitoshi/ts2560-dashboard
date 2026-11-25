// ts2560-dashboard は、セイコー TS-2560 タイムサーバーの状態を1画面で見るためのダッシュボード。
//
// 機器の Web 画面にログインして状態画面のテキストを読み、NTP(UDP 123) で公開NTPサーバーとの
// 時刻差も測って、/api/status に JSON で出す。画面（web/index.html）は実行ファイルに埋め込んである。
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"log"
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

	col := newCollector(cfg)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { col.Run(stop); close(done) }()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(col.State())
	})
	// 死活確認。収集サーバー自体が動いていれば 200 を返す（機器の状態は /api/status の ok で見る）
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(indexHTML)
	})

	addr := net.JoinHostPort(cfg.Bind, strconv.Itoa(cfg.Port))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		target := cfg.Host
		if cfg.Demo {
			target = "デモデータ"
		}
		log.Printf("TS-2560 dashboard: http://%s/ (%s)", addr, target)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	// SIGTERM（k8s の停止）で、履歴を保存してから終わる
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	close(stop)
	select {
	case <-done:
	case <-ctx.Done():
	}
}
