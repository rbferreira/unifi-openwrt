# An Apple AirPort Time Capsule as a UniFi mesh AP

How an AirPort Time Capsule A1470 (802.11ac, firmware 7.9.1) was made to join a UniFi
network as a wireless (mesh) child of a UniFi AP, serve WLANs on both bands, and show up in
the UniFi console through `uow -ap-platform airport`. This directory is the kit and the
runbook to repeat it on another unit.

It is a different thing from the rest of this project. An AirPort does not run OpenWrt: the
daemon runs on the base station itself, built with a patched Go toolchain ([go/](go/README.md)).
With `-apply` the console configures its WLANs ([what it configures](#what-the-console-configures));
everything else an OpenWrt AP gets from this project is not applied there.

## What you get

| | |
|---|---|
| Uplink | 5 GHz client on the parent UniFi AP's mesh backhaul SSID (`vwire-…`), bridged at layer 2: hosts behind the AirPort keep their own MAC addresses |
| WLANs | the ones the console assigns to the AP, up to 8 per band, open or WPA2 personal: one hostapd each, started by the daemon |
| LAN ports | bridged to the uplink |
| In the console | adopted as a UAP-AC-Pro (`U7PG2`): radios, WLANs, clients with signal and rates, packet, byte, retry and error counters, the wireless uplink and its parent, ports, CPU and memory; Restart works; the LED follows the UniFi state |
| Depends on | nothing but the base station: no second host |
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
   when it first runs with that file still there. Without the seed the daemon applies nothing.

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
already meshing through this project has them in `/etc/unifi-openwrt/mesh.conf`.

`rc.local` is **sourced** by the boot script: it must never `exit` or block. Everything in
it runs in a background subshell.

**Where the daemon lives.** `/mnt/Flash` is 2.5 MB and the binary is 9 MB (2.5 MB as xz).
The flash has two 14 MB firmware banks: `flash0` with the running firmware and `flash1`
with the factory one. In both the image ends near 8.2 MB and the rest, up to a trailer in
the last 64 KB sector, is erased. `flash-uow.sh` writes the compressed binary into that
erased tail of `flash1` (from sector 144), leaving the factory image and the running bank
alone, and `up.sh` unpacks it into the RAM disk at every boot. Checked after writing: a
full read of both banks differed from the read before only in the sectors written.

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
  airport: wlan10 started
  airport: wlan11 started
  airport: wlan20 started
after apply: 3 of 3 wanted VAP(s) running
```

### 5. Adopt

The device appears in the console as pending. Adopt it there: the console opens SSH to the
base station on port 2222, where the daemon answers the adoption.

## What `up.sh` does

1. Refuses to go on if `dhcpd` is running (router mode).
2. Unpacks the daemon from `flash1` into the RAM disk and checks its size.
3. With no link on the WAN port for 20 s: creates a client VAP on `bwl0`, starts
   `wpa_supplicant`, waits for the association, adds the VAP to `bridge0`.
4. Every 5 s: a cable with link on the WAN port removes the wireless uplink (a wired uplink
   and a wireless one together are a loop); an uplink that is not associated for 60 s is torn
   down and tried again; the daemon is started once the bridge has an address and restarted
   if it dies. Whenever the uplink is created again or has changed channel, the 5 GHz WLANs
   are taken down first (they share its radio); the daemon notices within a minute and
   starts them again on the uplink's channel.

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

| From the console | On the base station |
|---|---|
| WLANs per band, SSID, hidden SSID, enable/disable | applied |
| Open and WPA2 personal | applied |
| WPA3 and WPA2/WPA3 (the firmware's hostapd is 0.6.5, no SAE), WPA-Enterprise | the WLAN is left off, never downgraded |
| 2.4 GHz channel | applied; "auto" keeps the channel the radio is on |
| 5 GHz channel | the mesh uplink's while there is one, else the console's |
| Channel width | 5 GHz with a wired uplink and a fixed channel: 20, 40 and 80 MHz applied. Behind a mesh uplink the radio keeps the width the uplink negotiated. 2.4 GHz stays at 20 MHz whatever is asked (measured) |
| Transmit power | not applied: hostapd's `tx_power` showed no effect |
| Guest networks, WLANs on a VLAN | left off (they would need a bridge of their own) |
| Client isolation, MAC filter (including clients locked to another AP), PMF, 802.11r/k/v, band steering, rate limits, schedules, device logins | not applied |

A WLAN left off, and an ignored width, power, isolation, MAC filter or required PMF, are
logged as a `note:` at each apply. Measured on the unit, and the reason for how the applier
works:

- hostapd's `channel=`, `allow_ht40` and `allow_vht80` do not move the radio. `ifconfig VAP
  channel N` on the first VAP of an idle radio does, with the width in the same argument on
  5 GHz (`44:vht/80`, `44:ht/40`, `44:ht/20`; without it the radio came up at 40 MHz), and the
  other VAPs follow the radio. On 2.4 GHz `6:ht/40` is accepted and the radio stays at 20 MHz.
- The VAPs of one radio are not independent: restarting one left another down with its
  hostapd still running. So when anything changes on a radio, all its WLANs start again
  (about 3 s); the other radio and the uplink are not touched.
- A VAP is destroyed and created again on every change: restarted on the same VAP, an open
  network kept the WPA2 state of the one before it.
- There is no timed rollback as on OpenWrt: nothing the applier does can cut the daemon
  off from the controller.

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
- Leave both radios off in the stored configuration. A WLAN of the firmware's own on a radio
  the daemon manages is one more VAP outside its control.

## What does not work

| | |
|---|---|
| Stock Go on the base station | dies creating its first thread; see [go/](go/README.md) for the patched toolchain |
| The firmware's `wpa_supplicant`, and its "join a network" mode | broken, see step 2 |
| Channel utilization | `chanim_stats` panics the kernel; `cca_get_stats`, `itfr_get_stats`, `pktq_stats` and `rate_histo` are not supported by this driver build |
| Temperatures and fan in the console | `envstat` reads four board sensors and the fan, but for an AP the controller keeps only `has_temperature` / `has_fan` and drops the values |
| Per-client retry counters | `wl sta_info` on this driver has none, so the experience score only sees the PHY rate. Retries and errors are reported per radio, from `wl counters` |
| A VAP's MAC address | `wlanaddr` and `wlanbssid` are ignored: the driver numbers the BSSIDs itself, one per VAP |
| Automatic channel selection | with `acs=1` the radio stayed on the channel it was on |
| `wl dwds_config enable sta <bssid>` | not needed, and after it (together with a test bridge) the link stopped passing traffic until the supplicant was restarted |
| `ifconfig wlanN inet A.B.C.D netmask …` | `SIOCAIFADDR: Invalid argument`; the firmware uses the `alias` form |
| `tcpdump` on a wlan VAP | shows received frames only |

## Tested and not tested

Tested on one A1470: several reboots with the kit, each coming back on its own, the last with
the daemon on the base station and no other host involved; a wired client and Wi-Fi clients on both
bands, each with a ping in both directions and a download; a wired host on the LAN learning
the real MAC of a client behind the base station; adoption; a phone on the real SSID showing
in the console with the same signal and rates the base station reports. With `-apply`: two
WLANs on 2.4 GHz and one on 5 GHz brought up from the configuration on flash at boot; a
client authenticating with WPA2 and getting an address on a WLAN rendered from scratch; the
2.4 GHz channel changed from the console (to 11, to 1, back to auto), each applied within
seconds, the 5 GHz WLAN and the uplink untouched. With the uplink taken away by hand and
the daemon's copy of the configuration edited (the controller was out of reach that way): the
5 GHz radio put on channel 44 at 40, 20 and 80 MHz, a width-only change included; then the
uplink coming back by itself with the WLANs behind it.

Not tested: a cable on the WAN port (the wired-uplink branch of `up.sh`); an automatic
5 GHz channel without an uplink; the parent changing channel; an open WLAN, a hidden one and
more than two WLANs on a radio outside the unit tests; any other AirPort model; a parent that
is not a UniFi AP.

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
