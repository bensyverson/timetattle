//go:build darwin

package fsfixture

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// CanSetBirthtime reports whether this platform has a settable creation
// timestamp. Only Darwin does.
const CanSetBirthtime = true

// The two attribute-list constants this file needs. golang.org/x/sys/unix
// exports Setattrlist and the Attrlist struct but not the ATTR_ bits, so they
// are declared here from <sys/attr.h> rather than reached for through a new
// dependency.
const (
	attrBitMapCount = 5          // ATTR_BIT_MAP_COUNT
	attrCmnCrtime   = 0x00000200 // ATTR_CMN_CRTIME
)

// setBirthtime sets a file's creation timestamp.
//
// There is no portable call for this and no libc wrapper: utimes and its
// relatives reach mtime and atime only. setattrlist with ATTR_CMN_CRTIME is
// the interface macOS exposes, and it is what `SetFile -d` has always used
// underneath.
func setBirthtime(path string, at time.Time) error {
	attrs := unix.Attrlist{
		Bitmapcount: attrBitMapCount,
		Commonattr:  attrCmnCrtime,
	}
	ts := unix.NsecToTimespec(at.UnixNano())
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&ts)), unsafe.Sizeof(ts))
	if err := unix.Setattrlist(path, &attrs, buf, 0); err != nil {
		return fmt.Errorf("setattrlist ATTR_CMN_CRTIME on %s: %w", path, err)
	}
	return nil
}

// Birthtime reports what st_birthtime holds for path.
//
// It exists so that Build can verify its own work and so that a fixture's
// tests can assert against the timestamp the filesystem actually stored rather
// than the one the manifest asked for. internal/fsobserve has its own reader of
// the same field; that one is unexported and returns a bool, which suits a
// walk that tolerates a file it cannot read. A fixture builder that cannot read
// back what it wrote has failed, so this one returns an error.
func Birthtime(path string) (time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, fmt.Errorf("%s: no syscall.Stat_t behind this FileInfo", path)
	}
	return time.Unix(stat.Birthtimespec.Unix()), nil
}
