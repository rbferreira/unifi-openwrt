// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"reflect"
	"strings"
	"testing"
)

func TestNTPServers(t *testing.T) {
	c := ParseCfg("ntpclient.status=enabled\nntpclient.1.status=enabled\nntpclient.1.server=0.ntp.example\nntpclient.2.status=disabled\nntpclient.2.server=off.example\nntpclient.3.server=2.ntp.example\n")
	if got := NTPServers(c); !reflect.DeepEqual(got, []string{"0.ntp.example", "2.ntp.example"}) {
		t.Errorf("servers: %q", got)
	}
	if got := NTPServers(ParseCfg("ntpclient.status=disabled\nntpclient.1.server=x\n")); got != nil {
		t.Errorf("client off: %q", got)
	}
}

func TestRemoteSyslogOf(t *testing.T) {
	key := strings.Repeat("ab", 32)
	for _, c := range []struct {
		name, cfg string
		want      RemoteSyslog
		ok        bool
	}{
		{"off", "syslog.status=disabled\nsyslog.remote.status=enabled\nsyslog.remote.ip=192.0.2.9\n", RemoteSyslog{}, false},
		{"remote off", "syslog.status=enabled\nsyslog.remote.status=disabled\nsyslog.remote.ip=192.0.2.9\n", RemoteSyslog{}, false},
		{"no address", "syslog.status=enabled\nsyslog.remote.status=enabled\n", RemoteSyslog{}, false},
		{"plain, default port", "syslog.status=enabled\nsyslog.remote.status=enabled\nsyslog.remote.ip=192.0.2.9\n", RemoteSyslog{Addr: "192.0.2.9:514"}, true},
		{"plain, port", "syslog.status=enabled\nsyslog.remote.status=enabled\nsyslog.remote.ip=192.0.2.9\nsyslog.remote.port=1514\n", RemoteSyslog{Addr: "192.0.2.9:1514"}, true},
		{"encrypted", "syslog.status=enabled\nsyslog.remote.status=enabled\nsyslog.remote.ip=192.0.2.9\nsyslog.remote.encrypt=enabled\nsyslog.remote.key=" + key + "\n", RemoteSyslog{Addr: "192.0.2.9:5514", Encrypted: true, Key: key}, true},
		{"encrypted, short key", "syslog.status=enabled\nsyslog.remote.status=enabled\nsyslog.remote.ip=192.0.2.9\nsyslog.remote.encrypt=enabled\nsyslog.remote.key=abcd\n", RemoteSyslog{}, false},
	} {
		got, ok := RemoteSyslogOf(ParseCfg(c.cfg))
		if ok != c.ok || got != c.want {
			t.Errorf("%s: %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
