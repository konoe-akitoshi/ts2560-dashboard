package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T, collect bool) (*httptest.Server, *Collector) {
	t.Helper()
	cfg := defaultConfig()
	cfg.Demo, cfg.Host, cfg.DataDir, cfg.Interval = true, "demo", t.TempDir(), 1
	col := newCollector(cfg)
	if collect {
		col.once()
	}
	mux := http.NewServeMux()
	registerAPI(mux, col, "")
	registerMetrics(mux, col)
	registerHealth(mux, col)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, col
}

func get(t *testing.T, url string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b strings.Builder
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(nil, 1<<22)
	for sc.Scan() {
		b.WriteString(sc.Text() + "\n")
	}
	return res, b.String()
}

func TestReadyAndProblem(t *testing.T) {
	srv, _ := testServer(t, false)
	if res, _ := get(t, srv.URL+"/readyz"); res.StatusCode != 503 {
		t.Errorf("収集前の /readyz は 503 のはずが %d", res.StatusCode)
	}
	res, body := get(t, srv.URL+"/api/v1/gnss")
	if res.StatusCode != 503 || res.Header.Get("Content-Type") != "application/problem+json" || !strings.Contains(body, `"status":503`) {
		t.Errorf("収集前の /api/v1/gnss: %d %s %s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	if res, _ := get(t, srv.URL+"/api/v1/nope"); res.StatusCode != 404 {
		t.Errorf("ない API は 404 のはずが %d", res.StatusCode)
	}
}

func TestAPIAndETag(t *testing.T) {
	srv, _ := testServer(t, true)
	if res, _ := get(t, srv.URL+"/readyz"); res.StatusCode != 200 {
		t.Fatalf("/readyz %d", res.StatusCode)
	}
	res, body := get(t, srv.URL+"/api/v1/gnss/satellites")
	var sats []Sat
	if err := json.Unmarshal([]byte(body), &sats); err != nil || len(sats) == 0 {
		t.Fatalf("衛星: %v %s", err, body)
	}
	etag := res.Header.Get("ETag")
	if res2, _ := get(t, srv.URL+"/api/v1/gnss/satellites", "If-None-Match", etag); res2.StatusCode != 304 {
		t.Errorf("同じ ETag なら 304 のはずが %d", res2.StatusCode)
	}
	for _, p := range []string{"/api/status", "/api/v1/status", "/api/v1/ntp", "/api/v1/hardware", "/api/v1/alerts", "/api/v1/history"} {
		if res, _ := get(t, srv.URL+p); res.StatusCode != 200 {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
}

// OpenAPI に書いたパスが、すべて実際に返ってくること
func TestOpenAPIMatchesRoutes(t *testing.T) {
	srv, _ := testServer(t, true)
	var doc struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openAPI, &doc); err != nil || doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi.json: %v %q", err, doc.OpenAPI)
	}
	for p := range doc.Paths {
		if p == "/api/v1/events" {
			continue
		}
		if res, _ := get(t, srv.URL+p); res.StatusCode != 200 {
			t.Errorf("OpenAPI にある %s が %d", p, res.StatusCode)
		}
	}
	for name := range resources {
		if _, ok := doc.Paths["/api/v1/"+name]; !ok {
			t.Errorf("/api/v1/%s が OpenAPI にありません", name)
		}
	}
}

var reSample = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{([a-zA-Z_][a-zA-Z0-9_]*="([^"\\]|\\.)*",?)*\})? (NaN|[+-]Inf|[-+0-9.eE]+)$`)

func TestMetricsFormat(t *testing.T) {
	srv, _ := testServer(t, true)
	res, body := get(t, srv.URL+"/metrics")
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("Content-Type: %s", ct)
	}
	typed := map[string]bool{}
	seen := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if strings.HasPrefix(l, "# TYPE ") {
			name := strings.Fields(l)[2]
			if typed[name] {
				t.Errorf("TYPE が2回: %s", name)
			}
			typed[name] = true
			continue
		}
		if strings.HasPrefix(l, "#") {
			continue
		}
		if !reSample.MatchString(l) {
			t.Errorf("形式が違う行: %q", l)
		}
		series := l[:strings.LastIndex(l, " ")]
		if seen[series] {
			t.Errorf("同じ系列が2回: %s", series)
		}
		seen[series] = true
		name := series
		if i := strings.IndexByte(series, '{'); i >= 0 {
			name = series[:i]
		}
		if !typed[name] {
			t.Errorf("TYPE より前に値: %s", name)
		}
	}
	for _, want := range []string{"ts2560_up 1", `ts2560_gnss_state{state="Sync"}`, `ts2560_ntp_sync_source{source="gnss"}`, `ts2560_hardware_normal{component="psu_b"} 0`, "ts2560_gnss_satellite_snr{", "ts2560_reference_offset_seconds{"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s がありません", want)
		}
	}
}

func TestEvents(t *testing.T) {
	srv, col := testServer(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type: %s", ct)
	}
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(nil, 1<<22)
	events := 0
	go col.once() // 2回目の収集で、もう1回届くはず
	for sc.Scan() && events < 2 {
		if l := sc.Text(); strings.HasPrefix(l, "data: ") {
			var s Snapshot
			if err := json.Unmarshal([]byte(l[6:]), &s); err != nil {
				t.Fatalf("data が JSON ではありません: %v", err)
			}
			events++
		}
	}
	if events != 2 {
		t.Fatalf("イベントが %d 回しか届きません", events)
	}
}
