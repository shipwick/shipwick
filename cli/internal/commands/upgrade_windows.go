package commands

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// replaceExecutable puts staged in place of current. Windows lets a running
// executable be renamed but neither overwritten nor deleted, so the old one
// moves aside and the next run removes it.
func replaceExecutable(current, staged string) error {
	old := staleExecutableName(current)
	os.Remove(old)
	if err := os.Rename(current, old); err != nil {
		return err
	}
	if err := os.Rename(staged, current); err != nil {
		os.Rename(old, current)
		return err
	}
	return nil
}

// removeStaleExecutable deletes what the previous upgrade left behind. Best
// effort: another shipwick may still be running it.
func removeStaleExecutable() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	os.Remove(staleExecutableName(exe))
}

// Machine types as IsWow64Process2 reports them (IMAGE_FILE_MACHINE_*).
const (
	machineAMD64 = 0x8664
	machineARM64 = 0xAA64
)

// machineArch is the architecture of the machine. Windows on arm64 runs
// amd64 programs as well, and a shipwick installed before there was an arm64
// build is one: the upgrade should bring the build made for the machine, not
// another one of what it happens to be.
func machineArch() string {
	isWow64Process2 := syscall.NewLazyDLL("kernel32.dll").NewProc("IsWow64Process2")
	if isWow64Process2.Find() != nil {
		// Before Windows 10 1709, where there is no arm64 either.
		return runtime.GOARCH
	}
	process, err := syscall.GetCurrentProcess()
	if err != nil {
		return runtime.GOARCH
	}
	var emulated, native uint16
	ok, _, _ := isWow64Process2.Call(uintptr(process), uintptr(unsafe.Pointer(&emulated)), uintptr(unsafe.Pointer(&native)))
	if ok == 0 {
		return runtime.GOARCH
	}
	switch native {
	case machineARM64:
		return "arm64"
	case machineAMD64:
		return "amd64"
	}
	return runtime.GOARCH
}
