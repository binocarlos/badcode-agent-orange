package orchestrator

import "testing"

func TestWorkerToolsetExcludesPublishing(t *testing.T) {
	set := WorkerToolset()

	// The worker surface is exactly the two thread syscalls (contracts §6).
	if len(set) != 2 || !set[SyscallJobFinished] || !set[SyscallEscalateToHuman] {
		t.Fatalf("worker surface is not the frozen §6 set: %+v", set)
	}

	// Publishing is NOT a worker tool, under any plausible name (contracts §7 #3).
	for _, name := range []string{"publish", "Publish", "connector", "connector.publish", "post"} {
		if IsWorkerTool(name) {
			t.Fatalf("%q is reachable as a worker tool — the publish gate is bypassable", name)
		}
	}

	// The map is a copy: mutating it cannot smuggle a tool into the real surface.
	set["publish"] = true
	if IsWorkerTool("publish") {
		t.Fatalf("WorkerToolset must return a fresh copy each call")
	}
}
