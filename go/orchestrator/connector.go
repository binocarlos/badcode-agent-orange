package orchestrator

import (
	"context"
	"fmt"
)

// Post and the Connector seam are declared in contracts.go (the frozen §4/§5
// types). Slice D adds only the offline double + the real adapter (channel_connector.go).

// FakeConnector is the deterministic, offline Connector double: it records what
// it "would publish", can be made to fail, and never touches the network.
type FakeConnector struct {
	Published []Post // recorded would-publish posts (successful calls only)
	Ref       string // ref to return; default "fake://post/<n>"
	Err       error  // when set, Publish fails and records nothing
	Calls     int    // total Publish invocations (success + failure)
}

func (f *FakeConnector) Publish(_ context.Context, p Post) (string, error) {
	f.Calls++
	if f.Err != nil {
		return "", f.Err
	}
	f.Published = append(f.Published, p)
	ref := f.Ref
	if ref == "" {
		ref = fmt.Sprintf("fake://post/%d", f.Calls)
	}
	return ref, nil
}

var _ Connector = &FakeConnector{}
