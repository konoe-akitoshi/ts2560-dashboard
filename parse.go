package main

// TS-2560 の Web 画面（?id=ID_MENU_xxx）の HTML から値を取り出す。
// 状態画面は ntpq などの出力が 1 行ずつ <pre> に入っているので、テキストに戻してから読む。

import (
	"html"
	"regexp"
	"strconv"
	"strings"
)

var (
	rePre     = regexp.MustCompile(`(?s)<pre[^>]*>(.*?)</pre>`)
	reTag     = regexp.MustCompile(`<[^>]+>`)
	reBr      = regexp.MustCompile(`<br\s*/?>`)
	reSection = regexp.MustCompile(`^###\s*(.+?)\s*###`)
)

func preText(page string) string {
	var b strings.Builder
	for _, m := range rePre.FindAllStringSubmatch(page, -1) {
		b.WriteString(m[1])
	}
	return html.UnescapeString(b.String())
}

// kv は「Key:   value」の行から値を返す。見つからなければ nil。
func kv(text, key string) *string {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(key) + `:[ \t]*(.*?)\s*$`)
	m := re.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	return &m[1]
}

func kvInt(text, key string) *int {
	if s := kv(text, key); s != nil {
		if n, err := strconv.Atoi(*s); err == nil {
			return &n
		}
	}
	return nil
}

func kvFloat(text, key string) *float64 {
	if s := kv(text, key); s != nil {
		if f, err := strconv.ParseFloat(*s, 64); err == nil {
			return &f
		}
	}
	return nil
}

