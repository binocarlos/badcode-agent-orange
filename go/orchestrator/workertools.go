package orchestrator

// WorkerSyscall names a tool a worker scope may invoke. The worker surface is
// deliberately reduced (contracts §6): only the thread syscalls. Publishing is
// NOT here — a worker has no publish tool to call (contracts §7 floor #3).
type WorkerSyscall string

const (
	SyscallJobFinished     WorkerSyscall = "job_finished"      // deliver a Result
	SyscallEscalateToHuman WorkerSyscall = "escalate_to_human" // raise a Needs-Human ticket
)

// WorkerToolset returns the complete, closed set of syscalls a worker scope may
// invoke. Returned fresh each call so callers cannot mutate the canonical surface.
// There is no publish/Connector entry: publishing is unreachable from here.
func WorkerToolset() map[WorkerSyscall]bool {
	return map[WorkerSyscall]bool{
		SyscallJobFinished:     true,
		SyscallEscalateToHuman: true,
	}
}

// IsWorkerTool reports whether name is an allowed worker syscall.
func IsWorkerTool(name string) bool {
	return WorkerToolset()[WorkerSyscall(name)]
}
