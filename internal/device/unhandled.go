// SPDX-License-Identifier: GPL-2.0-or-later

package device

// The ledger of what the controller sent and nothing here acted on: reply
// types, commands, reply fields, mgmt_cfg keys and system_cfg sections. It
// is a debugging aid: the file shows where this daemon still has gaps on a
// given controller version, without a packet capture. Values are kept short
// and anything named like a secret is never written.

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Unhandled is one thing the controller sent that was not acted on.
type Unhandled struct {
	Kind   string `json:"kind"` // reply, cmd, field, mgmt_cfg, system_cfg
	Name   string `json:"name"`
	Count  int    `json:"count"`
	First  string `json:"first_seen"`
	Last   string `json:"last_seen"`
	Sample string `json:"sample,omitempty"` // first one seen, secrets removed
}

const (
	ledgerMax    = 100       // entries; the one seen longest ago goes first
	ledgerSample = 300       // bytes of a sample
	ledgerEvery  = time.Hour // repeat counts reach the file this often
)

// Ledger collects Unhandled entries and keeps them in a file (mode 600).
// A nil *Ledger is valid and records nothing.
type Ledger struct {
	mu      sync.Mutex
	path    string
	m       map[string]*Unhandled
	dirty   bool
	written time.Time
}

// OpenLedger reads the ledger at path, or starts an empty one.
func OpenLedger(path string) *Ledger {
	l := &Ledger{path: path, m: map[string]*Unhandled{}}
	var list []*Unhandled
	if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, &list) == nil {
		for _, u := range list {
			if u != nil && u.Kind != "" && len(l.m) < ledgerMax {
				l.m[u.Kind+"\x00"+u.Name] = u
			}
		}
	}
	return l
}

// Note records one sighting and reports whether it is the first of its
// kind and name. A new entry is written at once, a repeat only now and then
// (the file may sit on flash).
func (l *Ledger) Note(kind, name, sample string, now time.Time) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ts := now.UTC().Format(time.RFC3339)
	u, seen := l.m[kind+"\x00"+name]
	if !seen {
		if len(l.m) >= ledgerMax {
			l.evict()
		}
		if len(sample) > ledgerSample {
			sample = sample[:ledgerSample] + "..."
		}
		u = &Unhandled{Kind: kind, Name: name, First: ts, Sample: sample}
		l.m[kind+"\x00"+name] = u
	}
	u.Count++
	u.Last = ts
	l.dirty = true
	if !seen || now.Sub(l.written) >= ledgerEvery {
		l.write(now)
	}
	return !seen
}

// Entries returns a copy of the ledger, sorted by kind and name.
func (l *Ledger) Entries() []Unhandled {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Unhandled, 0, len(l.m))
	for _, u := range l.m {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Flush writes pending repeat counts (call before exit).
func (l *Ledger) Flush() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dirty {
		l.write(time.Now())
	}
}

func (l *Ledger) evict() {
	var oldest string
	for k, u := range l.m {
		if oldest == "" || u.Last < l.m[oldest].Last {
			oldest = k
		}
	}
	delete(l.m, oldest)
}

// write stores the ledger through a temporary file. Callers hold l.mu.
func (l *Ledger) write(now time.Time) {
	l.written, l.dirty = now, false
	if l.path == "" {
		return
	}
	list := make([]*Unhandled, 0, len(l.m))
	for _, u := range l.m {
		list = append(list, u)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			return list[i].Kind < list[j].Kind
		}
		return list[i].Name < list[j].Name
	})
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	tmp := l.path + ".tmp"
	if os.WriteFile(tmp, append(b, '\n'), 0o600) == nil {
		os.Rename(tmp, l.path)
	}
}

// secretName: field and key names whose values are never recorded. Broad on
// purpose: a hidden harmless value costs a second look, a recorded
// passphrase cannot be taken back.
var secretName = regexp.MustCompile(`(?i)psk|pass|secret|token|private|key|auth|cert|hash`)

// redactValue is the sample kept for a named value.
func redactValue(name, v string) string {
	if secretName.MatchString(name) {
		return "<hidden>"
	}
	return MaskHex(v)
}

// redactReply is a reply as a sample: no configuration bodies (they carry
// the Wi-Fi passphrase and the device key), no secret-named fields, at any
// depth.
func redactReply(js []byte) string {
	var v any
	if json.Unmarshal(js, &v) != nil {
		return ""
	}
	b, err := json.Marshal(redactAny("", v))
	if err != nil {
		return ""
	}
	return MaskHex(string(b))
}

func redactAny(name string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = redactAny(k, e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = redactAny(name, e)
		}
		return t
	}
	if name == "mgmt_cfg" || name == "system_cfg" || strings.HasSuffix(name, "_cfg") || secretName.MatchString(name) {
		return "<hidden>"
	}
	return v
}

// replyFields are the reply fields this daemon reads.
var replyFields = map[string]bool{"_type": true, "interval": true, "immediate": true, "cmd": true, "key": true,
	"uri": true, "version": true, "url": true, "cfgversion": true, "mgmt_cfg": true, "system_cfg": true,
	"blocked_sta": true, "mac": true, "minutes": true, "end": true,
	"server_time_in_utc": true,
	// sent with every cmd, and the fields of the commands refused on purpose
	"device_id": true, "time": true, "datetime": true, "_id": true,
	"token": true, "pinned_pubkey": true, "upload_url": true,
	"uuid": true, "ttl": true, "stun": true, "turn": true, "username": true, "password": true,
	"sdpanswer": true, "shellcmd": true}

// mgmtKeys are the mgmt_cfg keys this daemon reads.
var mgmtKeys = map[string]bool{"cfgversion": true, "authkey": true, "use_aes_gcm": true, "inform_url": true, "led_enabled": true}

// noteUnread records the reply fields nothing reads.
func (x *Session) noteUnread(js []byte, typ string, now time.Time, o *Outcome) {
	if x.Unhandled == nil {
		return
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(js, &raw) != nil {
		return
	}
	for k, v := range raw {
		if replyFields[k] {
			continue
		}
		sample := "<hidden>"
		if !secretName.MatchString(k) && !strings.HasSuffix(k, "_cfg") {
			var a any
			if json.Unmarshal(v, &a) == nil {
				if b, err := json.Marshal(redactAny(k, a)); err == nil {
					sample = MaskHex(string(b))
				}
			}
		}
		x.note("field", typ+"."+k, sample, now, o)
	}
}

// note records one unhandled thing and logs the first sighting.
func (x *Session) note(kind, name, sample string, now time.Time, o *Outcome) {
	if x.Unhandled.Note(kind, name, sample, now) {
		o.Events = append(o.Events, "unhandled "+kind+" "+name+" (first time; listed in unhandled.json)")
	}
}
