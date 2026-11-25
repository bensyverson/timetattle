package filedays

import (
	"slices"
	"testing"

	"github.com/bensyverson/timetattle/internal/generated"
)

// What reports refuse to count must also not be counted by a scan.
func TestDefaultExcludesCoverEveryGeneratedDirectory(t *testing.T) {
	for _, dir := range generated.Dirs {
		if !slices.Contains(DefaultExcludes, dir) {
			t.Errorf("reports leave out %q but a scan still counts it", dir)
		}
	}
}
