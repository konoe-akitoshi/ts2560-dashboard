package main

import (
	"encoding/binary"
	"net"
	"strings"
	"time"
)

const ntpEpoch = 2208988800 // 1900-01-01 から 1970-01-01 までの秒数

// NTPProbe は NTP(UDP 123) で問い合わせた結果。Offset はこのサーバーの時計に対する秒。
type NTPProbe struct {
	Stratum int     `json:"stratum"`
	Leap    int     `json:"leap"`
	RefID   string  `json:"refid"`
	Offset  float64 `json:"offset"`
	RTT     float64 `json:"rtt"`
}

// ntpQuery は tries 回問い合わせて、往復時間がいちばん短かった応答を返す。応答がなければ nil。
func ntpQuery(host string, timeout time.Duration, tries int) *NTPProbe {
	var best *NTPProbe
	for i := 0; i < tries; i++ {
		r := ntpOnce(host, timeout)
		if r != nil && (best == nil || r.RTT < best.RTT) {
			best = r
		}
	}
	return best
}

func ntpOnce(host string, timeout time.Duration) *NTPProbe {
	conn, err := net.DialTimeout("udp", net.JoinHostPort(host, "123"), timeout)
	if err != nil {
		return nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	req := make([]byte, 48)
	req[0] = 0x23 // LI=0, VN=4, Mode=3（クライアント）
	t0 := time.Now()
	if _, err := conn.Write(req); err != nil {
		return nil
	}
	d := make([]byte, 512)
	n, err := conn.Read(d)
	t3 := time.Now()
	if err != nil || n < 48 {
		return nil
	}
	ts := func(b []byte) float64 {
		return float64(binary.BigEndian.Uint32(b[:4])) - ntpEpoch + float64(binary.BigEndian.Uint32(b[4:8]))/(1<<32)
	}
	t1, t2 := ts(d[32:40]), ts(d[40:48])
	f0, f3 := unix(t0), unix(t3)

	refid := strings.TrimRight(string(d[12:16]), "\x00")
	if d[1] > 1 {
		refid = net.IP(d[12:16]).String()
	}
	return &NTPProbe{
		Stratum: int(d[1]),
		Leap:    int(d[0] >> 6),
		RefID:   refid,
		Offset:  ((t1 - f0) + (t2 - f3)) / 2,
		RTT:     f3 - f0,
	}
}

func unix(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }
