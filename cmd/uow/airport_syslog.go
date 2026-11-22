// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/antoniocosta/unifi-openwrt/internal/device"
	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// Remote syslog from an AirPort. The base station has no syslogd and no logread,
// so the OpenWrt forwarder (syslogForwarder) has nothing to read there. When the
// controller's system_cfg asks for remote syslog the daemon sends, from the host
// it runs on, what it can honestly produce:
//   - its own log lines (the log package's output, tapped by syslogTap), and
//   - the new lines of the hostapd log of each VAP it runs (apRunDir/VAP.log,
//     read by byte offset through the AirPort's shell, openwrt.LogTail).
//
// The base station's own log (ACP's ring, `acp logm`) is not forwarded. The
// datagrams are RFC 3164 lines over UDP; for the controller's encrypted intake
// (syslog.remote.encrypt) the same lines are sealed like the OpenWrt forwarder's.

const (
	airportSyslogEvery = 15 * time.Second // how often the logs are looked at
	syslogTapMax       = 500              // daemon lines kept between two looks
	syslogMaxLine      = 1024             // RFC 3164: a packet is at most 1024 bytes
)

// logTap is the log package's output: everything goes to w, and, while on, a copy
// of each line is kept for the forwarder (bounded: the oldest is dropped).
type logTap struct {
	w       io.Writer
	on      atomic.Bool
	mu      sync.Mutex
	q       []tapLine
	dropped int
}

type tapLine struct {
	t   time.Time
	msg string
}

var syslogTap = &logTap{w: os.Stderr}

// stdLogPrefix is the width of log.Ldate|log.Ltime ("2006/01/02 15:04:05 ").
const stdLogPrefix = 20

func (l *logTap) Write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	if !l.on.Load() {
		return n, err
	}
	now := time.Now()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if len(line) > stdLogPrefix && line[4] == '/' && line[7] == '/' {
			line = line[stdLogPrefix:]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		l.mu.Lock()
		if len(l.q) >= syslogTapMax {
			l.q = l.q[1:]
			l.dropped++
		}
		l.q = append(l.q, tapLine{now, line})
		l.mu.Unlock()
	}
	return n, err
}

// take hands over the kept lines and how many were dropped since the last take.
func (l *logTap) take() ([]tapLine, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	q, d := l.q, l.dropped
	l.q, l.dropped = nil, 0
	return q, d
}

// set switches the copy on or off; off forgets what was kept.
func (l *logTap) set(on bool) {
	l.on.Store(on)
	if !on {
		l.take()
	}
}

// syslogSeverity is the RFC 3164 severity of one of our lines: err for a failure
// the daemon or hostapd reports in words, info for the rest.
func syslogSeverity(msg string) int {
	m := strings.ToLower(msg)
	if strings.Contains(m, "fail") || strings.Contains(m, "error") {
		return 3
	}
	return 6
}

// rfc3164 builds "<PRI>Mmm dd hh:mm:ss HOST PREFIXTAG: MSG": facility daemon,
// the severity given, the message made one printable line and the whole line cut
// to 1024 bytes on a character boundary. prefix is "" or text that goes before
// the tag (the encrypted intake's "MODEL,mac,version: ").
func rfc3164(sev int, t time.Time, host, prefix, tag, msg string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, msg)
	host = strings.ReplaceAll(strings.TrimSpace(host), " ", "-")
	if host == "" {
		host = "-"
	}
	line := fmt.Sprintf("<%d>%s %s %s%s: %s", 3*8+sev, t.Format(time.Stamp), host, prefix, tag, clean)
	if len(line) > syslogMaxLine {
		line = line[:syslogMaxLine]
		for !utf8.ValidString(line) { // not in the middle of a character
			line = line[:len(line)-1]
		}
	}
	return line
}

// sysLine is one line waiting to be sent.
type sysLine struct {
	t        time.Time
	tag, msg string
	sev      int
}

