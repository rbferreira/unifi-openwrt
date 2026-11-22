// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "net"

// NTPServers is the NTP servers the controller asks for (ntpclient section), in
// its order. Empty when the NTP client is off or names none.
func NTPServers(cfg Cfg) []string {
	if cfg["ntpclient.status"] != "enabled" {
		return nil
	}
	var servers []string
	for _, n := range indices(cfg, "ntpclient") {
		if cfg["ntpclient."+n+".status"] != "disabled" && cfg["ntpclient."+n+".server"] != "" {
			servers = append(servers, cfg["ntpclient."+n+".server"])
		}
	}
	return servers
}

// RemoteSyslog is the controller's remote syslog target.
type RemoteSyslog struct {
	Addr      string // host:port
	Encrypted bool   // the controller's own encrypted intake (UDP 5514); Key is then set
	Key       string // 64 hex digits, only with Encrypted
}

// RemoteSyslogOf reads the syslog section: ok is false when remote syslog is
// off, has no address, or is the encrypted kind without a usable key. A plain
// target defaults to port 514, the encrypted one to 5514.
func RemoteSyslogOf(cfg Cfg) (RemoteSyslog, bool) {
	if cfg["syslog.status"] != "enabled" || cfg["syslog.remote.status"] != "enabled" || cfg["syslog.remote.ip"] == "" {
		return RemoteSyslog{}, false
	}
	r := RemoteSyslog{Encrypted: cfg["syslog.remote.encrypt"] == "enabled"}
	port := cfg["syslog.remote.port"]
	if r.Encrypted {
		if len(cfg["syslog.remote.key"]) != 64 {
			return RemoteSyslog{}, false
		}
		r.Key = cfg["syslog.remote.key"]
		if port == "" {
			port = "5514"
		}
	} else if port == "" {
		port = "514"
	}
	r.Addr = net.JoinHostPort(cfg["syslog.remote.ip"], port)
	return r, true
}
