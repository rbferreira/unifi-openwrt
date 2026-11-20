/* relay: accept TCP on PORT from one source address and pipe it to DEST:DPORT.
 * Used on the AirPort so the UniFi console's adoption SSH (to the base station's
 * address, port 2222) reaches the uow daemon running on another host.
 * usage: relay LISTEN_PORT DEST_IP DEST_PORT ALLOWED_SRC_IP */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <signal.h>
#include <sys/types.h>
#include <sys/socket.h>
#include <sys/select.h>
#include <netinet/in.h>
#include <arpa/inet.h>

static void pump(int a, int b) {
	char buf[4096];
	for (;;) {
		fd_set r; FD_ZERO(&r); FD_SET(a, &r); FD_SET(b, &r);
		if (select((a > b ? a : b) + 1, &r, NULL, NULL, NULL) < 0) return;
		int from = FD_ISSET(a, &r) ? a : b, to = from == a ? b : a;
		ssize_t n = read(from, buf, sizeof buf);
		if (n <= 0) return;
		for (ssize_t o = 0; o < n;) { ssize_t w = write(to, buf + o, n - o); if (w <= 0) return; o += w; }
	}
}

int main(int argc, char **argv) {
	if (argc != 5) { fprintf(stderr, "usage: relay PORT DEST_IP DEST_PORT ALLOWED_SRC\n"); return 2; }
	struct sockaddr_in la, da; int one = 1;
	memset(&la, 0, sizeof la); la.sin_family = AF_INET; la.sin_port = htons(atoi(argv[1])); la.sin_addr.s_addr = INADDR_ANY;
	memset(&da, 0, sizeof da); da.sin_family = AF_INET; da.sin_port = htons(atoi(argv[3])); da.sin_addr.s_addr = inet_addr(argv[2]);
	in_addr_t allow = inet_addr(argv[4]);
	int ls = socket(AF_INET, SOCK_STREAM, 0);
	setsockopt(ls, SOL_SOCKET, SO_REUSEADDR, &one, sizeof one);
	if (bind(ls, (struct sockaddr *)&la, sizeof la) < 0 || listen(ls, 4) < 0) { perror("bind/listen"); return 1; }
	signal(SIGCHLD, SIG_IGN);
	for (;;) {
		struct sockaddr_in ca; socklen_t cl = sizeof ca;
		int c = accept(ls, (struct sockaddr *)&ca, &cl);
		if (c < 0) continue;
		if (ca.sin_addr.s_addr != allow) { close(c); continue; }
		if (fork() == 0) {
			close(ls);
			int d = socket(AF_INET, SOCK_STREAM, 0);
			if (connect(d, (struct sockaddr *)&da, sizeof da) == 0) pump(c, d);
			_exit(0);
		}
		close(c);
	}
}
