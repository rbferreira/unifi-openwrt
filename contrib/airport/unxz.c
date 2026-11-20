/* unxz: decompress an .xz stream from stdin to stdout (liblzma). For unpacking
 * the daemon from the AirPort's small flash into its RAM disk at boot. */
#include <stdio.h>
#include <stdint.h>
#include <unistd.h>
#include <lzma.h>
int main(void) {
	static uint8_t in[65536], out[65536];
	lzma_stream s = LZMA_STREAM_INIT;
	if (lzma_stream_decoder(&s, UINT64_MAX, 0) != LZMA_OK) return 2;
	lzma_action act = LZMA_RUN;
	s.next_out = out; s.avail_out = sizeof out;
	for (;;) {
		if (s.avail_in == 0 && act == LZMA_RUN) {
			ssize_t n = read(0, in, sizeof in);
			if (n < 0) return 3;
			if (n == 0) act = LZMA_FINISH;
			s.next_in = in; s.avail_in = (size_t)n;
		}
		lzma_ret r = lzma_code(&s, act);
		if (s.avail_out == 0 || r == LZMA_STREAM_END) {
			size_t n = sizeof out - s.avail_out, o = 0;
			while (o < n) { ssize_t w = write(1, out + o, n - o); if (w <= 0) return 4; o += (size_t)w; }
			s.next_out = out; s.avail_out = sizeof out;
		}
		if (r == LZMA_STREAM_END) return 0;
		if (r != LZMA_OK) { fprintf(stderr, "unxz: error %d\n", (int)r); return 1; }
	}
}
