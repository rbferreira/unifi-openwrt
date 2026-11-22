// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The AirPort has no syslogd and no logread. What the daemon can honestly offer
// a remote syslog target besides its own log lines is the log of each hostapd it
// runs: apRunDir/VAP.log (airport_apply.go starts hostapd with -F). LogTail
// reads those files a piece at a time, by byte offset, bounded.
//
// Measured: nothing yet on the unit for this script (tail -c +N and ls -l are
// the NetBSD forms; see the device notes on the userland, which has no grep,
// awk, head, tr or cut).

// apLogMax bounds what one pass reads from one log, in 1 KiB blocks (the same
// number as apLogBlocks, which goes into the script).
const (
	apLogMax    = 32
	apLogBlocks = "32"
)

// Every header line is preceded by a newline of the script's own, so that a log
// piece that stops in the middle of a line is not glued to the next header; the
// reader takes that one newline off again.
//
// apLogHead lists the logs of the daemon's VAPs. For each one it knows an
// offset of (o_VAP=N, set above this text by AirPortLogScript) it prints the
// text from there on, at most apLogMax KiB; for one it does not know it prints
// only the size, so the reader can start at the end. A log that is shorter than
// the offset was made again (the VAP restarted) and is read from its start.
const apLogHead = `for f in $D/wlan1?.log $D/wlan2?.log; do
	[ -f "$f" ] || continue
	v=${f##*/}; v=${v%.log}
	case $v in wlan1[0-7]|wlan2[0-7]) ;; *) continue;; esac
	set -- $(ls -l $f 2>/dev/null)
	sz=$5
	case $sz in ""|*[!0-9]*) continue;; esac
	eval o=\$o_$v
	if [ -z "$o" ]; then
		printf '\n=== %s %s -\n' $v $sz
		continue
	fi
	[ $sz -lt $o ] && o=0
	printf '\n=== %s %s %s\n' $v $sz $o
	[ $sz -gt $o ] && tail -c +$((o + 1)) $f | dd bs=1024 count=` + apLogBlocks + ` 2>/dev/null
done
exit 0
`

// LogTail remembers how far each hostapd log has been read.
type LogTail struct {
	off map[string]int64
}

// Script is the script that reads what is new in the logs.
func (t *LogTail) Script() string {
	var b strings.Builder
	b.WriteString(apPATH + "D=" + apRunDir + "\n")
	var names []string
	for v := range t.off {
		names = append(names, v)
	}
	sort.Strings(names)
	for _, v := range names {
		if apVAPName.MatchString(v) { // the name is in the script as a variable name
			fmt.Fprintf(&b, "o_%s=%d\n", v, t.off[v])
		}
	}
	b.WriteString(apLogHead)
	return b.String()
}

// LogLine is one line of one VAP's hostapd log.
type LogLine struct {
	VAP  string
	Text string
}

var apLogHeader = regexp.MustCompile(`^=== (wlan(?:1[0-7]|2[0-7])) (\d+) (\d+|-)$`)

// Take reads the script's output and returns the new complete lines. A log seen
// for the first time is not replayed: reading starts at its end. A piece that
// ends in the middle of a line leaves that line for the next pass, unless it
// is as long as a pass can read (then it is taken, to not stall on a log with
// no newlines).
func (t *LogTail) Take(out []byte) []LogLine {
	if t.off == nil {
		t.off = map[string]int64{}
	}
	var lines []LogLine
	seen := map[string]bool{}
	var vap string
	var from int64
	var body bytes.Buffer
	flush := func() {
		if vap == "" {
			return
		}
		data := body.Bytes()
		end := bytes.LastIndexByte(data, '\n') + 1
		if end == 0 && len(data) >= apLogMax*1024-1024 {
			end = len(data)
		}
		for _, l := range strings.Split(strings.TrimRight(string(data[:end]), "\n"), "\n") {
			if l = strings.TrimRight(l, "\r"); strings.TrimSpace(l) != "" {
				lines = append(lines, LogLine{vap, l})
			}
		}
		t.off[vap] = from + int64(end)
		body.Reset()
	}
	parts := strings.Split(string(out), "\n=== ")
	for _, part := range parts[1:] {
		head, rest, _ := strings.Cut(part, "\n")
		if m := apLogHeader.FindStringSubmatch("=== " + head); m != nil {
			flush()
			vap = m[1]
			seen[vap] = true
			size, _ := strconv.ParseInt(m[2], 10, 64)
			if m[3] == "-" { // new to us: start at its end
				t.off[vap] = size
				vap = ""
				continue
			}
			from, _ = strconv.ParseInt(m[3], 10, 64)
			body.WriteString(rest)
			continue
		}
		if vap != "" { // not one of our headers: it was inside the log's text
			body.WriteString("\n=== " + part)
		}
	}
	flush()
	for v := range t.off { // a log that is gone starts from its beginning when it comes back
		if !seen[v] {
			delete(t.off, v)
		}
	}
	return lines
}

// ReadLogs reads what is new in the hostapd logs of the daemon's VAPs.
func (cl *Client) ReadLogs(t *LogTail) ([]LogLine, error) {
	if !cl.IsAirPort() {
		return nil, errors.New("not an AirPort base station")
	}
	out, err := cl.runStdin("sh", []byte(t.Script()))
	if err != nil {
		return nil, fmt.Errorf("airport logs: %w", err)
	}
	return t.Take(out), nil
}