// kvFirstFloat は「0.00009 s」のように単位が付いた値の、最初の数字だけを読む。
func kvFirstFloat(text, key string) *float64 {
	if s := kv(text, key); s != nil {
		if f := strings.Fields(*s); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				return &v
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- NTP状態

type NTPState struct {
	SystemPeer     *string  `json:"system_peer"`
	Stratum        *int     `json:"stratum"`
	RefID          string   `json:"refid"`
	RootDispersion *float64 `json:"root_dispersion"`
	Jitter         *float64 `json:"jitter"`
	Leap           *string  `json:"leap"`
}

func parseNTPState(t string) *NTPState {
	s := &NTPState{
		SystemPeer:     kv(t, "system peer"),
		Stratum:        kvInt(t, "stratum"),
		RootDispersion: kvFirstFloat(t, "root dispersion"),
		Jitter:         kvFirstFloat(t, "jitter"),
		Leap:           kv(t, "leap indicator"),
	}
	if r := kv(t, "reference ID"); r != nil {
		s.RefID = strings.Trim(*r, "[]")
	}
	return s
}

// ---------------------------------------------------------------- NTPピアリスト

type Peer struct {
	Tally    string  `json:"tally"`
	Name     string  `json:"name"`
	RefID    string  `json:"refid"`
	Stratum  int     `json:"stratum"`
	When     string  `json:"when"`
	Poll     int     `json:"poll"`
	Reach    int     `json:"reach"`
	OffsetMs float64 `json:"offset_ms"`
	JitterMs float64 `json:"jitter_ms"`
}

var (
	rePeer = regexp.MustCompile(`(?m)^([ x*+\-#o.])(\S+)\s+\.(\w*)\.\s+(\d+)\s+(\w)\s+(\S+)\s+(\d+)\s+([0-7]+)\s+([\d.\-]+)\s+([\d.\-]+)\s+([\d.\-]+)`)
	tally  = map[string]string{"*": "selected", "+": "candidate", "x": "falseticker", "-": "outlier", " ": "reject", "#": "backup", "o": "pps", ".": "excess"}
)

func parsePeers(t string) []Peer {
	if i := strings.LastIndex(t, "Peer list (ntpq)"); i >= 0 { // ntpdc と ntpq の両方が載っているので、ntpq の方だけ読む
		t = t[i:]
	}
	out := []Peer{}
	for _, m := range rePeer.FindAllStringSubmatch(t, -1) {
		p := Peer{Tally: tally[m[1]], Name: m[2], RefID: m[3], When: m[6]}
		if p.Tally == "" {
			p.Tally = "reject"
		}
		p.Stratum, _ = strconv.Atoi(m[4])
		p.Poll, _ = strconv.Atoi(m[7])
		reach, _ := strconv.ParseInt(m[8], 8, 32)
		p.Reach = int(reach)
		p.OffsetMs, _ = strconv.ParseFloat(m[10], 64)
		p.JitterMs, _ = strconv.ParseFloat(m[11], 64)
		out = append(out, p)
	}
	return out
}

// ---------------------------------------------------------------- GNSS状態

type Sat struct {
	PRN     int    `json:"prn"`
	Elev    int    `json:"elev"`
	Azim    int    `json:"azim"`
	SNR     int    `json:"snr"`
	Tracked bool   `json:"tracked"`
	System  string `json:"system"`
	SNRMin  *int   `json:"snr_min,omitempty"`
	SNRMax  *int   `json:"snr_max,omitempty"`
}

type Jam struct {
	Band     string  `json:"band"`
	Freq     float64 `json:"freq"`
	Strength int     `json:"strength"`
}

type GNSS struct {
	State       *string         `json:"state"`
	LastAdjust  *string         `json:"last_adjust"`
	OffsetS     *float64        `json:"offset_s"`
	PPB         *int            `json:"ppb"`
	Antenna     *string         `json:"antenna"`
	Positioning *string         `json:"positioning"`
	PDOP        *float64        `json:"pdop"`
	HDOP        *float64        `json:"hdop"`
	VDOP        *float64        `json:"vdop"`
	Tracking    *int            `json:"tracking"`
	Visible     *int            `json:"visible"`
	MaxSNR      *int            `json:"max_snr"`
	Spoofing    *string         `json:"spoofing"`
	Latitude    *string         `json:"latitude"`
	Longitude   *string         `json:"longitude"`
	Altitude    *float64        `json:"altitude"`
	Transitions map[string]*int `json:"transitions"`
	Satellites  []Sat           `json:"satellites"`
	Jamming     []Jam           `json:"jamming"`
}

var (
	reSat = regexp.MustCompile(`(?m)^\s*(\d{3})\s+(\d{3})\s+(\d{3})\s+(\d{3})\s+(On|Off)\s+\((\w+)\)`)
	reJam = regexp.MustCompile(`(?m)^\s*([A-Z/]+)\s+(\d+\.\d+)\s+(\d+)\s*$`)
)

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func parseGNSS(t string) *GNSS {
	g := &GNSS{
		State: kv(t, "State"), LastAdjust: kv(t, "Last Adjust Time"),
		OffsetS: kvFloat(t, "Offset"), PPB: kvInt(t, "PPB"),
		Antenna: kv(t, "Antenna"), Positioning: kv(t, "Positioning"),
		PDOP: kvFloat(t, "PDOP"), HDOP: kvFloat(t, "HDOP"), VDOP: kvFloat(t, "VDOP"),
		Tracking: kvInt(t, "Tracking Satellites"), Visible: kvInt(t, "Visible Satellites"),
		MaxSNR: kvInt(t, "Max SNR"), Spoofing: kv(t, "Spoofing"),
		Latitude: kv(t, "Latitude"), Longitude: kv(t, "Longitude"), Altitude: kvFloat(t, "Altitude"),
		Transitions: map[string]*int{},
		Satellites:  []Sat{},
		Jamming:     []Jam{},
	}
	for _, k := range []string{"Sync", "Unsync", "Holdover"} {
		g.Transitions[k] = kvInt(t, k+" Transition")
	}
	for _, m := range reSat.FindAllStringSubmatch(t, -1) {
		g.Satellites = append(g.Satellites, Sat{PRN: atoi(m[1]), Elev: atoi(m[2]), Azim: atoi(m[3]), SNR: atoi(m[4]), Tracked: m[5] == "On", System: m[6]})
	}
	for _, m := range reJam.FindAllStringSubmatch(t, -1) {
		f, _ := strconv.ParseFloat(m[2], 64)
		g.Jamming = append(g.Jamming, Jam{Band: m[1], Freq: f, Strength: atoi(m[3])})
	}
	return g
}

// ---------------------------------------------------------------- ハードウェア

type PSU struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type Temp struct {
	Name    string   `json:"name"`
	State   *string  `json:"state,omitempty"`
	Celsius *float64 `json:"celsius,omitempty"`
}

type Hardware struct {
	PSU        []PSU   `json:"psu"`
	Temps      []Temp  `json:"temps"`
	Oscillator *string `json:"oscillator,omitempty"`
	Fan        *string `json:"fan,omitempty"`
	RTCBattery *string `json:"rtc_battery,omitempty"`
}

func after(s, prefix string) string { return strings.TrimSpace(strings.SplitN(s, ":", 2)[1]) }

func parseHardware(t string) *Hardware {
	h := &Hardware{PSU: []PSU{}, Temps: []Temp{}}
	section, item := "", ""
	for _, line := range strings.Split(t, "\n") {
		s := strings.TrimSpace(line)
		if m := reSection.FindStringSubmatch(s); m != nil {
			section, item = m[1], ""
			continue
		}
		switch {
		case section == "Power Supply Unit":
			if strings.HasPrefix(s, "PSU") {
				item = s
			} else if strings.HasPrefix(s, "State:") && item != "" {
				h.PSU = append(h.PSU, PSU{Name: item, State: after(s, "State:")})
			}
		case section == "Temperature":
			last := len(h.Temps) - 1
			if strings.HasPrefix(s, "Sensor") {
				h.Temps = append(h.Temps, Temp{Name: s})
			} else if strings.HasPrefix(s, "State:") && last >= 0 {
				v := after(s, "State:")
				h.Temps[last].State = &v
			} else if strings.HasPrefix(s, "Current Degree:") && last >= 0 {
				if f, err := strconv.ParseFloat(after(s, "Current Degree:"), 64); err == nil {
					h.Temps[last].Celsius = &f
				}
			}
		case (section == "Oscillator" || section == "Fan") && strings.HasPrefix(s, "State:"):
			v := after(s, "State:")
			if section == "Fan" {
				h.Fan = &v
			} else {
				h.Oscillator = &v
			}
		case section == "RTC" && strings.HasPrefix(s, "Battery State:"):
			v := after(s, "Battery State:")
			h.RTCBattery = &v
		}
	}
	return h
}

// ---------------------------------------------------------------- NTPトラフィック（クライアント一覧）

type Client struct {
	Last  int    `json:"last"`
	Avg   int    `json:"avg"`
	Count int    `json:"count"`
	Addr  string `json:"addr"`
}

var reTraffic = regexp.MustCompile(`(?m)^\s*(\d+)\s+(\d+)\s+\S+\s+\S\s+\d\s+\d\s+(\d+)\s+\d+\s+(\S+)\s*$`)

func parseTraffic(t string) []Client {
	out := []Client{}
	for _, m := range reTraffic.FindAllStringSubmatch(t, -1) {
		out = append(out, Client{Last: atoi(m[1]), Avg: atoi(m[2]), Count: atoi(m[3]), Addr: m[4]})
	}
	return out
}

// ---------------------------------------------------------------- トップ画面（機器情報）

type DeviceInfo struct {
	Model      *string             `json:"model"`
	Subsystem  *string             `json:"subsystem"`
	IPv4       map[string][]string `json:"ipv4"`
	IPv6       map[string][]string `json:"ipv6"`
	Hostname   *string             `json:"hostname"`
	Version    *string             `json:"version"`
	Serial     *string             `json:"serial"`
	BootReason *string             `json:"boot_reason"`
	Uptime     *string             `json:"uptime"`
}

var (
	reTitle  = regexp.MustCompile(`<title>\s*(\S+)`)
	reLan    = regexp.MustCompile(`(?s)<th>(LAN\d)</th>\s*<td>(.*?)</td>`)
	reScope  = regexp.MustCompile(`\s*Scope-Id:\d+`)
	reAsOf   = regexp.MustCompile(`\s*\(as of.*`)
	reLogMsg = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)\S*\s+\S+\s+([\w\-]+)(?:\[\d+\])?:\s*(.*)`)
	reLogTag = regexp.MustCompile(`^\[\d{8}-\d{6}\]\s*`)
)

func parseTop(page string) *DeviceInfo {
	cell := func(label string) *string {
		m := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(label) + `</th>\s*<td>(.*?)</td>`).FindStringSubmatch(page)
		if m == nil {
			return nil
		}
		v := strings.TrimSpace(reTag.ReplaceAllString(m[1], ""))
		return &v
	}
	lans := func(start, end string) map[string][]string {
		out := map[string][]string{}
		i, j := strings.Index(page, start), strings.Index(page, end)
		if i < 0 {
			return out
		}
		part := page[i:]
		if j > i {
			part = page[i:j]
		}
		for _, m := range reLan.FindAllStringSubmatch(part, -1) {
			var addrs []string
			for _, a := range reBr.Split(m[2], -1) {
				a = strings.TrimSpace(reScope.ReplaceAllString(a, ""))
				if a != "" && a != "&nbsp;" {
					addrs = append(addrs, a)
				}
			}
			if len(addrs) > 0 {
				out[m[1]] = addrs
			}
		}
		return out
	}
	d := &DeviceInfo{
		Subsystem: cell("サブシステム"),
		IPv4:      lans("IPv4アドレス", "IPv6アドレス"),
		IPv6:      lans("IPv6アドレス", "バージョン"),
		Hostname:  cell("ホスト名"), Version: cell("バージョン"), Serial: cell("製造番号"), BootReason: cell("起動要因"),
	}
	if m := reTitle.FindStringSubmatch(page); m != nil {
		d.Model = &m[1]
	}
	if up := cell("起動時間"); up != nil {
		v := strings.TrimSpace(strings.ReplaceAll(reAsOf.ReplaceAllString(*up, ""), "up", ""))
		if v != "" {
			d.Uptime = &v
		}
	}
	return d
}

// ---------------------------------------------------------------- ログ

type LogLine struct {
	Time string `json:"time"`
	Proc string `json:"proc"`
	Msg  string `json:"msg"`
}

func parseLog(page string, limit int) []LogLine {
	if i := strings.Index(page, "</h4>"); i >= 0 {
		page = page[i+len("</h4>"):]
	}
	out := []LogLine{}
	for _, raw := range reBr.Split(page, -1) {
		line := strings.TrimSpace(html.UnescapeString(reTag.ReplaceAllString(raw, "")))
		if m := reLogMsg.FindStringSubmatch(line); m != nil {
			out = append(out, LogLine{Time: m[1], Proc: m[2], Msg: reLogTag.ReplaceAllString(m[3], "")})
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- 1時間ごとのリクエスト数（ntphist.csv）

type Requests struct {
	T     string `json:"t"`
	Count int    `json:"count"`
}

func parseHist(csv string) []Requests {
	out := []Requests{}
	for _, line := range strings.Split(csv, "\n") {
		p := strings.Split(strings.TrimSpace(line), ",")
		if len(p) == 3 {
			if n, err := strconv.Atoi(p[2]); err == nil && n >= 0 {
				out = append(out, Requests{T: p[0] + "T" + p[1], Count: n})
			}
		}
	}
	return out
}
