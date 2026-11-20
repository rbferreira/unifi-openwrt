/*
 * Apple's NetBSD 6.0 kernel on the AirPort Time Capsule A1470 (7.9.1) sends a
 * 160-byte struct if_msghdr (RTM_IFINFO), 8 bytes more than the NetBSD 7.0
 * earm header (152): the sockaddr_dl starts at offset 160 in NET_RT_IFLIST.
 * struct ifa_msghdr is the stock 24 bytes. Measured on-device 2026-11-20.
 */
#ifndef _APPLE_COMPAT_NET_IF_H_
#define _APPLE_COMPAT_NET_IF_H_

#define if_msghdr if_msghdr_netbsd70
#include_next <net/if.h>
#undef if_msghdr

struct if_msghdr {
	u_short	ifm_msglen __attribute__((__aligned__(8)));
	u_char	ifm_version;
	u_char	ifm_type;
	int	ifm_addrs;
	int	ifm_flags;
	u_short	ifm_index;
	struct	if_data ifm_data __attribute__((__aligned__(8)));
	u_int32_t ifm_apple_pad[2];
};

typedef char __apple_if_msghdr_size_check[sizeof(struct if_msghdr) == 160 ? 1 : -1];

#endif
