package main

import (
	"fmt"
	"math"
	"math/bits"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Prometheus のテキスト形式（version 0.0.4）で /metrics を出す。外部ライブラリは使わない。
//
// 公式の指針では、エクスポーターはスクレイプのたびに機器を読むのが基本だが、この機器は
// Web 画面にログインして何枚も読む必要があり重い。画面・API・Prometheus が同じ結果を使えるよう、
// 収集は interval 秒ごとの1回にまとめ、/metrics はその結果を返す。
// 古さは ts2560_last_collect_timestamp_seconds で分かる。

var version = "dev" // ビルド時に -ldflags "-X main.version=..." で入る

type metric struct {
	name, typ, help string
	samples         []sample
}

type sample struct {
	labels []string // name, value, name, value ...
	value  float64
}

type metrics struct{ list []*metric }

func (m *metrics) add(name, typ, help string) *metric {
	x := &metric{name: name, typ: typ, help: help}
	m.list = append(m.list, x)
	return x
}

func (x *metric) set(v float64, labels ...string) *metric {
	x.samples = append(x.samples, sample{labels, v})
	return x
}

func (x *metric) setPtr(v *float64, labels ...string) *metric {
	if v != nil {
		x.set(*v, labels...)
	}
	return x
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

var labelEsc = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
var helpEsc = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

func (m *metrics) write(w *strings.Builder) {
	for _, x := range m.list {
		if len(x.samples) == 0 {
			continue // 値がないメトリクスは HELP/TYPE ごと出さない
		}
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", x.name, helpEsc.Replace(x.help), x.name, x.typ)
		for _, s := range x.samples {
			w.WriteString(x.name)
			if len(s.labels) > 0 {
				w.WriteByte('{')
				for i := 0; i+1 < len(s.labels); i += 2 {
					if i > 0 {
						w.WriteByte(',')
					}
					fmt.Fprintf(w, `%s="%s"`, s.labels[i], labelEsc.Replace(s.labels[i+1]))
				}
				w.WriteByte('}')
			}
			w.WriteByte(' ')
			w.WriteString(formatFloat(s.value))
			w.WriteByte('\n')
		}
	}
}

func formatFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	if a := math.Abs(v); a >= 1e6 && a < 1e21 { // 時刻や周波数を 1.79e+09 ではなく桁どおりに出す
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// ntpd の同期先を、アラートで使いやすい名前に分ける。
func syncSource(peer string) string {
	switch {
	case peer == "":
		return "none"
	case peer == "LOCAL":
		return "local"
	case strings.HasPrefix(peer, "TimeServer"):
		return strings.ToLower(strings.TrimPrefix(peer, "TimeServer")) // gnss, ojjy など
	}
	return "server" // 上位NTPサーバー
}

var (
	gnssStates    = []string{"Init", "Sync", "Holdover", "Unsync", "Forcesync", "Disabled"} // 手引き 1.3.2.2
	syncSources   = []string{"gnss", "ojjy", "fm", "ljjy", "server", "local", "none"}
	reSensor      = regexp.MustCompile(`^Sensor\((\d+)\)\s*(.*)$`)
	reNonAlnumLow = regexp.MustCompile(`[^a-z0-9]+`)
)

func componentName(s string) string {
	return strings.Trim(reNonAlnumLow.ReplaceAllString(strings.ToLower(s), "_"), "_")
}

func buildMetrics(v View) string {
	s := v.Snap
	m := &metrics{}

	m.add("ts2560_dashboard_build_info", "gauge", "Build information of ts2560-dashboard (always 1).").
		set(1, "version", version, "goversion", runtime.Version())
	m.add("ts2560_up", "gauge", "1 if the last collection read every status page of the device without error.").
		set(b2f(v.Seq > 0 && s.OK))
	if v.Seq > 0 {
		m.add("ts2560_last_collect_timestamp_seconds", "gauge", "Unix time when the last collection finished.").
			set(float64(v.At.UnixMilli()) / 1000)
		m.add("ts2560_collect_duration_seconds", "gauge", "Time the last collection took.").
			set(v.Dur.Seconds())
	}
	al := map[string]int{"critical": 0, "serious": 0, "warning": 0}
	for _, a := range s.Alerts {
		al[a.Level]++
	}
	am := m.add("ts2560_alerts", "gauge", "Number of alerts the dashboard currently shows, by level.")
	for _, lv := range []string{"critical", "serious", "warning"} {
		am.set(float64(al[lv]), "level", lv)
	}

	if d := s.Device; d != nil {
		m.add("ts2560_device_info", "gauge", "Device model, firmware and identity read from the top page (always 1).").
			set(1, "model", deref(d.Model), "firmware", deref(d.Version), "hostname", deref(d.Hostname), "serial", deref(d.Serial))
	}

	// ---- NTP（このサーバーから UDP 123 で測った値）
	m.add("ts2560_ntp_probe_success", "gauge", "1 if the device answered an NTP client query from this exporter.").
		set(b2f(s.NTPProbe != nil))
	if p := s.NTPProbe; p != nil {
		m.add("ts2560_ntp_probe_stratum", "gauge", "Stratum in NTP replies the device sends to clients.").set(float64(p.Stratum))
		m.add("ts2560_ntp_probe_leap_indicator", "gauge", "Leap indicator in NTP replies (3 = unsynchronized).").set(float64(p.Leap))
		m.add("ts2560_ntp_probe_rtt_seconds", "gauge", "Round-trip time of the NTP query to the device.").set(p.RTT)
	}
	if s.RefDiffMs != nil {
		m.add("ts2560_reference_offset_seconds", "gauge", "Device time minus the median of the reference NTP servers (positive = device ahead).").
			set(*s.RefDiffMs/1000, "reference", deref(s.Reference))
	}

	// ---- NTP（機器の ntpd の状態）
	if n := s.NTP; n != nil {
		if n.Stratum != nil {
			m.add("ts2560_ntp_stratum", "gauge", "Stratum of the device's ntpd.").set(float64(*n.Stratum))
		}
		src := syncSource(deref(n.SystemPeer))
		sm := m.add("ts2560_ntp_sync_source", "gauge", "1 for the reference the device's ntpd is synchronized to (gnss, ojjy, server, local, none ...).")
		known := false
		for _, k := range syncSources {
			sm.set(b2f(k == src), "source", k)
			known = known || k == src
		}
		if !known {
			sm.set(1, "source", src)
		}
		if n.RootDispersion != nil {
			m.add("ts2560_ntp_root_dispersion_seconds", "gauge", "Root dispersion of the device's ntpd.").set(*n.RootDispersion)
		}
		if n.Jitter != nil {
			m.add("ts2560_ntp_jitter_seconds", "gauge", "System jitter of the device's ntpd.").set(*n.Jitter)
		}
	}
	if len(s.Peers) > 0 {
		off := m.add("ts2560_ntp_peer_offset_seconds", "gauge", "Offset of each ntpd peer (ntpq).")
		jit := m.add("ts2560_ntp_peer_jitter_seconds", "gauge", "Jitter of each ntpd peer (ntpq).")
		reach := m.add("ts2560_ntp_peer_reach_ratio", "gauge", "Share of the last 8 polls that got an answer (reach register).")
		st := m.add("ts2560_ntp_peer_status", "gauge", "Selection status of each ntpd peer as a label (always 1): selected, candidate, falseticker, outlier, reject ...")
		seen := map[string]bool{}
		for _, p := range s.Peers {
			if seen[p.Name] { // ntpq には同じ名前が2行出ることがある（無効にした時刻源の古い行など）。最初の行を使う
				continue
			}
			seen[p.Name] = true
			off.set(p.OffsetMs/1000, "peer", p.Name)
			jit.set(p.JitterMs/1000, "peer", p.Name)
			reach.set(float64(bits.OnesCount8(uint8(p.Reach)))/8, "peer", p.Name)
			st.set(1, "peer", p.Name, "status", p.Tally)
		}
	}

	// ---- GNSS
	if g := s.GNSS; g != nil {
		state := deref(g.State)
		gs := m.add("ts2560_gnss_state", "gauge", "1 for the current state of the GNSS time-source clock.")
		for _, k := range gnssStates {
			gs.set(b2f(k == state), "state", k)
		}
		if g.Antenna != nil {
			m.add("ts2560_gnss_antenna_ok", "gauge", "1 if the GNSS antenna state is Normal (0 for Short or Unknown).").set(b2f(*g.Antenna == "Normal"))
		}
		if g.Spoofing != nil {
			m.add("ts2560_gnss_spoofing_detected", "gauge", "1 if the receiver reports spoofing.").set(b2f(*g.Spoofing != "Not Detected"))
		}
		if g.OffsetS != nil {
			m.add("ts2560_gnss_offset_seconds", "gauge", "Offset between GNSS time and the time-source clock.").set(*g.OffsetS)
		}
		if g.PPB != nil {
			m.add("ts2560_gnss_frequency_adjustment_ratio", "gauge", "Rate adjustment applied to the time-source clock (PPB x 1e-9).").set(float64(*g.PPB) * 1e-9)
		}
		if g.Tracking != nil {
			m.add("ts2560_gnss_satellites_tracked", "gauge", "Satellites used for positioning.").set(float64(*g.Tracking))
		}
		if g.Visible != nil {
			m.add("ts2560_gnss_satellites_visible", "gauge", "Satellites in view.").set(float64(*g.Visible))
		}
		if g.MaxSNR != nil {
			m.add("ts2560_gnss_snr_max", "gauge", "Highest SNR among tracked satellites (dB-Hz). The manual says 40-50 is normal and 35 or less needs a better antenna position.").set(float64(*g.MaxSNR))
		}
		dop := m.add("ts2560_gnss_dop", "gauge", "Dilution of precision.")
		dop.setPtr(g.PDOP, "kind", "pdop").setPtr(g.HDOP, "kind", "hdop").setPtr(g.VDOP, "kind", "vdop")
		if g.Altitude != nil && g.Positioning != nil && *g.Positioning != "None" {
			m.add("ts2560_gnss_altitude_meters", "gauge", "Antenna altitude above the WGS-84 ellipsoid.").set(*g.Altitude)
		}
		tr := m.add("ts2560_gnss_state_transitions_total", "counter", "Transitions of the GNSS time-source clock into each state since the device booted.")
		keys := make([]string, 0, len(g.Transitions))
		for k := range g.Transitions {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if n := g.Transitions[k]; n != nil {
				tr.set(float64(*n), "state", k)
			}
		}
		if s.GNSSSince != nil {
			m.add("ts2560_gnss_state_since_timestamp_seconds", "gauge", "Unix time when the GNSS state last changed, as seen by this exporter. Until a change is observed it is the time the exporter first saw the state.").
				set(float64(*s.GNSSSince))
		}
		if len(g.Satellites) > 0 {
			snr := m.add("ts2560_gnss_satellite_snr", "gauge", "SNR of each satellite in view (dB-Hz).")
			trk := m.add("ts2560_gnss_satellite_tracked", "gauge", "1 if the satellite is used for positioning.")
			el := m.add("ts2560_gnss_satellite_elevation_degrees", "gauge", "Elevation of each satellite in view.")
			az := m.add("ts2560_gnss_satellite_azimuth_degrees", "gauge", "Azimuth of each satellite in view.")
			for _, x := range g.Satellites {
				l := []string{"system", x.System, "prn", strconv.Itoa(x.PRN)}
				snr.set(float64(x.SNR), l...)
				trk.set(b2f(x.Tracked), l...)
				el.set(float64(x.Elev), l...)
				az.set(float64(x.Azim), l...)
			}
		}
		// 妨害電波は帯域ごとに一番強い値。周波数はラベルにすると値が揺れて系列が増えるので、別の値にする
		if len(g.Jamming) > 0 {
			best := map[string]Jam{}
			order := []string{}
			for _, j := range g.Jamming {
				b, ok := best[j.Band]
				if !ok {
					order = append(order, j.Band)
				}
				if !ok || j.Strength > b.Strength {
					best[j.Band] = j
				}
			}
			js := m.add("ts2560_gnss_jamming_strength", "gauge", "Strongest jamming signal per band as reported by the receiver (unitless; compare with its usual level).")
			jf := m.add("ts2560_gnss_jamming_frequency_hertz", "gauge", "Frequency of the strongest jamming signal per band.")
			for _, band := range order {
				j := best[band]
				js.set(float64(j.Strength), "band", band)
				jf.set(j.Freq*1e6, "band", band)
			}
		}
	}

	// ---- ハードウェア
	if h := s.Hardware; h != nil {
		hn := m.add("ts2560_hardware_normal", "gauge", "1 if the component state is Normal (psu_a, psu_b, fan, oscillator, rtc_battery, temperature_1 ...).")
		for _, p := range h.PSU {
			hn.set(b2f(p.State == "Normal"), "component", componentName(p.Name))
		}
		tc := m.add("ts2560_temperature_celsius", "gauge", "Temperature inside the device.")
		for _, t := range h.Temps {
			id, loc := t.Name, ""
			if mm := reSensor.FindStringSubmatch(t.Name); mm != nil {
				id, loc = mm[1], mm[2]
			}
			if t.State != nil {
				hn.set(b2f(*t.State == "Normal"), "component", "temperature_"+componentName(id))
			}
			if t.Celsius != nil {
				tc.set(*t.Celsius, "sensor", id, "location", loc)
			}
		}
		for name, st := range map[string]*string{"fan": h.Fan, "oscillator": h.Oscillator, "rtc_battery": h.RTCBattery} {
			if st != nil {
				hn.set(b2f(strings.HasPrefix(*st, "Normal")), "component", name)
			}
		}
		sort.SliceStable(hn.samples, func(i, j int) bool { return hn.samples[i].labels[1] < hn.samples[j].labels[1] })
	}

	// ---- クライアント
	if s.Clients != nil {
		m.add("ts2560_ntp_clients", "gauge", "Distinct client addresses in the device's NTP traffic table (max 1000, reset when ntpd restarts).").
			set(float64(len(s.Clients)))
	}
	if n := len(s.Requests); n > 0 {
		r := s.Requests[n-1]
		m.add("ts2560_ntp_requests_last_hour", "gauge", "NTP requests in the latest (possibly still running) hour of the device's packet statistics (ntphist.csv).").
			set(float64(r.Count))
	}

	var b strings.Builder
	m.write(&b)
	return b.String()
}

func registerMetrics(mux *http.ServeMux, col *Collector) {
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(buildMetrics(col.View())))
	})
}
