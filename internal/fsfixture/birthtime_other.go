//go:build !darwin

package fsfixture

import (
	"time"
)

// CanSetBirthtime reports whether this platform has a settable creation
// timestamp. Away from Darwin it does not, and internal/fsobserve reports no
// birth time here either, so a tree built on this platform would be missing
// half of every file's evidence rather than merely being approximate.
const CanSetBirthtime = false

func setBirthtime(string, time.Time) error { return ErrNoBirthtime }

// Birthtime is unavailable here, for the same reason internal/fsobserve's own
// reader is: substituting mtime would put a wrong answer into the record under
// the name of a right one.
func Birthtime(string) (time.Time, error) { return time.Time{}, ErrNoBirthtime }
