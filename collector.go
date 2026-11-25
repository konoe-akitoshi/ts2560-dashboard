package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const skyAz, skyEl = 15, 15 // 空の受信記録のマス目（方位15°×仰角15°）

// Hist は推移グラフ用に、収集のたびに1点ずつためる値。
type Hist struct {
	T            int64    `json:"t"`
	RefDiffMs    *float64 `json:"ref_diff_ms"`
	GNSSOffsetMs *float64 `json:"gnss_offset_ms"`
	Tracking     *int     `json:"tracking"`
	GNSSUs       *float64 `json:"gnss_us"`
	AvgSNR       *float64 `json:"avg_snr"`
	Jamming      *int     `json:"jamming"`
}

type Alert struct {
	Level  string `json:"level"` // critical > serious > warning
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type SkyOut struct {
	AzStep   int         `json:"az_step"`
	ElStep   int         `json:"el_step"`
	Interval int         `json:"interval"`
	Cells    [][]float64 `json:"cells"` // [方位の区画, 仰角の区画, 回数, 平均の受信強度]
}

// Snapshot は /api/v1/status がそのまま返す、収集結果のすべて。
type Snapshot struct {
	OK             bool      `json:"ok"`
	Errors         []string  `json:"errors"`
	Updated        *int64    `json:"updated"`
	Demo           bool      `json:"demo"`
	Host           string    `json:"host"`
	NTPProbe       *NTPProbe `json:"ntp_probe"`
	RefDiffMs      *float64  `json:"ref_diff_ms"`
	Reference      *string   `json:"reference"`
	History        []Hist    `json:"history"`
	GNSSSince      *int64    `json:"gnss_since"`
	GNSSSinceExact bool      `json:"gnss_since_exact"`
	HoldoverHours  float64   `json:"holdover_hours"`
	Sky            *SkyOut   `json:"sky,omitempty"`

	NTP      *NTPState `json:"ntp,omitempty"`
	Peers    []Peer    `json:"peers,omitempty"`
	GNSS     *GNSS     `json:"gnss,omitempty"`
	Hardware *Hardware `json:"hardware,omitempty"`

	Device   *DeviceInfo `json:"device,omitempty"`
	Clients  []Client    `json:"clients,omitempty"`
	Requests []Requests  `json:"requests,omitempty"`
	Log      []LogLine   `json:"log,omitempty"`

	Alerts []Alert `json:"alerts"`
}

type slowData struct {
	device   *DeviceInfo
	clients  []Client
	requests []Requests
	log      []LogLine
}

type Collector struct {
	cfg Config
	src Source

	mu    sync.RWMutex
	snap  *Snapshot                  // 最後に作った Snapshot。公開したあとは書き換えない
	state []byte                     // その JSON
	seq   uint64                     // 収集した回数（0 はまだ一度も終わっていない）
	at    time.Time                  // 最後に収集が終わった時刻
	dur   time.Duration              // その収集にかかった時間
	subs  map[chan struct{}]struct{} // 更新を待っている SSE の接続

	history           []Hist
	sky               map[string][2]float64 // "方位の区画,仰角の区画" -> [回数, 受信強度の合計]
	histFile, skyFile string
	savedAt           time.Time
	gnssState         *string // GNSSの状態と、その状態になった時刻（このサーバーが見た範囲で）
	gnssSeen          bool
	gnssSince         *int64
	gnssSinceExact    bool
	satSNR            map[string][]int // 衛星ごとの直近の受信強度（最大・最小の印に使う）
	slow              slowData
	slowAt            time.Time
}

func newCollector(cfg Config) *Collector {
	c := &Collector{cfg: cfg, sky: map[string][2]float64{}, satSNR: map[string][]int{}, savedAt: time.Now(), subs: map[chan struct{}]struct{}{}}
	if cfg.Demo {
		c.src = newDemo()
	} else {
		c.src = newDevice(cfg)
	}
	suffix := ""
	if cfg.Demo {
		suffix = "-demo"
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Printf("データの置き場所を作れません: %v", err)
	}
	c.histFile = filepath.Join(cfg.DataDir, "history"+suffix+".json")
	c.skyFile = filepath.Join(cfg.DataDir, "sky"+suffix+".json")
	readJSON(c.histFile, &c.history) // 再起動しても推移グラフと空の記録が消えないように読み戻す
	readJSON(c.skyFile, &c.sky)
	if n := len(c.history); n > cfg.HistoryPoints {
		c.history = c.history[n-cfg.HistoryPoints:]
	}
	c.snap = &Snapshot{Errors: []string{"起動中…"}, Host: cfg.Host, Demo: cfg.Demo, History: []Hist{}, Alerts: []Alert{}}
	c.state, _ = json.Marshal(c.snap)
	return c
}

func readJSON(path string, v any) {
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, v); err != nil {
			log.Printf("%s を読めません: %v", path, err)
		}
	}
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", b, 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// View は最後の収集結果。Snap は読むだけにすること。
type View struct {
	Snap *Snapshot
	JSON []byte
	Seq  uint64
	At   time.Time
	Dur  time.Duration
}

