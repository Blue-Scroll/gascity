package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestWorkspacePinnedBdBinaryParsesTheConfigOncePerEdit pins vn-cq6w8n8. The
// bd pin reads only workspace.env, and every exec order and bd env build asks
// for it, so it must not parse the whole city config per call. It must still
// see an edit: a long-lived supervisor reads the new pin on the next call.
func TestWorkspacePinnedBdBinaryParsesTheConfigOncePerEdit(t *testing.T) {
	captureAmbientBdBinWarnings(t)
	pinA := filepath.Join(t.TempDir(), "bd")
	pinB := filepath.Join(t.TempDir(), "bd-other")
	writeExecutable(t, pinA, "#!/bin/sh\nexit 0\n")
	writeExecutable(t, pinB, "#!/bin/sh\nexit 0\n")
	cityDir, _, _ := writeAmbientBdBinCity(t, "[workspace.env]\nBD_BIN = "+strconv.Quote(pinA)+"\n")

	before := workspaceEnvLoads.Load()
	for i := 0; i < 20; i++ {
		got, err := workspacePinnedBdBinaryOptional(cityDir)
		if err != nil {
			t.Fatalf("call %d: workspacePinnedBdBinaryOptional: %v", i, err)
		}
		if got != pinA {
			t.Fatalf("call %d: pin = %q, want %q", i, got, pinA)
		}
	}
	// One load, or two when the first load read more files than the
	// pre-load stamp covered. Never one per call.
	if loads := workspaceEnvLoads.Load() - before; loads < 1 || loads > 2 {
		t.Fatalf("20 calls parsed the city config %d times, want 1 or 2", loads)
	}

	edited := "[workspace]\nname = \"demo\"\n# repinned\n[workspace.env]\nBD_BIN = " + strconv.Quote(pinB) + "\n"
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := workspacePinnedBdBinaryOptional(cityDir)
	if err != nil {
		t.Fatalf("after edit: workspacePinnedBdBinaryOptional: %v", err)
	}
	if got != pinB {
		t.Fatalf("after edit: pin = %q, want %q (a cached pin must not outlive a city.toml edit)", got, pinB)
	}
}

// TestCityWorkspaceEnvReturnsAPrivateCopy pins that a caller mutating the
// returned map cannot change what the next caller reads.
func TestCityWorkspaceEnvReturnsAPrivateCopy(t *testing.T) {
	cityDir, _, _ := writeAmbientBdBinCity(t, "[workspace.env]\nFOO = \"bar\"\n")
	first, err := cityWorkspaceEnv(cityDir)
	if err != nil {
		t.Fatalf("cityWorkspaceEnv: %v", err)
	}
	first["FOO"] = "mutated"
	second, err := cityWorkspaceEnv(cityDir)
	if err != nil {
		t.Fatalf("cityWorkspaceEnv: %v", err)
	}
	if second["FOO"] != "bar" {
		t.Fatalf("FOO = %q after a caller mutated its copy, want %q", second["FOO"], "bar")
	}
}
