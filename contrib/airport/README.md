# An Apple AirPort Time Capsule as a UniFi mesh AP

How an AirPort Time Capsule A1470 (802.11ac, firmware 7.9.1) was made to join a UniFi
network as a wireless (mesh) child of a UniFi AP, serve WLANs on both bands, and show up in
the UniFi console through `uow -ap-platform airport`. This directory is the kit and the
runbook to repeat it on another unit.

It is a different thing from the rest of this project. An AirPort does not run OpenWrt: the
daemon runs on the base station itself, built with a patched Go toolchain ([go/](go/README.md)).
With `-apply` the daemon owns the access-point side of both radios and does by itself what an
OpenWrt AP gets from uci, cron, usteer and logread, because the base station has none of
those. Some of it was seen working on the unit, some is built and not yet exercised there, and
some the hardware cannot do: [what the console configures](#what-the-console-configures) says
which, feature by feature.

## What you get

| | |
|---|---|
| Uplink | 5 GHz client on the parent UniFi AP's mesh backhaul SSID (`vwire-…`), bridged at layer 2: hosts behind the AirPort keep their own MAC addresses |
| WLANs | the ones the console assigns to the AP, up to 8 per band, open or WPA2 personal: one hostapd each, started by the daemon |
| LAN ports | bridged to the uplink |
| In the console | adopted as a UAP-AC-Pro (`U7PG2`): radios, WLANs, clients with signal, rates, bytes and packets, packet, retry and error counters per radio, the wireless uplink and its parent, ports, CPU and memory, the neighbour scan; Restart, Reconnect, Block and Locate work |
| From the console to the unit | channel and width, transmit power, DTIM, client limit, hidden SSID, MAC filters and blocked clients, client isolation, schedules, an automatic channel, remote syslog: see the table below for the state of each |
| Depends on | nothing but the base station: no second host |
| Survives | reboot and power loss: back on the mesh about 2 minutes after power-on, with the WLANs rebuilt from the last configuration kept on flash |

Measured, one hop from a U7 Lite at -27 dBm (uplink PHY rate 866 Mbit/s), downloading from a
wired host over SSH, so the numbers compare paths rather than state a ceiling:

| Client path | Link | Throughput |
|---|---|---|
| Cable on a LAN port | 1 Gbit/s | 141 to 159 Mbit/s |
| Wi-Fi, 5 GHz (same radio as the uplink) | 802.11ac, 866.7 Mbit/s | 71 Mbit/s |
| Wi-Fi, 2.4 GHz | 802.11n, 144.4 Mbit/s | 60 Mbit/s |

## The base station

| | Measured on the A1470 |
|---|---|
| System | NetBSD 6.0 evbarm (Apple build), ARM EABI binaries, one static crunched userland |
| CPU / RAM | Broadcom BCM53018, Cortex-A9 1 GHz, 256 MB |
| Radios | 2x Broadcom BCM4360, `wl` driver 6.30.229: `bwl0` = 5 GHz, `bwl1` = 2.4 GHz, with net80211-style VAPs (`wlanN`) on top |
| Storage | `/mnt/Flash` (2.5 MB, persistent), `/mnt/Memory` (16 MB, RAM); `/` is a RAM disk |
| Userland | `sh sed sort tail cat ps ifconfig brconfig wl netstat arp sysctl envstat acp tcpdump curl pfctl`; no `grep`, `awk`, `head`, `tr`, `xargs`, `cut`, `touch`, `syslogd`, `snmpd` or `lldpd`; `PATH` over SSH is only `/bin` |
| Boot hook | `/etc/rc.local` is a link to `/mnt/Flash/rc.local`, sourced by `/etc/rc.d/local` at every boot |

Other models are not covered. The 802.11n ones are big-endian ARM or MIPS on NetBSD 4 and
have other radios; see [TDSOJohn/airport-express-tools](https://github.com/TDSOJohn/airport-express-tools),
which this builds on, for the A1392.

## Steps

Work over a cable on a LAN port until the uplink is proven.

### 1. Root shell

Factory-reset the unit if its admin password is unknown (unplug, hold reset, plug in, hold
about 6 s until the light flashes amber fast). It then answers ACP at `10.0.1.1` with the
password `public`.

With [airportctl](https://github.com/TDSOJohn/airport-express-tools) (Python, no dependencies),
set the debug bit and reboot. sshd comes up with `root` and the admin password:

```sh
python3 -c "import struct; from airportctl import acp; \
  acp.set_props('10.0.1.1','public',[('dbug',struct.pack('>I',0x3000))]); acp.reboot('10.0.1.1','public')"
```

The server is OpenSSH 5.9; a current client needs the old algorithms:

```sh
ssh -o HostKeyAlgorithms=+ssh-rsa -o KexAlgorithms=+diffie-hellman-group14-sha1 \
    -o Ciphers=+aes128-cbc,3des-cbc -o PubkeyAuthentication=no root@10.0.1.1
```

There is no sftp: copy files with `ssh … 'cat > /path' < file`. **Change the admin password**
(`airportctl admin-password`) before the unit is on a network you do not control alone: the
debug shell is root with that password.

### 2. Build

On Linux (WSL works), with `gcc-arm-linux-gnueabi` and `xz`:

```sh
cd build
./setup.sh         # NetBSD 7.0 evbarm-earm headers and libc, wpa_supplicant 0.7.3 (checksummed)
./build-wpa.sh     # out/wpa_supplicant, about 320 KB
./build-unxz.sh    # out/unxz, about 250 KB
```

and the daemon with the patched Go toolchain, then packed for the flash:

```sh
# see go/README.md for GOROOT
GOROOT=... GOTOOLCHAIN=local GOOS=netbsd GOARCH=arm GOARM=7 CGO_ENABLED=0 \
    go build -trimpath -ldflags='-s -w' -o uow-netbsd-arm ./cmd/uow     # about 9 MB
build/pack-uow.sh uow-netbsd-arm                                       # out/uow.blob, out/uow.meta.new
```

The firmware's own `wpa_supplicant` (0.6.5) cannot be used: started by hand it does nothing
and logs nothing, and started by the firmware's "join a network" mode it dies at startup.

What had to change against stock headers for the C programs, all in `build/compat/`:

| Mismatch | Fix |
|---|---|
| `struct if_msghdr` is 160 bytes on this kernel, 152 in NetBSD 7: `getifaddrs()` returns empty names and `if_nametoindex()` fails | `compat/net/if.h`, and NetBSD's `getifaddrs.c` rebuilt against it |
| The 802.11 ioctls are FreeBSD 8's, as on the A1392 | `compat/net80211/ieee80211_ioctl.h`, unchanged from airport-express-tools |

### 3. Stored configuration (over ACP)

Three things, then a reboot. With `airportctl` (it refuses writes on this model without
`--untested`; every Wi-Fi write is backed up first):

1. **Bridge mode**: `airportctl mode sharing bridge --untested`. In router mode the unit runs
   a DHCP server on its LAN bridge, and bridging the uplink into that would leak it onto the
   network. `up.sh` refuses to start the uplink while `dhcpd` runs.
2. **Both radios off** in the `WiFi` blob (`raSt = 3` on each), so the firmware runs no
   hostapd and leaves `bwl0` and `bwl1` to the kit.
3. Before that, while the firmware still runs a WLAN of its own (it does after a factory
   reset), **save the unit's regulatory ids**: the `sku=` and `region=` lines of
   `/etc/hostap_wlan0.conf` go into `/mnt/Flash/mesh/hostap.seed`. `up.sh` does it by itself
   when it first runs with that file still there. Without the seed the daemon applies nothing
   (every hostapd config of the unit carries those two ids, and only the unit knows them).

In bridge mode the unit takes its address by DHCP on `bridge0` and no longer answers at
`10.0.1.1`; over the cable it is still reachable at its IPv6 link-local address.

### 4. Install the kit

```sh
mkdir -p /mnt/Flash/mesh          # on the base station
```

| File | To | |
|---|---|---|
| `build/out/wpa_supplicant` | `/mnt/Flash/mesh/wpa_supplicant` | |
| `build/out/unxz` | `/mnt/Flash/mesh/unxz` | mode 755 |
| `up.sh` | `/mnt/Flash/mesh/up.sh` | |
| `wpa.conf` | `/mnt/Flash/mesh/wpa.conf` | from `wpa.conf.example`: the parent's backhaul SSID and its PMK; mode 600 |
| `uow.conf` | `/mnt/Flash/mesh/uow.conf` | from `uow.conf.example`: inform URL, UniFi model, name |
| `rc.local` | `/mnt/Flash/rc.local` | mode 644 |
| `build/out/uow.blob`, `uow.meta.new`, `flash-uow.sh` | `/mnt/Memory/` | then `sh /mnt/Memory/flash-uow.sh` |

The backhaul SSID and key are what the console hands a UniFi mesh child; an OpenWrt AP
already meshing through this project has them in `/etc/unifi-openwrt/mesh.conf`. The uplink
is configured by this file and by `up.sh`: the daemon does not take mesh credentials from
the console.

`rc.local` is **sourced** by the boot script: it must never `exit` or block. Everything in
it runs in a background subshell.

**Where the daemon lives.** `/mnt/Flash` is 2.5 MB and the binary is 9 MB (2.5 MB as xz).
The flash has two 14 MB firmware banks: `flash0` with the running firmware and `flash1`
with the factory one. In both the image ends near 8.2 MB and the rest, up to a trailer in
the last 64 KB sector, is erased. `flash-uow.sh` writes the compressed binary into that
erased tail of `flash1` (from sector 144), leaving the factory image and the running bank
alone, and `up.sh` unpacks it into the RAM disk at every boot. Checked after writing: a
full read of both banks differed from the read before only in the sectors written.

**Updating the daemon.** The same script does it on a running unit: put the new
`uow.blob`, `uow.meta.new` and `flash-uow.sh` in `/mnt/Memory/` and run it. It erases and
writes the sectors by number, reads them back through `unxz` and compares the checksum from
the meta file, and only then touches the running daemon. The RAM disk holds one copy of the
binary, not two, so the script removes the old file, stops the old daemon and *waits until it
has exited*, and unpacks the new binary under another name (`uow.new`), checking its size,
before moving it to `uow`; `up.sh` starts it within 5 s. The order matters: an earlier
version unpacked over a daemon that was still exiting, the RAM disk filled, the copy came out
short, and the space came back only with a reboot. If the unpack fails the script says so and
leaves the image on flash, which is good: reboot, and `up.sh` unpacks it. The blob is
deleted from `/mnt/Memory` as soon as it is written, for the same reason.

The daemon's state directory is in RAM as well. `state.json`, the adoption SSH keys and the
controller's last configuration (`system_cfg.txt`, with the WLAN passphrases, mode 600) are
copied to `/mnt/Flash/mesh/state` when they change: the configuration history the daemon
keeps would fill the flash in time. With the configuration on flash the WLANs come up at
boot before the controller is reached.

Reboot. `up.sh` logs to `/mnt/Memory/mesh.log`:

```
up.sh start
uplink associated and in bridge0
uow started (pid 151)
```

and the daemon to `/mnt/Memory/uow.log`:

```
PLAN (applying): 3 VAP(s)
  vap wlan10 on bwl1: ssid "…", psk2, auto channel
  vap wlan11 on bwl1: ssid "…", psk2, channel of wlan10
  vap wlan20 on bwl0: ssid "…", psk2, channel 40
  note: …
  airport: wlan10 started
  airport: wlan11 started
  airport: wlan20 started
after apply: 3 of 3 wanted VAP(s) running
```

Every `note:` line is something the console asked for that the unit does not do, or does
differently, and why; read them first when a setting seems to have no effect.

### 5. Adopt

The device appears in the console as pending. Adopt it there: the console opens SSH to the
base station on port 2222, where the daemon answers the adoption.

## What `up.sh` does

It is started by `rc.local` 45 s after boot and logs to `/mnt/Memory/mesh.log`. It changes
nothing in the base station's stored configuration.

1. Refuses to go on if `dhcpd` is running (router mode).
2. Unpacks the daemon from `flash1` into the RAM disk and checks its size; restores the
   adoption state saved on flash into the daemon's RAM state directory; saves `hostap.seed`
   if it is not there yet.
3. With no link on the WAN port for 20 s: creates a client VAP on `bwl0`, starts
   `wpa_supplicant`, waits for the association, adds the VAP to `bridge0`.
4. Every 5 s: a cable with link on the WAN port removes the wireless uplink (a wired uplink
   and a wireless one together are a loop); an uplink that is not associated for 60 s is torn
   down and tried again; the daemon is started once the bridge has an address (with
   `-apply`, state directory in RAM, SSH on port 2222) and started again if it dies; the
   adoption state is copied to flash when it changes; the daemon's log is emptied when it
   passes 400 KB. Whenever the uplink is created again or has changed channel, the 5 GHz
   WLANs are taken down first (they share its radio); the daemon notices within a minute and
   starts them again on the uplink's channel.
5. The status LED is set green when the uplink is up and amber when it fails or is removed;
   the daemon then takes it over from the console's state (see the table).

The sequence that brings the client VAP up, for doing it by hand:

```sh
A=$(cat /var/run/ACPd.pid); kill -STOP $A
ifconfig wlan9 create wlandev bwl0 wlanmode sta
ifconfig wlan9 media autoselect     # without it: "SIOCSIFMEDIA: Invalid argument"
ifconfig wlan9 dwds                 # 4-address frames
ifconfig wlan9 up; ifconfig wlan9 down
./wpa_supplicant -t -D bsd -i wlan9 -c wpa.conf > wpa.log 2>&1 &
sleep 3; kill -CONT $A
```

## What the console configures

With `-apply` (which `up.sh` passes) the daemon owns the AP side of both radios. For every
WLAN the console assigns to the AP it runs one hostapd on a VAP of its own: `wlan10`..`wlan17`
on 2.4 GHz (`bwl1`), `wlan20`..`wlan27` on 5 GHz (`bwl0`), configs and logs in
`/mnt/Memory/uowap`. It never touches the uplink VAP or anything outside those names.

The base station has no cron, no uci, no usteer, no syslogd and no hostapd control
interface, so what OpenWrt does with those the daemon does itself, from the controller's own
configuration file. Four states, and the third column says what was and was not seen:

- **works**: done through the daemon and seen on the unit;
- **built**: the code is there, flashed on the unit and covered by unit tests, but the
  behaviour has not yet been seen on the unit, usually for want of a client or of the
  setting on the console;
- **not possible**: the hardware or its driver does not allow it, with the measurement;
- **not done**: nothing written; where something was measured, it is said.

| Feature | State | Detail |
|---|---|---|
| **WLANs** | | |
| WLANs per band, SSID, enable/disable | works | Up to 8 per band. Two on 2.4 GHz and one on 5 GHz came up from the configuration on flash at boot, before the controller was reached |
| WPA2 personal | works | A client authenticated and got an address on a WLAN rendered from scratch. The passphrase must be 8 to 63 characters |
| Open | built | Rendered; not tried with a client through the console |
| Hidden SSID | built | Rendered as `hide_ssid`; not tried with a client |
| WPA3, WPA2/WPA3, PMF, 802.11r, 802.11v | not possible | The Broadcom driver does authentication and association itself, so hostapd only sees events and has no management-frame path, which SAE, FT and BSS transition need. `wl` answers "Operation not supported" to `mfp`, `mfp_config`, `mfp_sha256`, `fbt`, `wnm` and `sae_password`, `wpa_auth` knows only WPA and WPA2 PSK, and `wl cap` lists none of them. The firmware's hostapd (0.6.5) rejects the keys; a hostapd 2.11 cross-built for the unit would add nothing. These WLANs are left off, never downgraded, and a required PMF is logged |
| WPA-Enterprise | not done | The WLAN is left off with a note |
| Guest networks, WLANs on a VLAN | not done | Left off with a note. A VLAN interface on top of a VAP, and on the uplink VAP, was created and destroyed without disturbing anything, and a second bridge works; no frame was sent through either, because the network under test has no VLAN. Isolating a guest network from the LAN would need `pf` on the bridge: also untried |
| **Channel and radio** | | |
| 2.4 GHz channel | works | Changed from the console to 11, to 1 and back to auto, each applied within seconds; the 5 GHz WLAN and the uplink untouched |
| 2.4 GHz width | not possible | The radio stays at 20 MHz whatever is asked: `6:ht/40` is accepted and nothing changes |
| 5 GHz channel behind the mesh uplink | works | The radio follows the uplink's channel and width; a different channel asked for is logged. The uplink coming back by itself with the WLANs behind it was seen. The parent moving to another channel has not been seen |
| 5 GHz channel and width with no mesh uplink | works | Seen with the uplink removed by hand and the daemon's copy of the configuration edited: channel 44 at 20, 40 and 80 MHz, a width-only change included. 80 MHz is the most applied. Not seen with a cable on the WAN port, which is the real case |
| Automatic channel | built | `acs=1` is not relied on (see below). From the neighbour scan the daemon picks the least-interfered of 1, 6 and 11 on 2.4 GHz, or of the non-DFS 5 GHz channels when there is no uplink, and holds it 4 hours; a held radio is moved only while it has no client. The first pass had no scan results yet; the choice itself has not been seen. Behind a mesh uplink the 5 GHz radio follows the uplink |
| Transmit power | built | `ifconfig VAP txpower N` moves the radio, not hostapd's `tx_power` (no effect) or `wl pwr_percent` (reads back, changes nothing). By hand on the 5 GHz radio with the uplink: 21 to 15 dBm, the uplink stayed associated. A value above the radio's own is cut to it. hostapd puts the default back a moment after a VAP starts, so the daemon waits 6 s after a radio restart; that wait has not been re-tested |
| DTIM, client limit | works | Rendered into the hostapd config (the console sends DTIM 1 on 2.4 GHz). Their effect on the air, and the limit with two clients, were not measured. The driver's limit per WLAN is 64 |
| Minimum RSSI | built | A client below the radio's minimum on every reading for 30 s is disconnected (the same code as on OpenWrt). Not seen: it needs a weak client. Refusing a weak client at association is not done |
| Band steering | built | "Prefer 5 GHz" only, off on the console so far. A client that has just joined on 2.4 GHz, was seen on 5 GHz, and has a good signal is kicked and denied on 2.4 GHz for 10 s, at most 3 times per 6 h. Load balancing is not steering: its client limit becomes the limit above |
| Channel utilization | not possible | `chanim_stats` panics the kernel; `cca_get_stats`, `itfr_get_stats`, `pktq_stats` and `rate_histo` are not supported. Only frame counters exist, and any percentage from them would be invented |
| Country | not possible | Read-only on this unit: the regulatory domain is fixed by the `sku` and `region` of the unit. The daemon reports it and logs a note when the console asks for another |
| **Clients** | | |
| Reconnect | works | `wl deauthenticate`; the client's time in the network dropped (141 s to 38 s) and it came back by itself |
| Block, unblock | works | The MAC joins the driver ACL of every WLAN, is kicked, and leaves the client list; unblock removes it and the client reassociated. Immediate, no restart |
| MAC filter, clients locked to another AP | works | The deny lists the console sends are applied as the driver ACL of every VAP. An allow-list filter is built, not seen |
| Client isolation | works | The driver flag is set and read back (`ap_isolate` 1, and back to 0). That it keeps two clients apart is built, not seen: it needs two wireless clients on one WLAN |
| Per-client counters | works | Signal, rates, bytes, packets and time in network, from `wl sta_info`. `tx_retries` is 0 and `tx_failed` empty: the driver has no per-station retry count, so the experience score sees the PHY rate only. Retries and errors are reported per radio, from `wl counters` |
| Neighbour scan | works | 7 networks scanned on 2.4 GHz and 9 read from the cache on 5 GHz; the console lists the neighbours. Clients stayed up through a scan. 5 GHz is never scanned while the uplink is on it: only the results the driver already holds |
| Schedules | built | The daemon leaves a WLAN out of the VAP list outside its hours, in the time zone the controller sent, and applies again when a boundary passes. No schedule exists on the console yet. With no readable time zone the WLANs stay on |
| Rate limits | not done | Measured feasible with `pf` and `altq` (cbq): a 2 Mbit queue on a VAP took a download from about 40 to 1.8 Mbit/s, and per-client limits in both directions classified packets with the same queue name on the VAP and on the uplink. But the rates are inaccurate (2 Mbit gave 3.6, 1 Mbit gave 0.9 once and 5 once) and a queue on the uplink slowed unqueued traffic through it (a wired client from 9.7 to 2.3 MB/s). Needs tuning or another discipline before any code; the daemon does not limit anything |
| **Device** | | |
| Restart | works | The VAPs are rebuilt from the saved configuration when the unit is back |
| Locate | works | From the console the LED alternates amber and green every second, and goes back |
| LED states | works | The console's steady blue or green shows as green; white, red, orange and yellow show as amber. The unit has three states only (its own, amber, green), so "LED off" is not possible: it is noted and the LED left alone |
| Temperatures and fan | built | `envstat` is read (four board sensors, the fan and its duty). The console kept `has_temperature` and `has_fan` and showed no value; the daemon now sends the scalar and the array form, not yet checked in the console |
| Ports | built | Every PHY is reported, with or without link (four seen, a client's at 100 Mbit/s). There are no counters per port, so they go to the first port with link |
| Remote syslog | works | The daemon forwards its own log and the hostapd logs of its VAPs, as RFC 3164 over UDP to the controller's target (20 lines reached the log server within 10 minutes). The unit has no syslogd, and the base station's own log ring is not forwarded. The controller's encrypted intake is built, not seen |
| NTP, time zone | not done | The unit keeps its own `sntpd` server and zone; the request is logged. The time zone is still used for schedules |
| Firmware, capability words | works | The real firmware version is reported in a field of its own (the `version` the controller upgrades from stays the model's). The WPA3 bit is cleared. The 802.11k, v and r bits are still claimed: there is no table of their positions |
| 802.11k neighbour reports | not done | The radio firmware has them (`wl rrm` lists Neighbor_Report, `rrm_nbr_list` reads) and the daemon does not use them |
| Device logins, SSH keys | not done | The login is root with the ACP admin password; changing it from the console could lock the unit out |
| SNMP | not done | There is no `snmpd` on the unit and the daemon's SNMP step is OpenWrt's only |
| Mesh credentials from the console, mesh children | not done | The uplink is `up.sh` and `wpa.conf` |
| `uow -dump`, `owctl`, `owplan`, `owprobe` | built | Take `-ap-platform airport`; not yet run against the unit. `owctl` does kick and LED there, and refuses block (the driver ACLs belong to the daemon). No beep: no sound hardware |

## How the applier works

One hostapd per WLAN, each on a VAP of its own, from a config the daemon renders in a fixed
key order (the firmware's own: this hostapd reads top to bottom and a few keys depend on
earlier ones). The passphrases go to the unit on the standard input of a shell, never on a
command line. The 5 GHz VAPs take the uplink's channel in their config, because a hostapd
that does not say so starts on channel 36 and drags the uplink along.

- **All or nothing per radio.** When any VAP of a radio has to start, stop or change, or the
  radio is on another channel or width, all of that radio's VAPs are stopped and started
  again (about 3 s). The other radio and the uplink are not touched. Every change destroys
  and creates the VAP.
- **What is not in the hostapd config is set on the live VAP**, so changing it never
  restarts a radio: the driver ACL (MAC filter, blocked clients), client isolation and the
  transmit power. The ACL is kept in a file per VAP; the script does nothing when the file
  already says what is wanted.
- **Once a minute** (`airportKeep`) the daemon checks, from its own memory and one reading of
  the unit, that what it applied is still running. It applies again when a WLAN is not
  running, when the 5 GHz uplink changed channel (`up.sh` takes the 5 GHz VAPs down around
  such a change), when a schedule boundary has passed, or when an automatic channel is due a
  second look; otherwise it only re-checks the ACLs. A configuration push from the controller
  applies at once.
- **An empty configuration is refused.** `system_cfg.txt` with no radio section (any index) is
  an empty or cut-short file, not "no WLANs": applying it would take every WLAN off the air,
  as happened once with a file truncated by a full RAM disk. The daemon logs it and applies
  nothing.
- **Never outside its names.** Only `wlan10`..`wlan17` and `wlan20`..`wlan27` are created,
  stopped or destroyed, and a VAP name, parent, MAC or SSID is checked before it reaches a
  shell. There is no timed rollback as on OpenWrt: nothing the applier does can cut the
  daemon off from the controller.

Measured on the unit, and the reason for all of it:

- hostapd's `channel=`, `allow_ht40` and `allow_vht80` do not move the radio. `ifconfig VAP
  channel N` on the first VAP of an idle radio does, with the width in the same argument on
  5 GHz (`44:vht/80`, `44:ht/40`, `44:ht/20`; without it the radio came up at 40 MHz), and the
  other VAPs follow the radio. On 2.4 GHz `6:ht/40` is accepted and the radio stays at 20 MHz.
  Behind the mesh uplink the radio keeps the width the uplink negotiated.
- The VAPs of one radio are not independent: restarting one left another down with its
  hostapd still running.
- A VAP is destroyed and created again on every change: restarted on the same VAP, an open
  network kept the WPA2 state of the one before it.
- The driver's ACL per VAP (`ifconfig VAP mac:add`, `mac:deny`, `mac:allow`, `mac:open`,
  `mac:flush`, `mac:kick`) takes effect at once. hostapd's own `deny_mac_file` is read only at
  start: after SIGHUP, with the file emptied, the client was still refused. A refused client
  stays "associating" and tries another network.
- Client isolation is the VAP's `apbridge` flag (`-apbridge` isolates), per VAP.
- Transmit power is the radio's, whichever VAP it is set through.
- hostapd's `acs=1`: moving an idle 2.4 GHz radio across channels was seen once, and in the
  applier's runs the radio kept the channel it was on. The daemon chooses the channel itself.
- Other hostapd keys the parser rejects: `ieee80211w`, `sae_password`, `ap_isolate`,
  `mobility_domain`, `ieee80211r`, `ieee80211k`, `ieee80211v`, `ht_capab`, `wps_state`,
  `bssid`, among others. `ctrl_interface` is accepted but creates no socket, and there is no
  client for it.
- There is no syslogd: `/var/run/log` belongs to ACPd, which keeps a ring of 256 lines
  (`acp logm`).

## Rules

- **Never plug a LAN port into the rest of the network while the mesh is up.** Only the WAN
  port is watched. That the WAN port is PHY index 0 comes from airport-express-tools' notes
  on the A1392; on the A1470 it was not confirmed with a cable.
- **Never run `eraseflash`, and never `flashctl … erase` without a sector number.** The first
  erases `/mnt/Flash` (the base station's configuration, its SSH keys and this kit) even with
  no arguments; it happened here, with the unit running. Recovery without a reboot, as
  `/etc/rc.d/flash` does it: `umount -f /mnt/Flash; flashctl /dev/rflash2.raw erase;
  flashctl /dev/rflash2.raw format 0x1234; disklabel -w -r -f /etc/flash2.disktab flash2 flash;
  newfs /dev/rflash2a; mount /mnt/Flash`, copy the host keys back from `/etc/ssh`, reinstall
  the kit, then reboot over ACP: the firmware writes its `ACPData.bin` when it shuts down,
  not at each change.
- **Never run `wl … chanim_stats`.** It panics the kernel on both radios. Once the unit rebooted
  and came back on its own; the second time it did not come back by itself.
- **Do not enable `pf` with the stored ruleset.** It is `block drop all`: it would cut SSH and
  the uplink. A rate limiter or a guest filter must load a ruleset of its own first.
- **Mind the RAM disk.** `/mnt/Memory` is 16 MB and holds the daemon (9 MB), its log and the
  VAPs' files. A script that writes the daemon's state must check every copy it makes.
- Leave both radios off in the stored configuration. A WLAN of the firmware's own on a radio
  the daemon manages is one more VAP outside its control.

## What does not work

| | |
|---|---|
| Stock Go on the base station | dies creating its first thread; see [go/](go/README.md) for the patched toolchain |
| The firmware's `wpa_supplicant`, and its "join a network" mode | broken, see step 2 |
| WPA3, PMF, 802.11r and v, channel utilization, a country of another domain, a dark LED | see the table: the driver or the hardware does not allow them |
| Per-client retry counters | `wl sta_info` on this driver has none |
| A VAP's MAC address | `wlanaddr` and `wlanbssid` are ignored: the driver numbers the BSSIDs itself, one per VAP |
| `wl dwds_config enable sta <bssid>` | not needed, and after it (together with a test bridge) the link stopped passing traffic until the supplicant was restarted |
| `ifconfig wlanN inet A.B.C.D netmask …` | `SIOCAIFADDR: Invalid argument`; the firmware uses the `alias` form |
| `tcpdump` on a wlan VAP | shows received frames only |
| `wl txpwr` and `pwr_percent`, hostapd's `tx_power` | read back or accepted, move nothing; only `ifconfig VAP txpower` does |

## Tested and not tested

Tested on one A1470: several reboots with the kit, each coming back on its own, the last with
the daemon on the base station and no other host involved; a wired client and Wi-Fi clients on
both bands, each with a ping in both directions and a download; a wired host on the LAN
learning the real MAC of a client behind the base station; adoption; a phone on the real SSID
showing in the console with the same signal and rates the base station reports.

With `-apply`: two WLANs on 2.4 GHz and one on 5 GHz brought up from the configuration on
flash at boot; a client authenticating with WPA2 and getting an address on a WLAN rendered
from scratch; the 2.4 GHz channel changed from the console (to 11, to 1, back to auto), each
applied within seconds, the 5 GHz WLAN and the uplink untouched; with the uplink taken away
by hand and the daemon's copy of the configuration edited, the 5 GHz radio put on channel 44
at 40, 20 and 80 MHz, a width-only change included, then the uplink coming back by itself with
the WLANs behind it. With a Raspberry Pi as a client, on a WLAN and on a LAN port: Reconnect,
Block and Unblock from the console; the console's MAC lists in the driver ACL; per-client
signal, rates, bytes, packets and time in network; the isolation flag; the neighbour scan
(clients up through it); Locate; remote syslog reaching the log server; the refusal of an
empty configuration; and the update script described above.

Built and flashed, but not yet seen on the unit: the minimum RSSI disconnect, the allow-list
MAC filter, isolation actually separating two clients, the transmit power wait after a radio
restart, schedules, the chosen automatic channel, band steering, the table of all ports,
temperatures and fan in the console, the encrypted syslog intake, and `uow -dump`, `owctl`,
`owplan` and `owprobe` against the unit.

Not tested: a cable on the WAN port (the wired-uplink branch of `up.sh`), and so the 5 GHz
radio with no uplink on a real unit rather than by hand; the parent changing channel; an open
WLAN and a hidden one through the console; any other AirPort model; a parent that is not a
UniFi AP. Not done: guest networks and VLANs, rate limits, device logins, SNMP, NTP and the
time zone, mesh credentials from the console.

## Undo

On the base station, delete `/mnt/Flash/mesh` and `/mnt/Flash/rc.local` (the image in the
tail of `flash1` is inert without them). Restore the Wi-Fi backup `airportctl` made before the first write
(`airportctl wifi restore <file> --untested`), set `mode sharing nat --untested`, reboot, and
forget the device in the console. A factory reset also clears the stored configuration, but
not the files on `/mnt/Flash`.

## Credits

[TDSOJohn/airport-express-tools](https://github.com/TDSOJohn/airport-express-tools) (MIT): the
ACP client, the model and firmware notes, the client-mode analysis, and the wpa_supplicant
port this one is adapted from. See `build/NOTICE`.
