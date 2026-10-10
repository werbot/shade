package store_test

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/werbot/shade/internal/store"
)

// TestProxyMarkerRoundTrip — a fresh marker with a live pid covers the project, and clearing
// it makes ProxyCovers false again.
func TestProxyMarkerRoundTrip(t *testing.T) {
	st := openTemp(t)
	root := "/tmp/project"
	m := store.ProxyMarker{PID: os.Getpid(), Port: 8787, RootPath: root, TS: time.Now().Unix()}
	if err := st.SetProxyMarker(ctx, m); err != nil {
		t.Fatal(err)
	}
	ok, err := st.ProxyCovers(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("a fresh marker with a live pid must cover its project")
	}
	if err := st.ClearProxyMarker(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err = st.ProxyCovers(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a cleared marker must not cover the project")
	}
}

// TestProxyCoversRejectsAStaleMarker — a marker older than three heartbeats is a proxy that
// died without clearing it, so it must not silence the gate.
func TestProxyCoversRejectsAStaleMarker(t *testing.T) {
	st := openTemp(t)
	root := "/tmp/project"
	m := store.ProxyMarker{
		PID:      os.Getpid(),
		Port:     8787,
		RootPath: root,
		TS:       time.Now().Add(-31 * time.Second).Unix(),
	}
	if err := st.SetProxyMarker(ctx, m); err != nil {
		t.Fatal(err)
	}
	ok, err := st.ProxyCovers(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a marker older than three heartbeats must not cover the project")
	}
}

// TestProxyCoversRejectsADeadProcess — the timestamp may be fresh, but a pid whose process is
// gone is not a running proxy.
func TestProxyCoversRejectsADeadProcess(t *testing.T) {
	st := openTemp(t)
	root := "/tmp/project"
	m := store.ProxyMarker{PID: deadPID(t), Port: 8787, RootPath: root, TS: time.Now().Unix()}
	if err := st.SetProxyMarker(ctx, m); err != nil {
		t.Fatal(err)
	}
	ok, err := st.ProxyCovers(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a marker whose process is gone must not cover the project")
	}
}

// TestProxyCoversRejectsAnotherProject — a proxy started in another repository does not wrap
// this one's traffic, so it must not silence this project's gate.
func TestProxyCoversRejectsAnotherProject(t *testing.T) {
	st := openTemp(t)
	m := store.ProxyMarker{PID: os.Getpid(), Port: 8787, RootPath: "/tmp/one", TS: time.Now().Unix()}
	if err := st.SetProxyMarker(ctx, m); err != nil {
		t.Fatal(err)
	}
	ok, err := st.ProxyCovers(ctx, "/tmp/two")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a proxy serving another project must not cover this one")
	}
}

// deadPID runs a throwaway process to completion and returns its pid: it has exited and been
// waited for, so the kernel reports it gone.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running a throwaway process: %v", err)
	}
	return cmd.Process.Pid
}
