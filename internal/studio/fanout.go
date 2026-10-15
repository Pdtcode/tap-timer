package studio

import (
	"errors"
	"io"
	"sync"
)

// errSinkDone tells the copy loop a sink has stopped reading.
var errSinkDone = errors.New("sink finished")

// fanout streams body to every sink at once, so one upload from the phone
// reaches all three platforms without touching disk. It runs at the pace of
// the slowest sink. A sink that fails is dropped and the others carry on.
// It returns each sink's error, in order.
func fanout(body io.Reader, sinks []func(io.Reader) error) []error {
	errs := make([]error, len(sinks))
	writers := make([]*io.PipeWriter, len(sinks))
	var wg sync.WaitGroup
	for i, sink := range sinks {
		pr, pw := io.Pipe()
		writers[i] = pw
		wg.Go(func() {
			err := sink(pr)
			errs[i] = err
			if err == nil {
				err = errSinkDone
			}
			pr.CloseWithError(err) // unblocks the copy loop if it's mid-write
		})
	}

	live := len(writers)
	buf := make([]byte, 256<<10)
	var readErr error
	for live > 0 {
		n, err := body.Read(buf)
		if n > 0 {
			for i, w := range writers {
				if w == nil {
					continue
				}
				if _, werr := w.Write(buf[:n]); werr != nil {
					writers[i] = nil
					live--
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}
	for _, w := range writers {
		if w != nil {
			w.CloseWithError(readErr) // nil means a clean EOF
		}
	}
	wg.Wait()
	if readErr != nil {
		for i := range errs {
			if errs[i] == nil {
				errs[i] = readErr
			}
		}
	}
	return errs
}
