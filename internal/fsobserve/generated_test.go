package fsobserve

import (
	"testing"

	"github.com/bensyverson/timetattle/internal/generated"
)

// What reports refuse to count must also not be collected.
func TestTheWalkSkipsEveryGeneratedDirectory(t *testing.T) {
	for _, dir := range generated.Dirs {
		if !excludedDirs[dir] {
			t.Errorf("reports leave out %q but the walk still collects it", dir)
		}
	}
}
