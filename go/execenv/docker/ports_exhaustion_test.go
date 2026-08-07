package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/execenv"
)

// TestPortPoolExhaustionNamesThePool is the diagnosability test for the failure
// that cost a day: the 101st session on a 100-port pool must fail with an error
// an operator can act on — which pool, how big it is, and that it is FULL of
// live sessions — not the shape-free "no available ports".
func TestPortPoolExhaustionNamesThePool(t *testing.T) {
	pa, err := NewPortAllocator(30001, 30100)
	if err != nil {
		t.Fatalf("NewPortAllocator: %v", err)
	}
	for i := 0; i < 100; i++ {
		if _, err := pa.Allocate(fmt.Sprintf("s-%03d", i)); err != nil {
			t.Fatalf("allocate %d: %v", i, err)
		}
	}
	_, err = pa.Allocate("s-101")
	if err == nil {
		t.Fatalf("the 101st session must fail")
	}
	t.Logf("operator sees: %v", err)
	for _, want := range []string{"100", "30001", "30100", "exhausted"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("exhaustion error does not name %q — an operator cannot tell "+
				"a saturated host from a lost session: %q", want, err.Error())
		}
	}
	// And it must be recognisable by TYPE, so callers above the adapter (the
	// Runner) can branch on "the host is full" without string-matching.
	if !errors.Is(err, execenv.ErrNoCapacity) {
		t.Errorf("exhaustion does not wrap execenv.ErrNoCapacity: %v", err)
	}

	// Releasing one lease makes room again, and the pool stops reporting full.
	pa.Release("s-000")
	if err := pa.Capacity(); err != nil {
		t.Errorf("a pool with a free port must report capacity, got %v", err)
	}
}

// TestDinDReportsCapacity pins the seam the Runner uses (execenv.CapacityReporter)
// and the warning that fires on the approach to the cliff — the pool is the
// host's hard session ceiling and nothing reaps it, so silence until it is
// already empty is how a saturated host gets misread as a product bug.
func TestDinDReportsCapacity(t *testing.T) {
	var lines []string
	ports, _ := NewPortAllocator(40000, 40002) // three ports
	e := newDinDWith(DinDConfig{
		GatewayIP: "172.17.0.1", Network: "bridge",
		PortRangeStart: 40000, PortRangeEnd: 40002,
		Logf: func(f string, v ...any) { lines = append(lines, fmt.Sprintf(f, v...)) },
	}, newFakeDocker(), ports)
	e.poller = func(context.Context, string) bool { return true }
	e.healthRetryInterval = 0

	if err := e.Capacity(); err != nil {
		t.Fatalf("an empty pool must report capacity, got %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := e.Provision(context.Background(), execenv.ProvisionSpec{
			SessionID: fmt.Sprintf("s-%d", i), Image: "img:test",
		}); err != nil {
			t.Fatalf("provision %d: %v", i, err)
		}
	}
	if err := e.Capacity(); !errors.Is(err, execenv.ErrNoCapacity) {
		t.Fatalf("a full pool must report ErrNoCapacity, got %v", err)
	}
	_, err := e.Provision(context.Background(), execenv.ProvisionSpec{SessionID: "s-4", Image: "img:test"})
	if !errors.Is(err, execenv.ErrNoCapacity) {
		t.Fatalf("provisioning past the pool must wrap ErrNoCapacity, got %v", err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "nearly exhausted") || !strings.Contains(joined, "40000-40002") {
		t.Errorf("no low-water warning naming the pool was logged:\n%s", joined)
	}
}
