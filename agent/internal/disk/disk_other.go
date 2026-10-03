//go:build !linux

package disk

// Of reports the usage of the filesystem that holds path. Off Linux, where
// only the development build runs, it is unknown.
func Of(string) (Usage, bool) { return Usage{}, false }
