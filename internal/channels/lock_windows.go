package channels

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// A handle with no sharing is released by Windows even if the process crashes.
func lockDirectory(dir string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(filepath.Join(dir, "channels.lock"))
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), "channels.lock"), nil
}

// File Sync uses FlushFileBuffers. Windows does not expose directory fsync via
// os.File; use a write-through replacement for compaction (see replace helper).
func syncDirectory(string) error { return nil }

func replaceJournal(from, to string) error {
	source, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	// MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH.
	result, _, callErr := syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW").Call(uintptr(unsafe.Pointer(source)), uintptr(unsafe.Pointer(target)), 0x1|0x8)
	if result == 0 {
		return callErr
	}
	return nil
}
