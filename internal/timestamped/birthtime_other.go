//go:build !darwin

package timestamped

import (
	"fmt"
	"io/fs"
	"runtime"
	"time"
)

// birthtime is unavailable away from macOS.
//
// Creation time is not a portable concept, and guessing at it by returning
// the modification time instead would put a wrong answer into the record
// under the name of a right one.
func birthtime(info fs.FileInfo) (time.Time, error) {
	return time.Time{}, fmt.Errorf("birth time for %s is not available on %s; use \"mtime\" instead",
		info.Name(), runtime.GOOS)
}