func (c *Collector) View() View {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return View{c.snap, c.state, c.seq, c.at, c.dur}
}

// State は最後の収集結果（JSON）を返す。
func (c *Collector) State() []byte { return c.View().JSON }

// Subscribe は収集が終わるたびに知らせるチャネルと、購読をやめる関数を返す。
func (c *Collector) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

func (c *Collector) publish(s *Snapshot, start time.Time) {
	b, err := json.Marshal(s)
	if err != nil {
		b, _ = json.Marshal(Snapshot{Errors: []string{err.Error()}, History: []Hist{}, Alerts: []Alert{}})
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snap, c.state, c.seq, c.at, c.dur = s, b, c.seq+1, time.Now(), time.Since(start)
	for ch := range c.subs {
		select {
		case ch <- struct{}{}:
		default: // まだ前の知らせを読んでいない接続には、重ねて送らない
		}
	}
}

func (c *Collector) Run(stop <-chan struct{}) {
	for {
		t := time.Now()
		c.once()
		wait := time.Duration(c.cfg.Interval)*time.Second - time.Since(t)
		select {
		case <-stop:
			c.save()
			return
		case <-time.After(max(time.Second, wait)):
		}
	}
}

func (c *Collector) once() {
	start := time.Now()
	defer func() { // 収集が丸ごと失敗しても、画面にはエラーとして出す
		if r := recover(); r != nil {
			s := *c.View().Snap
			s.OK, s.Errors = false, []string{fmt.Sprintf("収集エラー: %v", r)}
			s.Alerts = []Alert{{"critical", "収集エラー", s.Errors[0]}}
			c.publish(&s, start)
		}
	}()
	snap := c.collect()
	c.publish(&snap, start)
}

// measure は NTP で機器と基準サーバーを測り、差（機器 − 基準）を ms で返す。基準は複数台の中央値。
func (c *Collector) measure() (*NTPProbe, *float64, *string) {
	if c.cfg.Demo {
		page, _ := c.src.Page("ID_MENU_NTPSTATE")
		p, d := &NTPProbe{Stratum: 1, RefID: "GNSS", RTT: .003}, 0.4+jit(.4)
		if strings.Contains(page, "LOCAL") {
			p, d = &NTPProbe{Stratum: 6, RefID: "LOCL", RTT: .003}, 9.8+jit(.4)
		}
		ref := "demo"
		return p, &d, &ref
	}
	dev := ntpQuery(c.cfg.Host, 2*time.Second, 3)
	if dev == nil {
		return nil, nil, nil
	}
	var vals []float64
	var used []string
	for _, h := range c.cfg.ReferenceNTP {
		if r := ntpQuery(h, 2*time.Second, 3); r != nil && r.Stratum != 0 && r.Stratum != 16 {
			vals = append(vals, (dev.Offset-r.Offset)*1000)
			used = append(used, h)
		}
	}
	if len(vals) == 0 {
		return dev, nil, nil
	}
	sort.Float64s(vals)
	ref := strings.Join(used, ", ")
	return dev, &vals[len(vals)/2], &ref
}

func (c *Collector) collect() Snapshot {
	now := time.Now()
	errs := []string{}
	probe, diff, ref := c.measure()
	if probe == nil {
		errs = append(errs, "NTP(UDP 123) に応答がありません")
	}

	s := Snapshot{Demo: c.cfg.Demo, Host: c.cfg.Host, NTPProbe: probe, RefDiffMs: diff, Reference: ref, HoldoverHours: c.cfg.GNSSHoldoverHours}
	for _, step := range []struct {
		id  string
		set func(string)
	}{
		{"ID_MENU_NTPSTATE", func(p string) { s.NTP = parseNTPState(preText(p)) }},
		{"ID_MENU_NTPPEER", func(p string) { s.Peers = parsePeers(preText(p)) }},
		{"ID_MENU_GNSS_STATE", func(p string) { s.GNSS = parseGNSS(preText(p)) }},
		{"ID_MENU_ENVIRONMENT", func(p string) { s.Hardware = parseHardware(preText(p)) }},
	} {
		page, err := c.src.Page(step.id)
		if err != nil {
			errs = append(errs, "Web画面の取得に失敗: "+err.Error())
			break
		}
		step.set(page)
	}

	// 機器情報・クライアント・ログはあまり変わらないので、間隔をあけて読む
	if len(errs) == 0 && now.Sub(c.slowAt) > time.Duration(c.cfg.SlowInterval)*time.Second {
		try := func(label string, f func() error) {
			if err := f(); err != nil {
				errs = append(errs, label+"の取得に失敗: "+err.Error())
			}
		}
		try("機器情報", func() error {
			p, err := c.src.Page("ID_TOP")
			if err == nil {
				c.slow.device = parseTop(p)
			}
			return err
		})
		try("クライアント一覧", func() error {
			p, err := c.src.Page("ID_MENU_NTPTRAFFIC")
			if err == nil {
				cl := parseTraffic(preText(p))
				sort.SliceStable(cl, func(i, j int) bool { return cl[i].Count > cl[j].Count })
				c.slow.clients = cl
			}
			return err
		})
		try("リクエスト数", func() error {
			if _, err := c.src.Page("ID_MENU_NTPPACKET"); err != nil { // ntphist.csv は「NTPパケット」画面を開いたときに機器側で作られる
				return err
			}
			csv, err := c.src.File("ntphist.csv")
			if err == nil {
				c.slow.requests = parseHist(csv)
			}
			return err
		})
		try("ログ", func() error {
			p, err := c.src.Page("ID_MENU_MSGLOG")
			if err == nil {
				c.slow.log = parseLog(p, 200)
			}
			return err
		})
		c.slowAt = now
	}
	s.Device, s.Clients, s.Requests, s.Log = c.slow.device, c.slow.clients, c.slow.requests, c.slow.log

	h := Hist{T: now.Unix(), RefDiffMs: diff}
	if g := s.GNSS; g != nil {
		c.trackGNSS(g, now)
		h.Tracking = g.Tracking
		if g.OffsetS != nil {
			h.GNSSUs = ptr(round(*g.OffsetS*1e6, 2))
		}
		var sum, n int
		for _, x := range g.Satellites {
			if x.Tracked {
				sum, n = sum+x.SNR, n+1
			}
		}
		if n > 0 {
			h.AvgSNR = ptr(round(float64(sum)/float64(n), 1))
		}
		for _, j := range g.Jamming {
			if h.Jamming == nil || j.Strength > *h.Jamming {
				h.Jamming = ptr(j.Strength)
			}
		}
	}
	for _, p := range s.Peers {
		if strings.Contains(p.Name, "GNSS") {
			if p.Reach != 0 {
				h.GNSSOffsetMs = ptr(p.OffsetMs)
			}
			break
		}
	}
	c.history = append(c.history, h)
	if n := len(c.history); n > c.cfg.HistoryPoints {
		c.history = c.history[n-c.cfg.HistoryPoints:]
	}
	if now.Sub(c.savedAt) > time.Minute {
		c.save()
	}

	s.OK, s.Errors, s.Updated = len(errs) == 0, errs, ptr(now.Unix())
	s.History = slices.Clone(c.history)
	s.GNSSSince, s.GNSSSinceExact = c.gnssSince, c.gnssSinceExact
	s.Sky = c.skyOut()
	s.Alerts = buildAlerts(&s, c.cfg)
	return s
}

// trackGNSS は GNSS の状態が変わった時刻と、空の受信記録・衛星ごとの最大最小を更新する。
func (c *Collector) trackGNSS(g *GNSS, now time.Time) {
	if !c.gnssSeen || deref(g.State) != deref(c.gnssState) {
		c.gnssSinceExact = c.gnssSeen // 起動直後の最初の1回は、いつからその状態かが分からない
		c.gnssSeen, c.gnssState, c.gnssSince = true, g.State, ptr(now.Unix())
	}
	seen := map[string]bool{}
	for i := range g.Satellites {
		x := &g.Satellites[i]
		key := fmt.Sprintf("%d,%d", ((x.Azim%360)+360)%360/skyAz, min(89, max(0, x.Elev))/skyEl)
		cell := c.sky[key]
		c.sky[key] = [2]float64{cell[0] + 1, cell[1] + float64(x.SNR)}

		id := x.System + strconv.Itoa(x.PRN)
		seen[id] = true
		q := append(c.satSNR[id], x.SNR)
		if len(q) > 60 {
			q = q[len(q)-60:]
		}
		c.satSNR[id] = q
		x.SNRMin, x.SNRMax = ptr(slices.Min(q)), ptr(slices.Max(q))
	}
	for id := range c.satSNR {
		if !seen[id] {
			delete(c.satSNR, id)
		}
	}
}

func (c *Collector) skyOut() *SkyOut {
	out := &SkyOut{AzStep: skyAz, ElStep: skyEl, Interval: c.cfg.Interval, Cells: [][]float64{}}
	for k, v := range c.sky {
		var az, el int
		if _, err := fmt.Sscanf(k, "%d,%d", &az, &el); err != nil || v[0] == 0 {
			continue
		}
		out.Cells = append(out.Cells, []float64{float64(az), float64(el), v[0], round(v[1]/v[0], 1)})
	}
	return out
}

func (c *Collector) save() {
	c.savedAt = time.Now()
	if err := writeJSON(c.histFile, c.history); err != nil {
		log.Printf("履歴を保存できません: %v", err)
	}
	if err := writeJSON(c.skyFile, c.sky); err != nil {
		log.Printf("空の記録を保存できません: %v", err)
	}
}

// ---------------------------------------------------------------- 警告

func buildAlerts(s *Snapshot, cfg Config) []Alert {
	a := []Alert{}
	add := func(level, title, detail string) { a = append(a, Alert{level, title, detail}) }
	for _, e := range s.Errors {
		add("critical", "収集エラー", e)
	}
	stratum, peer := 0, ""
	if s.NTP != nil && s.NTP.Stratum != nil {
		stratum, peer = *s.NTP.Stratum, deref(s.NTP.SystemPeer)
	} else if s.NTPProbe != nil {
		stratum, peer = s.NTPProbe.Stratum, s.NTPProbe.RefID
	}
	if stratum > cfg.MaxStratum {
		add("critical", fmt.Sprintf("Stratum %d で配信中", stratum), fmt.Sprintf("時刻源: %s（本来は GNSS で stratum 1）", peer))
	}
	var falses []string
	for _, p := range s.Peers {
		if p.Tally == "falseticker" {
			falses = append(falses, strings.ReplaceAll(p.Name, "TimeServer", ""))
		}
	}
	if len(falses) > 0 {
		add("critical", "時刻源が除外されています", strings.Join(falses, " / ")+" が互いに食い違い、ntpd が信用していません")
	}
	if d := s.RefDiffMs; d != nil && math.Abs(*d) > cfg.MaxRefDiffMs {
		add("serious", fmt.Sprintf("基準時刻との差 %+.1f ms", *d), fmt.Sprintf("外部の基準サーバー（%s）との比較", deref(s.Reference)))
	}
	if g := s.GNSS; g != nil {
		if deref(g.State) != "Sync" {
			tr := "–"
			if g.Tracking != nil {
				tr = strconv.Itoa(*g.Tracking)
			}
			add("serious", "GNSS が同期していません", fmt.Sprintf("State: %s / 追尾 %s 機", deref(g.State), tr))
		}
		if g.Antenna != nil && *g.Antenna != "Normal" {
			add("serious", "GNSSアンテナ異常", "Antenna: "+*g.Antenna)
		}
		if g.Spoofing != nil && *g.Spoofing != "Not Detected" {
			add("critical", "スプーフィングを検出", *g.Spoofing)
		}
		// 妨害電波の指標は単位も基準も不明なので、絶対値ではなく「ふだん（中央値）からの上昇」で知らせる
		var past []int
		for _, h := range s.History[:max(0, len(s.History)-1)] {
			if h.Jamming != nil {
				past = append(past, *h.Jamming)
			}
		}
		if len(past) >= 30 {
			sort.Ints(past)
			base := past[len(past)/2]
			for _, j := range g.Jamming {
				if j.Strength >= base+cfg.JammingRise {
					add("warning", "妨害電波の指標が上がっています", fmt.Sprintf("%s %.3f MHz / いま %d（ふだんは %d 前後）", j.Band, j.Freq, j.Strength, base))
				}
			}
		}
	}
	if hw := s.Hardware; hw != nil {
		for _, p := range hw.PSU {
			if p.State != "Normal" {
				add("warning", p.Name+" が "+p.State, "電源ユニットを確認してください")
			}
		}
		for _, t := range hw.Temps {
			if t.State != nil && *t.State != "Normal" {
				c := "–"
				if t.Celsius != nil {
					c = strconv.FormatFloat(*t.Celsius, 'f', -1, 64)
				}
				add("serious", "温度異常", t.Name+": "+c+"℃")
			}
		}
		if hw.Fan != nil && *hw.Fan != "Normal" {
			add("serious", "ファン異常", *hw.Fan)
		}
		if hw.Oscillator != nil && *hw.Oscillator != "Normal" {
			add("serious", "発振器異常", *hw.Oscillator)
		}
	}
	order := map[string]int{"critical": 0, "serious": 1, "warning": 2}
	sort.SliceStable(a, func(i, j int) bool { return order[a[i].Level] < order[a[j].Level] })
	return a
}

func ptr[T any](v T) *T { return &v }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func round(v float64, d int) float64 {
	p := math.Pow(10, float64(d))
	return math.Round(v*p) / p
}
