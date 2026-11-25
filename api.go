package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"
	"time"
)

// JSON の API（/api/v1/...）。すべて最後の収集結果を返すだけで、機器にはアクセスしない。
// 仕様は web/openapi.json（GET /api/openapi.json）にある。

// part は Snapshot から1つの資源を取り出す。nil を返したら、まだ取れていない。
type part func(s *Snapshot) any

var resources = map[string]part{
	"ntp": func(s *Snapshot) any {
		if s.NTP == nil && s.NTPProbe == nil {
			return nil
		}
		return map[string]any{"state": s.NTP, "peers": orEmpty(s.Peers), "probe": s.NTPProbe, "reference_offset_ms": s.RefDiffMs, "reference": s.Reference}
	},
	"gnss": func(s *Snapshot) any {
		if s.GNSS == nil {
			return nil
		}
		return map[string]any{"gnss": s.GNSS, "since": s.GNSSSince, "since_exact": s.GNSSSinceExact, "holdover_hours": s.HoldoverHours}
	},
	"gnss/satellites": func(s *Snapshot) any {
		if s.GNSS == nil {
			return nil
		}
		return s.GNSS.Satellites
	},
	"sky":      func(s *Snapshot) any { return nilIf(s.Sky == nil, s.Sky) },
	"hardware": func(s *Snapshot) any { return nilIf(s.Hardware == nil, s.Hardware) },
	"device":   func(s *Snapshot) any { return nilIf(s.Device == nil, s.Device) },
	"clients":  func(s *Snapshot) any { return nilIf(s.Clients == nil, s.Clients) },
	"requests": func(s *Snapshot) any { return nilIf(s.Requests == nil, s.Requests) },
	"log":      func(s *Snapshot) any { return nilIf(s.Log == nil, s.Log) },
	"history":  func(s *Snapshot) any { return s.History },
	"alerts":   func(s *Snapshot) any { return s.Alerts },
}

func nilIf(cond bool, v any) any {
	if cond {
		return nil
	}
	return v
}

func orEmpty[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// problem は RFC 9457 の形でエラーを返す。
func problem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": title, "status": status, "detail": detail})
}

// writeCached は ETag を付けて返す。同じ内容なら 304 を返す。
func writeCached(w http.ResponseWriter, r *http.Request, ctype string, body []byte, at time.Time) {
	h := fnv.New64a()
	_, _ = h.Write(body)
	etag := fmt.Sprintf(`"%x"`, h.Sum64())
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache") // 毎回確かめる。変わっていなければ 304 で中身は送らない
	if !at.IsZero() {
		w.Header().Set("Last-Modified", at.UTC().Format(http.TimeFormat))
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(body)
}

func registerAPI(mux *http.ServeMux, col *Collector, cors string) {
	withCORS := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if cors != "" {
				w.Header().Set("Access-Control-Allow-Origin", cors)
				w.Header().Set("Access-Control-Expose-Headers", "ETag, Last-Modified")
			}
			h(w, r)
		}
	}
	status := withCORS(func(w http.ResponseWriter, r *http.Request) {
		v := col.View()
		writeCached(w, r, "application/json; charset=utf-8", v.JSON, v.At)
	})
	mux.HandleFunc("GET /api/status", status) // 互換のため残す（/api/v1/status と同じ）
	mux.HandleFunc("GET /api/v1/status", status)
	for name, get := range resources {
		mux.HandleFunc("GET /api/v1/"+name, withCORS(func(w http.ResponseWriter, r *http.Request) {
			v := col.View()
			out := get(v.Snap)
			if out == nil {
				problem(w, http.StatusServiceUnavailable, "まだ取得できていません", name+" はまだ機器から読めていません。/api/v1/status の errors を見てください")
				return
			}
			b, err := json.Marshal(out)
			if err != nil {
				problem(w, http.StatusInternalServerError, "JSON にできません", err.Error())
				return
			}
			writeCached(w, r, "application/json; charset=utf-8", b, v.At)
		}))
	}
	mux.HandleFunc("GET /api/v1/events", withCORS(func(w http.ResponseWriter, r *http.Request) { serveEvents(w, r, col) }))
	mux.HandleFunc("GET /api/openapi.json", withCORS(func(w http.ResponseWriter, r *http.Request) {
		writeCached(w, r, "application/json; charset=utf-8", openAPI, time.Time{})
	}))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		problem(w, http.StatusNotFound, "ありません", r.Method+" "+r.URL.Path+" はありません。一覧は /api/openapi.json にあります")
	})
}

// serveEvents は Server-Sent Events で、収集が終わるたびに status 全体を送る。
// 接続した直後にも、いまの状態を1回送る。
func serveEvents(w http.ResponseWriter, r *http.Request, col *Collector) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // nginx などの前段でためこまれないように
	ch, cancel := col.Subscribe()
	defer cancel()

	retry := strconv.Itoa(col.cfg.Interval * 1000)
	send := func() error {
		v := col.View()
		if _, err := fmt.Fprintf(w, "retry: %s\nid: %d\nevent: status\ndata: %s\n\n", retry, v.Seq, v.JSON); err != nil {
			return err
		}
		return rc.Flush()
	}
	if err := send(); err != nil {
		return
	}
	ping := time.NewTicker(25 * time.Second) // 途中のプロキシに切られないように、ときどきコメント行を送る
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if send() != nil {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

// health はプローブ用。/healthz（生きているか）と /readyz（1回でも収集が終わったか）。
func registerHealth(mux *http.ServeMux, col *Collector) {
	text := func(w http.ResponseWriter, code int, s string) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_, _ = fmt.Fprintln(w, s)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { text(w, 200, "ok") })
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) { text(w, 200, "ok") })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if col.View().Seq == 0 {
			text(w, http.StatusServiceUnavailable, "not ready: 最初の収集が終わっていません")
			return
		}
		text(w, 200, "ok")
	})
}
