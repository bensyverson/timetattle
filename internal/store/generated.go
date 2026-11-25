package store

import (
	"strings"

	"github.com/bensyverson/timetattle/internal/generated"
)

// notGenerated is generated.Is's refusal as SQL, true of a row reports count.
// Its arguments are notGeneratedArgs. The COALESCE keeps a row with no
// locator, which NOT over NULL would drop.
var notGenerated = `NOT (source_id = ? AND (` +
	strings.TrimSuffix(strings.Repeat("COALESCE(locator, '') GLOB ? OR ", len(generated.Dirs)), " OR ") +
	`))`

var notGeneratedArgs = func() []any {
	args := []any{generated.Source}
	for _, glob := range generated.Globs() {
		args = append(args, glob)
	}
	return args
}()
