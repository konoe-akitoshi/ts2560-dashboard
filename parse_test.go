package main

import (
	"strings"
	"testing"
)

func TestParseNTPStateAndPeers(t *testing.T) {
	text := preText(pre(`system peer:          LOCAL
leap indicator:       00
stratum:              6
root dispersion:      0.00009 s
reference ID:         [LOCL]
jitter:               0.000000 s`))
	s := parseNTPState(text)
	if deref(s.SystemPeer) != "LOCAL" || s.Stratum == nil || *s.Stratum != 6 || s.RefID != "LOCL" || s.RootDispersion == nil || *s.RootDispersion != 0.00009 {
		t.Fatalf("NTP状態を読めていません: %+v", s)
	}

	peers := parsePeers(preText(pre(`Peer list (ntpdc):
*LOCAL           127.0.0.1        5   16  377 0.00000  0.000000 0.01524
Peer list (ntpq):
     remote           refid      st t when poll reach   delay   offset  jitter
==============================================================================
xTimeServerOJJY  .OJJY.           0 l    8   16  377    0.000    4.880   0.008
xTimeServerGNSS  .GNSS.           0 l    9   16  377    0.000   -5.597   0.008
*LOCAL           .LOCL.           5 l    8   16  377    0.000    0.000   0.001
 TimeServerOJJY  .NONE.           0 l  308   16    0    0.000    0.000   0.000`)))
	if len(peers) != 4 {
		t.Fatalf("ntpq の4行だけを読むはずが %d 行", len(peers))
	}
	if p := peers[1]; p.Tally != "falseticker" || p.Name != "TimeServerGNSS" || p.Reach != 255 || p.OffsetMs != -5.597 {
		t.Errorf("GNSS の行: %+v", p)
	}
	if peers[2].Tally != "selected" || peers[3].Tally != "reject" || peers[3].Reach != 0 {
		t.Errorf("選択状況: %+v", peers)
	}
}

func TestParseGNSS(t *testing.T) {
	page, _ := newDemo().Page("ID_MENU_GNSS_STATE")
	g := parseGNSS(preText(page))
	if deref(g.State) != "Sync" || g.Tracking == nil || *g.Tracking != 5 || g.PDOP == nil || *g.PDOP != 9.0 {
		t.Fatalf("GNSS状態: %+v", g)
	}
	if len(g.Satellites) != 13 {
		t.Fatalf("衛星は13機のはずが %d 機", len(g.Satellites))
	}
	if s := g.Satellites[0]; s.PRN != 13 || s.Elev != 65 || s.Azim != 74 || !s.Tracked || s.System != "GPS" {
		t.Errorf("1機目: %+v", s)
	}
	if s := g.Satellites[11]; s.PRN != 94 || s.System != "QZSS" {
		t.Errorf("QZSS: %+v", s)
	}
	if len(g.Jamming) != 1 || g.Jamming[0].Band != "GPS/QZSS/GALILEO" || g.Jamming[0].Freq != 1574.999742 {
		t.Errorf("妨害電波: %+v", g.Jamming)
	}
	if v := g.Transitions["Sync"]; v == nil || *v != 1 {
		t.Errorf("Sync Transition: %v", v)
	}
}

func TestParseHardware(t *testing.T) {
	page, _ := newDemo().Page("ID_MENU_ENVIRONMENT")
	h := parseHardware(preText(page))
	if len(h.PSU) != 2 || h.PSU[1].Name != "PSU(B)" || h.PSU[1].State != "Abnormal" {
		t.Fatalf("電源: %+v", h.PSU)
	}
	if len(h.Temps) != 2 || h.Temps[0].Celsius == nil || deref(h.Temps[0].State) != "Normal" {
		t.Fatalf("温度: %+v", h.Temps)
	}
	if deref(h.Fan) != "Normal" || deref(h.Oscillator) != "Normal" || !strings.HasPrefix(deref(h.RTCBattery), "Normal") {
		t.Errorf("ファン・発振器・RTC: %+v", h)
	}
}

func TestParseTop(t *testing.T) {
	page := `<title>TS-2560 Time Server</title>
<tr><th>ホスト名</th><td>ntp-01</td></tr>
<tr><th colspan="2">IPv4アドレス</th></tr>
<tr><th>LAN1</th><td>192.0.2.10<br>
</td></tr><tr><th>LAN2</th><td><br>
</td></tr>
<tr><th colspan="2">IPv6アドレス</th></tr>
<tr><th>LAN1</th><td>fe80::1 Scope-Id:2<br>2001:db8::10<br>
</td></tr>
<tr><th colspan="2">バージョン</th><td>8.0.0</td></tr>
<tr><th colspan="2">製造番号</th><td>00000000</td></tr>
<tr><th colspan="2">起動時間</th><td>up  1:06 (as of 2026-11-25 10:50:59)</td></tr>`
	d := parseTop(page)
	if deref(d.Model) != "TS-2560" || deref(d.Hostname) != "ntp-01" || deref(d.Version) != "8.0.0" || deref(d.Uptime) != "1:06" {
		t.Fatalf("機器情報: %+v", d)
	}
	if got := d.IPv4["LAN1"]; len(got) != 1 || got[0] != "192.0.2.10" {
		t.Errorf("IPv4: %v", d.IPv4)
	}
	if _, ok := d.IPv4["LAN2"]; ok {
		t.Errorf("空の LAN2 は出さない: %v", d.IPv4)
	}
	if got := d.IPv6["LAN1"]; len(got) != 2 || got[0] != "fe80::1" {
		t.Errorf("IPv6（Scope-Id を除く）: %v", d.IPv6)
	}
}

func TestParseLogTrafficHist(t *testing.T) {
	logs := parseLog(`<h4>ログ参照 － 時刻源</h4>
2026-11-25T08:47:19.811050+09:00 ntp-01 optd[502]: [20261124-234719] Successfully received a time signal
<br>2026-11-25T10:45:51.687055+09:00 ntp-01 ntpd[509]: synchronized to TimeServerGNSS, stratum 0`, 200)
	if len(logs) != 2 || logs[0].Proc != "optd" || logs[0].Msg != "Successfully received a time signal" || logs[1].Time != "2026-11-25T10:45:51" {
		t.Fatalf("ログ: %+v", logs)
	}

	cl := parseTraffic(preText(pre(`lstint avgint rstr r m v  count rport remote address
==============================================================================
    60    482   50 . 3 4    104 56568 192.0.2.9
   501    455   50 . 3 4    109   123 192.0.2.1`)))
	if len(cl) != 2 || cl[1].Count != 109 || cl[1].Addr != "192.0.2.1" || cl[0].Last != 60 {
		t.Fatalf("クライアント: %+v", cl)
	}

	h := parseHist("2026-11-18,15:00,0\n2026-11-18,16:00,4\nbroken\n")
	if len(h) != 2 || h[1].T != "2026-11-18T16:00" || h[1].Count != 4 {
		t.Fatalf("リクエスト数: %+v", h)
	}
}

func TestAlerts(t *testing.T) {
	six := 6
	peer := "LOCAL"
	s := &Snapshot{NTP: &NTPState{Stratum: &six, SystemPeer: &peer}, Peers: []Peer{{Tally: "falseticker", Name: "TimeServerGNSS"}}, Errors: []string{}}
	a := buildAlerts(s, defaultConfig())
	if len(a) != 2 || a[0].Level != "critical" || !strings.Contains(a[0].Title, "Stratum 6") {
		t.Fatalf("警告: %+v", a)
	}
}
