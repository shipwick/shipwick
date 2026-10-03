// Package disk measures how full a filesystem is.
package disk

// Usage is the size of a filesystem and how much of it is taken, counted the
// way `df` does: Total leaves out the blocks reserved for root, so that
// Used == Total is the moment an application's write fails.
type Usage struct {
	Total int64
	Used  int64
}

func usage(blockSize int64, blocks, free, available uint64) Usage {
	used := int64(blocks-free) * blockSize
	return Usage{Total: used + int64(available)*blockSize, Used: used}
}
