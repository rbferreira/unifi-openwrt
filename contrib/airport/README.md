# An Apple AirPort Time Capsule as a UniFi mesh AP

How an AirPort Time Capsule A1470 (802.11ac, firmware 7.9.1) was made to join a UniFi
network as a wireless (mesh) child of a UniFi AP, serve WLANs on both bands, and show up in
the UniFi console through `uow -ap-platform airport`. This directory is the kit and the
runbook to repeat it on another unit.

It is a different thing from the rest of this project. An AirPort does not run OpenWrt and
cannot run `uow` (see [What does not work](#what-does-not-work)): the daemon runs on another
host and reads the base station over SSH, **read-only**. The console shows the device; it
does not configure it.

## What you get

| | |
|---|---|
| Uplink | 5 GHz client on the parent UniFi AP's mesh backhaul SSID (`vwire-…`), bridged at layer 2: hosts behind the AirPort keep their own MAC addresses |
| WLANs | one SSID on 2.4 GHz (the firmware's own hostapd) and the same SSID on 5 GHz (a second hostapd on the uplink's radio) |
| LAN ports | bridged to the uplink |
| In the console | adopted as a UAP-AC-Pro (`U7PG2`): radios, WLANs, clients with signal and rates, packet and byte counters, the wireless uplink and its parent, ports, CPU and memory; Restart works; the LED follows the UniFi state |
| Survives | reboot and power loss: back on the mesh about 2 minutes after power-on |

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
| Userland | `sh sed sort tail cat ps ifconfig brconfig wl netstat arp sysctl envstat acp tcpdump curl`; no `grep`, `awk`, `head`, `tr` or `xargs`; `PATH` over SSH is only `/bin` |
| Boot hook | `/etc/rc.local` is a link to `/mnt/Flash/rc.local`, sourced by `/etc/rc.d/local` at every boot |

Other models are not covered. The 802.11n ones are big-endian ARM or MIPS on NetBSD 4 and
have other radios; see [TDSOJohn/airport-express-tools](https://github.com/TDSOJohn/airport-express-tools),
which this builds on, for the A1392.

## Steps

Placeholders: `AP` the base station, `CONSOLE` the UniFi console, `DAEMON` the host that
runs `uow`. Work over a cable on a LAN port until the uplink is proven.

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

### 2. Build the two binaries

On Linux (WSL works), with `gcc-arm-linux-gnueabi`:

```sh
cd build
./setup.sh         # NetBSD 7.0 evbarm-earm headers and libc, wpa_supplicant 0.7.3 (checksummed)
./build-wpa.sh     # out/wpa_supplicant, about 320 KB
./build-relay.sh   # out/relay
```

The firmware's own `wpa_supplicant` (0.6.5) cannot be used: started by hand it does nothing
and logs nothing, and started by the firmware's "join a network" mode it dies at startup.

What had to change against stock headers, all in `build/compat/`:

| Mismatch | Fix |
|---|---|
| `struct if_msghdr` is 160 bytes on this kernel, 152 in NetBSD 7: `getifaddrs()` returns empty names and `if_nametoindex()` fails | `compat/net/if.h`, and NetBSD's `getifaddrs.c` rebuilt against it |
| The 802.11 ioctls are FreeBSD 8's, as on the A1392 | `compat/net80211/ieee80211_ioctl.h`, unchanged from airport-express-tools |

A static binary linked against NetBSD 7's libc runs on the 6.0 kernel. **Go does not**: a
`netbsd/arm` binary dies creating its first thread (`errno=-22`), which is why `uow` runs
elsewhere.

### 3. Stored configuration (over ACP)

Three things, then a reboot. With `airportctl` (it refuses writes on this model without
`--untested`; every Wi-Fi write is backed up first):

1. **Bridge mode**: `airportctl mode sharing bridge --untested`. In router mode the unit runs
   a DHCP server on its LAN bridge, and bridging the uplink into that would leak it onto the
   network. `up.sh` refuses to start the uplink while `dhcpd` runs.
2. **5 GHz radio off** in the `WiFi` blob (`raSt = 3` on the radio whose `raCh` is above 14),
   so the firmware leaves `bwl0` alone.
3. **2.4 GHz radio as an AP** with the SSID and passphrase to serve (`raSt = 0`, then
   `wifi.set_ssid` and `wifi.secure(radio, 'wpa2', passphrase)`).

In bridge mode the unit takes its address by DHCP on `bridge0` and no longer answers at
`10.0.1.1`; over the cable it is still reachable at its IPv6 link-local address.

### 4. Install the kit

```sh
mkdir -p /mnt/Flash/mesh          # on the base station
```

| File | To | |
|---|---|---|
| `build/out/wpa_supplicant` | `/mnt/Flash/mesh/wpa_supplicant` | |
| `build/out/relay` | `/mnt/Flash/mesh/relay` | mode 755 |
| `up.sh` | `/mnt/Flash/mesh/up.sh` | |
| `wpa.conf` | `/mnt/Flash/mesh/wpa.conf` | from `wpa.conf.example`: the parent's backhaul SSID and its PMK; mode 600 |
| `relay.conf` | `/mnt/Flash/mesh/relay.conf` | one line: `DAEMON CONSOLE` |
| `rc.local` | `/mnt/Flash/rc.local` | mode 644 |

The backhaul SSID and key are what the console hands a UniFi mesh child; an OpenWrt AP
already meshing through this project has them in `/etc/unifi-openwrt/mesh.conf`.

`rc.local` is **sourced** by the boot script: it must never `exit` or block. Everything in
it runs in a background subshell.

Reboot. `up.sh` logs to `/mnt/Memory/mesh.log`:

```
up.sh start
adoption relay: 2222 -> DAEMON:2222 (from CONSOLE only)
uplink associated and in bridge0
5 GHz AP on channel 40, in the bridge
```

### 5. The daemon

On `DAEMON` (any Linux host that reaches both the console and the base station):

```sh
uow -ap AP:22 -ap-platform airport -ap-password-file ap.pw -ap-known-hosts ap_known_hosts \
    -mac <bridge0 MAC> -url http://CONSOLE:8080/inform -model U7PG2 -profile-caps \
    -hostname AirPort -state state -ssh-listen :2222 -health-every 0 -stop-on-reset=false
```

`ap_known_hosts` is `AP ssh-rsa <key>` with the key from `/mnt/Flash/ssh_host_rsa_key.pub`;
`ap.pw` holds the admin password. `-mac` is the `ether` of `bridge0`.

The device appears as pending. Adopt it in the console: the console opens SSH to the base
station's address on port 2222, the relay forwards that to the daemon, and the daemon
answers the adoption there.

## What `up.sh` does

1. Refuses to go on if `dhcpd` is running (router mode).
2. Starts the relay.
3. With no link on the WAN port for 20 s: creates a client VAP on `bwl0`, starts
   `wpa_supplicant`, waits for the association, adds the VAP to `bridge0`.
4. Starts a 5 GHz AP on the same radio, from the firmware's 2.4 GHz hostapd config, on the
   uplink's channel.
5. Every 5 s: a cable with link on the WAN port removes the wireless uplink and the 5 GHz AP
   (a wired uplink and a wireless one together are a loop); an uplink that is not associated
   for 60 s is torn down and tried again; a parent that changed channel restarts the 5 GHz AP
   on the new one.

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

## Rules

- **Never plug a LAN port into the rest of the network while the mesh is up.** Only the WAN
  port is watched. That the WAN port is PHY index 0 comes from airport-express-tools' notes
  on the A1392; on the A1470 it was not confirmed with a cable.
- **Never run `wl … chanim_stats`.** It panics the kernel on both radios. Once the unit rebooted
  and came back on its own; the second time it did not come back by itself.
- The console does not configure the base station. SSID, key and channel are changed over
  ACP and take a reboot.

## What does not work

| | |
|---|---|
| `uow` on the base station | Go binaries do not run on this kernel |
| The firmware's `wpa_supplicant`, and its "join a network" mode | broken, see step 2 |
| Channel utilization | the only source found (`chanim_stats`) panics the kernel |
| Temperatures and fan in the console | `envstat` reads four board sensors and the fan, but for an AP the controller keeps only `has_temperature` / `has_fan` and drops the values |
| Per-client retry counters | `wl sta_info` on this driver has none, so the experience score only sees the PHY rate |
| A second SSID (IoT, guest) | the `WiFi` blob has a `vaps` list per radio; not explored |
| A separate MAC for the 5 GHz AP | `wlanaddr` is ignored: the AP's BSSID is the client VAP's MAC. It works that way |
| `wl dwds_config enable sta <bssid>` | not needed, and after it (together with a test bridge) the link stopped passing traffic until the supplicant was restarted |
| `ifconfig wlanN inet A.B.C.D netmask …` | `SIOCAIFADDR: Invalid argument`; the firmware uses the `alias` form |
| `tcpdump` on a wlan VAP | shows received frames only |

## Tested and not tested

Tested on one A1470: several reboots with the kit, each coming back on its own; a wired client and Wi-Fi clients on both
bands, each with a ping in both directions and a download; a wired host on the LAN learning
the real MAC of a client behind the base station; adoption; a phone on the real SSID showing
in the console with the same signal and rates the base station reports.

Not tested: a cable on the WAN port (the wired-uplink branch of `up.sh`); the parent changing
channel; any other AirPort model; a parent that is not a UniFi AP.

## Undo

On the daemon host, stop `uow`. On the base station, delete `/mnt/Flash/mesh` and
`/mnt/Flash/rc.local`. Restore the Wi-Fi backup `airportctl` made before the first write
(`airportctl wifi restore <file> --untested`), set `mode sharing nat --untested`, reboot, and
forget the device in the console. A factory reset also clears the stored configuration, but
not the files on `/mnt/Flash`.

## Credits

[TDSOJohn/airport-express-tools](https://github.com/TDSOJohn/airport-express-tools) (MIT): the
ACP client, the model and firmware notes, the client-mode analysis, and the wpa_supplicant
port this one is adapted from. See `build/NOTICE`.
