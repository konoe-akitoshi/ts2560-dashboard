package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// Source は機器の Web 画面の読み手。実機とデモの2種類がある。
type Source interface {
	Page(id string) (string, error) // ?id=ID_MENU_xxx の HTML
	File(name string) (string, error)
}

// ---------------------------------------------------------------- 実機

type Device struct {
	base, user, password string
	client               *http.Client
}

func newDevice(cfg Config) *Device {
	jar, _ := cookiejar.New(nil)
	return &Device{base: "http://" + cfg.Host + "/", user: cfg.Username, password: cfg.Password,
		client: &http.Client{Jar: jar, Timeout: 8 * time.Second}}
}

func (d *Device) do(req *http.Request) (string, error) {
	res, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d", req.URL.RequestURI(), res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return string(b), err
}

func (d *Device) get(path string) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, d.base+path, nil)
	return d.do(req)
}

// ログイン画面が返ってきたら、セッションが切れている
func loggedOut(page string) bool { return strings.Contains(page, `name="password"`) }

func (d *Device) login() error {
	form := url.Values{"username": {d.user}, "password": {d.password}}
	req, _ := http.NewRequest(http.MethodPost, d.base+"?id=ID_TOP", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	page, err := d.do(req)
	if err != nil {
		return err
	}
	if loggedOut(page) {
		return errors.New("ログインに失敗しました（ユーザー名とパスワードを確認してください）")
	}
	return nil
}

func (d *Device) Page(id string) (string, error) {
	page, err := d.get("?id=" + id)
	if err == nil && loggedOut(page) {
		if err = d.login(); err == nil {
			page, err = d.get("?id=" + id)
		}
	}
	return page, err
}

func (d *Device) File(name string) (string, error) { return d.get(name) }

// ---------------------------------------------------------------- デモ（実機なしで画面を確認する）

type Demo struct{ t0 time.Time }

func newDemo() *Demo { return &Demo{t0: time.Now()} }

func (d *Demo) bad() bool { return int(time.Since(d.t0).Seconds()/45)%2 == 1 } // 45秒ごとに正常/異常を切り替える

func jit(a float64) float64 { return (rand.Float64()*2 - 1) * a }

func pre(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.Trim(s, "\n"), "\n") {
		b.WriteString("<pre style=display:inline>" + l + "\n</pre>")
	}
	return b.String()
}

