// Package recorder provides a tiny interaction-log used by every in-memory mock
// in agentkit. Each mock embeds a Recorder and calls Record("Method", args...)
// at the top of every method, so tests can assert which dependency was called,
// with what arguments, in what order — the same hermetic-test discipline as the
// Platinum interface-refactor's mockutil.Recorder.
package recorder

import (
	"strings"
	"sync"
)

// Call is one recorded method invocation.
type Call struct {
	Method string
	Args   []any
}

// Recorder is an embeddable, concurrency-safe interaction log.
type Recorder struct {
	mu    sync.Mutex
	calls []Call
}

// Record appends a call. String and []string args are CLONED because callers in
// the real world often pass values backed by recycled buffers (the fasthttp
// #821 aliasing bug in Platinum): a retained log must not alias a buffer that is
// about to be reused. Cloning here keeps the discipline identical to mockutil.
func (r *Recorder) Record(method string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cloned := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case string:
			cloned[i] = strings.Clone(v)
		case []string:
			cp := make([]string, len(v))
			for j, s := range v {
				cp[j] = strings.Clone(s)
			}
			cloned[i] = cp
		default:
			cloned[i] = a
		}
	}
	r.calls = append(r.calls, Call{Method: method, Args: cloned})
}

// Calls returns a copy of the full ordered log.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Call, len(r.calls))
	copy(out, r.calls)
	return out
}

// CallsTo returns the calls to a given method, in order.
func (r *Recorder) CallsTo(method string) []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Call
	for _, c := range r.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Count returns how many times a method was called.
func (r *Recorder) Count(method string) int {
	return len(r.CallsTo(method))
}

// Methods returns the ordered list of method names called.
func (r *Recorder) Methods() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	for i, c := range r.calls {
		out[i] = c.Method
	}
	return out
}

// Reset clears the log.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}
