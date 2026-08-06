//go:build darwin

package fsobserve

import (
	"io/fs"
	"syscall"
	"time"
)

// birthtime reports when the file was created, straight from the st_birthtime
// field APFS keeps beside mtime.
//
// This mirrors internal/timestamped's own darwin birthtime reader rather than
// calling it: that function is unexported, and a bool return fits this
// package better than an error does — a Stat_t that will not assert here is
// not a configuration mistake worth failing a whole harvest over, the way a
// manifest declaring "birthtime" on a platform without one is. It just means
// this one file contributes no birth-time observation, and the walk carries
// on to the next.
func birthtime(info fs.FileInfo) (time.Time, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(stat.Birthtimespec.Unix()), true
}