func (d *Demo) Page(id string) (string, error) {
	bad := d.bad()
	switch id {
	case "ID_MENU_NTPSTATE":
		peer, st, ref, j := "TimeServerGNSS", 1, "GNSS", 20+rand.IntN(40)
		if bad {
			peer, st, ref, j = "LOCAL", 6, "LOCL", 1
		}
		return pre(fmt.Sprintf("system peer:          %s\nleap indicator:       00\nstratum:              %d\nroot dispersion:      0.00009 s\nreference ID:         [%s]\njitter:               0.000%03d s", peer, st, ref, j)), nil
	case "ID_MENU_NTPPEER":
		rows := []string{
			" TimeServerOJJY  .NONE.           0 l  308   16    0    0.000    0.000   0.000",
			fmt.Sprintf("*TimeServerGNSS  .GNSS.           0 l    5   16  377    0.000   %6.3f   0.035", jit(.05)),
			" LOCAL           .LOCL.           5 l    4   16  377    0.000    0.000   0.001",
		}
		if bad {
			rows = []string{
				fmt.Sprintf("xTimeServerOJJY  .OJJY.           0 l    8   16  377    0.000   %6.3f   0.014", 4.85+jit(.02)),
				fmt.Sprintf("xTimeServerGNSS  .GNSS.           0 l    9   16  377    0.000   %6.3f   0.007", -5.6+jit(.02)),
				"*LOCAL           .LOCL.           5 l    8   16  377    0.000    0.000   0.001",
			}
		}
		return pre("Peer list (ntpq):\n     remote           refid      st t when poll reach   delay   offset  jitter\n" + strings.Join(rows, "\n")), nil
	case "ID_MENU_GNSS_STATE":
		type s struct {
			prn, el, az, snr int
			tr               bool
			sys              string
		}
		sats := []s{{13, 65, 74, 39, true, "GPS"}, {25, 63, 175, 26, false, "GPS"}, {10, 58, 262, 21, false, "GPS"}, {12, 55, 68, 44, true, "GPS"},
			{23, 46, 200, 24, false, "GPS"}, {32, 40, 315, 18, false, "GPS"}, {24, 31, 60, 41, true, "GPS"}, {28, 21, 264, 0, false, "GPS"},
			{15, 17, 124, 42, true, "GPS"}, {19, 1, 42, 20, false, "GPS"}, {96, 82, 210, 11, false, "QZSS"}, {94, 33, 159, 41, true, "QZSS"}, {95, 18, 195, 0, false, "QZSS"}}
		var lines []string
		for _, x := range sats {
			snr, on := x.snr, "Off"
			if snr > 0 {
				snr = max(0, snr+rand.IntN(5)-2)
			}
			if x.tr {
				on = "On"
			}
			lines = append(lines, fmt.Sprintf("      %03d   %03d   %03d  %03d   %s  (%s)", x.prn, x.el, x.az, snr, on, x.sys))
		}
		return pre(fmt.Sprintf("State:                  Sync\nLast Adjust Time:       %s\nOffset:                 %+.9f\nPPB:                    13\nAntenna:                Normal\nSync Transition:        1\nUnsync Transition:      0\nHoldover Transition:    0\nPositioning:            3D\nPDOP:                   9.0\nHDOP:                   6.5\nVDOP:                   6.0\nTracking Satellites:    5\nVisible Satellites:     13\nMax SNR:                44\nSatellites:\n  PRN No.  Elev  Azim  SNR  Trac\n%s\nJamming:\n  Freq band         Frequency    Strength\n  GPS/QZSS/GALILEO  1574.999742  %03d\nSpoofing:               Not Detected",
			time.Now().Format("2006-01-02 15:04:05"), jit(3e-6), strings.Join(lines, "\n"), 52+rand.IntN(27))), nil
	case "ID_MENU_ENVIRONMENT":
		return pre(fmt.Sprintf("### Power Supply Unit ###\nPSU(A)\n  State: Normal\nPSU(B)\n  State: Abnormal\n### Temperature ###\nSensor(1) near CPU\n  State: Normal\n  Current Degree: %.3f\nSensor(2) near PSU\n  State: Normal\n  Current Degree: %.3f\n### Oscillator ###\nState: Normal\n### RTC ###\nBattery State: Normal (as of boot)\n### Fan ###\nState: Normal",
			38.1+jit(.3), 36.1+jit(.3))), nil
	case "ID_MENU_NTPTRAFFIC":
		var rows []string
		for i := range 18 {
			rows = append(rows, fmt.Sprintf("  %4d %6d   50 . 3 4 %6d %5d 192.0.2.%d", 1+rand.IntN(900), 480, 200-i*9, 50000+i, i+1))
		}
		return pre(strings.Join(rows, "\n")), nil
	case "ID_TOP":
		var b strings.Builder
		b.WriteString("<title>TS-2560 Time Server</title>")
		for _, p := range [][2]string{{"ホスト名", "demo-ntp-01"}, {"バージョン", "8.0.0"}, {"製造番号", "00000000"}, {"起動要因", "Reboot"}, {"起動時間", "up  1:06 (as of now)"}} {
			b.WriteString("<th>" + p[0] + "</th>\n<td>" + p[1] + "</td>")
		}
		return b.String(), nil
	case "ID_MENU_MSGLOG":
		now := time.Now().Format("2006-01-02T15:04:05")
		return "<h4>log</h4>" + now + ".0+09:00 demo ntpd[509]: synchronized to TimeServerGNSS, stratum 0<br>" + now + ".0+09:00 demo ntpd[509]: Lost reachable association. Start to refer the local clock.", nil
	}
	return "", nil
}

func (d *Demo) File(string) (string, error) {
	base := time.Now().Add(-46 * time.Hour)
	var b strings.Builder
	for i := range 47 {
		t := base.Add(time.Duration(i) * time.Hour)
		fmt.Fprintf(&b, "%s,%d\n", t.Format("2006-01-02,15:00"), int(40+25*math.Sin(float64(i)/4))+rand.IntN(9))
	}
	return b.String(), nil
}
