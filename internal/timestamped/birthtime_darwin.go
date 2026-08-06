//go:build darwin

package timestamped

import (
	"fmt"
	"io/fs"
	"syscall"
	"time"
)

// birthtime reports when the file was created.
//
// Creation time matters for formats that rewrite themselves in place: an
// application that saves over an existing backup moves its mtime but not its
// birthtime, so the two answer different questions and a manifest picks the
// one that matches how its application behaves. macOS keeps it in the stat
// structure, which io/fs deliberately does not expose portably.
func birthtime(info fs.FileInfo) (time.Time, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, fmt.Errorf("file information for %s carries no birth time", info.Name())
	}
	return time.Unix(stat.Birthtimespec.Unix()), nil
}
