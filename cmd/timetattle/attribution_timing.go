package main

import (
	"fmt"
	"io"
	"time"
)

// timedAttribution runs the attribution pass between a line saying when it
// started and one saying how long it took. The pass can run for many minutes
// and prints nothing until it ends, which is otherwise indistinguishable from a
// stuck process.
func timedAttribution(out io.Writer, now func() time.Time, pass func() error) error {
	began := now()
	fmt.Fprintf(out, "attribution: started at %s\n", began.Format(time.RFC3339))
	err := pass()
	fmt.Fprintf(out, "attribution: finished after %s\n", now().Sub(began).Round(time.Millisecond))
	return err
}
