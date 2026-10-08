package proctable

import (
	"path/filepath"
	"strings"
)

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

// isInfrastructureCommand reports whether a process name is tmux
// infrastructure rather than an agent. Both scanners use it: Darwin passes the
// first token of ps's command column (argv[0], possibly a path), Linux passes
// /proc/<pid>/comm. The match is exact on the path-stripped name — the bare
// executable, or the "tmux: server" / "tmux: client" titles tmux sets through
// setproctitle where the platform supports it. A substring test would also
// hide any tmux-* wrapper that is really an agent root, and a root the scan
// hides is a runtime the orphan sweep can never reap.
func isInfrastructureCommand(command string) bool {
	switch filepath.Base(strings.TrimSpace(command)) {
	// "tmux:" is the whole story on Darwin: psRecords splits the ps line with
	// strings.Fields, so a proctitle'd "tmux: server" reaches here as its first
	// token alone. Nothing legitimately runs as an executable named "tmux:", so
	// this costs none of the false-positive narrowing the exact match bought.
	case "tmux", "tmux:", "tmux: server", "tmux: client":
		return true
	}
	return false
}

// isRecordScanRoot is the pure half of IsScanRoot: whether record is the root
// process of the agent named by its GC_SESSION_ID. A root is what
// killExistingOrphans terminates, so a false "yes" here kills something that
// is not an agent. It lives in this untagged file so its tests run on the
// Linux CI runner too.
//
// Infrastructure is never a root (see scanRecordsBySessionID), so a kill path
// that asks about the tmux server is told no, even when the server carries a
// GC_SESSION_ID. A tmux server keeps the argv and environment of whichever
// command started it, so a server started by `new-session -e
// GC_SESSION_ID=<id>`, or by a gc command run inside an agent's pane, carries
// that agent's id for the rest of its life. It is also PPID 1, which looks
// exactly like an escaped orphan. Treating it as a root once killed the
// server, and with it every session on the socket, each time that one agent
// restarted (vn-p4rwdst, gastownhall/gascity#5392).
func isRecordScanRoot(records map[int]psRecord, record psRecord) bool {
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
