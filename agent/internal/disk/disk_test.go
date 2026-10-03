package disk

import "testing"

func TestUsageCountsLikeDf(t *testing.T) {
	// 1000 blocks of 4 KB, 300 free, of which 50 are reserved for root.
	u := usage(4096, 1000, 300, 250)
	if u.Used != 700*4096 || u.Total != 950*4096 {
		t.Errorf("usage = %+v; used is what is taken, total is used plus what an unprivileged process may still take", u)
	}
}
