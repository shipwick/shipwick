package disk

import "golang.org/x/sys/unix"

// Of reports the usage of the filesystem that holds path. ok is false when
// the operating system would not say.
func Of(path string) (u Usage, ok bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return Usage{}, false
	}
	return usage(int64(st.Bsize), st.Blocks, st.Bfree, st.Bavail), true
}