// airportSyslog follows the controller's remote syslog setting and forwards the
// daemon's own log and the hostapd logs. It reads system_cfg.txt only when the
// file changes, and the unit only while forwarding is on.
func airportSyslog(x *device.Session, ap *openwrt.Client, stateDir string) {
	cfgPath := filepath.Join(stateDir, "system_cfg.txt")
	var (
		want, cur     translate.RemoteSyslog
		wantOK, curOK bool
		mtime         time.Time
		size          int64 = -1
		conn          net.Conn
		tail          openwrt.LogTail
		tokens, last  = 100.0, time.Now()
		sendFailed    bool
		readFailed    bool
		dropNote      time.Time
	)
	pid := fmt.Sprintf("uow[%d]", os.Getpid())
	for ; ; time.Sleep(airportSyslogEvery) {
		if fi, err := os.Stat(cfgPath); err == nil && (!fi.ModTime().Equal(mtime) || fi.Size() != size) {
			if b, err := os.ReadFile(cfgPath); err == nil {
				mtime, size = fi.ModTime(), fi.Size()
				want, wantOK = translate.RemoteSyslogOf(translate.ParseCfg(string(b)))
			}
		}
		if wantOK != curOK || want != cur {
			if conn != nil {
				conn.Close()
				conn = nil
				syslogTap.set(false)
				log.Printf("SYSLOG: remote syslog to %s stopped", cur.Addr)
			}
			curOK, cur = false, translate.RemoteSyslog{}
			if wantOK {
				c, err := net.Dial("udp", want.Addr)
				if err != nil {
					log.Printf("SYSLOG: %v (trying again)", err)
					continue
				}
				conn, curOK, cur = c, true, want
				tail, sendFailed, readFailed = openwrt.LogTail{}, false, false
				syslogTap.set(true)
				kind := "plain UDP"
				if cur.Encrypted {
					kind = "UniFi-encrypted"
				}
				log.Printf("SYSLOG: remote syslog to %s (%s): sending the daemon's own log and the hostapd logs of its VAPs; the base station's own log is not forwarded", cur.Addr, kind)
			}
		}
		if conn == nil {
			continue
		}

		var lines []sysLine
		mine, dropped := syslogTap.take()
		for _, l := range mine {
			lines = append(lines, sysLine{l.t, pid, l.msg, syslogSeverity(l.msg)})
		}
		if dropped > 0 && time.Since(dropNote) > 5*time.Minute {
			dropNote = time.Now()
			log.Printf("SYSLOG: %d log line(s) dropped before they could be sent", dropped)
		}
		logs, err := ap.ReadLogs(&tail)
		switch {
		case err != nil && !readFailed:
			readFailed = true
			log.Printf("SYSLOG: reading the hostapd logs: %v (further errors not logged until it works again)", err)
		case err == nil:
			readFailed = false
		}
		now := time.Now()
		for _, l := range logs {
			msg := l.Text
			if !strings.HasPrefix(msg, l.VAP) {
				msg = l.VAP + ": " + msg
			}
			lines = append(lines, sysLine{now, "hostapd", msg, syslogSeverity(msg)})
		}

		prefix := ""
		if cur.Encrypted {
			prefix = fmt.Sprintf("%s,%s,%s: ", x.Dev.Model, strings.ReplaceAll(strings.ToLower(x.Dev.MAC.String()), ":", ""), x.S.Version)
		}
		for _, l := range lines {
			t := time.Now()
			tokens = min(100, tokens+t.Sub(last).Seconds()*50) // 50/s, bursts of 100, as the OpenWrt forwarder
			last = t
			if tokens < 1 {
				continue
			}
			tokens--
			b := []byte(rfc3164(l.sev, l.t, x.Dev.Hostname, prefix, l.tag, l.msg))
			var serr error
			if cur.Encrypted {
				b, serr = device.SealSyslog(cur.Key, x.S.HashID, b)
			}
			if serr == nil {
				_, serr = conn.Write(b)
			}
			if serr != nil && !sendFailed {
				sendFailed = true
				log.Printf("SYSLOG: send: %v (further errors not logged)", serr)
			}
		}
	}
}
