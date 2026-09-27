package proctable

import "strings"

// This file carries no build tag on purpose: the darwin scan's decisions are
// pure, so their tests run on the Linux CI runner too.

// psRecord is one row of `ps eww -ax -o pid=,ppid=,command=`. env holds every
// KEY=VALUE token on the row, which on darwin mixes the process's argv with its
// environment: ps cannot tell them apart.
type psRecord struct {
	pid     int
	ppid    int
	command string
	env     map[string]string
}

func darwinPSCommand(fields []string) string {
	if len(fields) < 3 {
		return ""
	}
	return fields[2]
}

func isInfrastructureCommand(command string) bool {
	return strings.Contains(strings.ToLower(command), "tmux")
}

// darwinScanRoot reports whether record is the root process of the agent named
// by its GC_SESSION_ID. A root is what killExistingOrphans terminates, so a
// false "yes" here kills something that is not an agent.
//
// A tmux process is never a root, even when it carries a GC_SESSION_ID. A tmux
// server keeps the argv and environment of whichever command started it, so a
// server started by `new-session -e GC_SESSION_ID=<id>`, or by a gc command run
// inside an agent's pane, carries that agent's id for the rest of its life. It
// is also PPID 1, which looks exactly like an escaped orphan. Treating it as a
// root once killed the server, and with it every session on the socket, each
// time that one agent restarted (vn-p4rwdst).
func darwinScanRoot(records map[int]psRecord, record psRecord) bool {
	if isInfrastructureCommand(record.command) {
		return false
	}
	sessionID := record.env["GC_SESSION_ID"]
	if sessionID == "" {
		return false
	}
	parent, ok := records[record.ppid]
	return !ok || parent.env["GC_SESSION_ID"] != sessionID || isInfrastructureCommand(parent.command)
}
