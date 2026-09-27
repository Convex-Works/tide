package e2e

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// Borrowed from moil's own end-to-end tests (e2e/log_test.go there).

// A testLog shows what the machines say in the test's log, each line
// prefixed with who said it, until the test is over. Go shows it for tests
// that fail, or with -v.
type testLog struct {
	t    *testing.T
	mu   sync.Mutex
	over bool
}

var testLogs sync.Map // *testing.T → *testLog

// logFor returns t's log. Call it before registering cleanups that may
// still log: the log closes after them.
func logFor(t *testing.T) *testLog {
	if l, ok := testLogs.Load(t); ok {
		return l.(*testLog)
	}
	l := &testLog{t: t}
	testLogs.Store(t, l)
	t.Cleanup(func() {
		l.mu.Lock()
		l.over = true
		l.mu.Unlock()
		testLogs.Delete(t)
	})
	return l
}

func (l *testLog) print(who, line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.over {
		l.t.Logf("%s: %s", who, line)
	}
}

// writer returns a writer whose lines go to the log as who's, and to each
// of also.
func (l *testLog) writer(who string, also ...func(line string)) *lineWriter {
	return &lineWriter{emit: func(line string) {
		l.print(who, line)
		for _, f := range also {
			f(line)
		}
	}}
}

// A lineWriter hands what is written to it on, one line at a time.
type lineWriter struct {
	mu      sync.Mutex
	partial []byte
	emit    func(line string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.emit(strings.TrimRight(string(w.partial[:i]), "\r"))
		w.partial = w.partial[i+1:]
	}
}

// lines collects lines, for tests to look through.
type lines struct {
	mu  sync.Mutex
	all []string
}

func (l *lines) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.all = append(l.all, line)
}

func (l *lines) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.all)
}

// find returns the first line, from the from-th on, that contains s.
func (l *lines) find(from int, s string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.all[min(from, len(l.all)):] {
		if strings.Contains(line, s) {
			return line, true
		}
	}
	return "", false
}
