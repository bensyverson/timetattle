//go:build !darwin

package fsobserve

import (
	"io/fs"
	"time"
)

// birthtime is unavailable away from macOS. Substituting mtime would put a
// wrong answer into the record under the name of a right one, so a file on
// this platform contributes only its modification-time observation.
func birthtime(info fs.FileInfo) (time.Time, bool) {
	return time.Time{}, false
}
