package store_test

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/werbot/shade/internal/store"
)

// coveredURL is the address the marker of proxyAddress serves on: every test writes a
// marker on that port, so the address the session exports is the only thing that varies.
const coveredURL = "http://127.0.0.1:8787"

// TestProxyMarkerRoundTrip — a fresh marker with a live pid covers the project, and clearing
// it makes ProxyCovers false again.
func TestProxyMarkerRoundTrip(t *testing.T) {
	st := openTemp(t)
	root := "/tmp/project"
	m := store.ProxyMarker{PID: os.Getpid(), Port: 8787, RootPath: root, TS: time.Now().Unix()}
	if err := st.SetProxyMarker(ctx, m); err != nil {
		t.Fatal(err)
	}
	ok, err := st.ProxyCovers(ctx, root, coveredURL)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("a fresh marker with a live pid on the address the session points at must cover its project")
	}
	if err := st.ClearProxyMarker(ctx); err != nil {
		t.Fatal(err)
	}
	ok, err = st.ProxyCovers(ctx, root, coveredURL)
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
	ok, err := st.ProxyCovers(ctx, root, coveredURL)
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
	ok, err := st.ProxyCovers(ctx, root, coveredURL)
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
	ok, err := st.ProxyCovers(ctx, "/tmp/two", coveredURL)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a proxy serving another project must not cover this one")
	}
}

// TestProxyCoversNeedsTheAddressTheSessionPointsAt is the second half of the question. A live
// proxy serving this project answers "is one running", not "does this session talk to it": a
// terminal that never exported the base URL sends its prompts straight to the provider. Every
// mismatch must therefore fall to the safe side — not covered — while the address the proxy
// printed, modulo a trailing slash a shell may leave, covers the project.
func TestProxyCoversNeedsTheAddressTheSessionPointsAt(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want bool
	}{
		{"the address serve printed", coveredURL, true},
		{"the same address with a trailing slash", coveredURL + "/", true},
		{"nothing exported", "", false},
		{"another proxy's port", "http://127.0.0.1:9999", false},
		{"the same port by another host name", "http://localhost:8787", false},
		{"a third-party router", "https://router.example.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openTemp(t)
			root := "/tmp/project"
			if err := st.SetProxyMarker(ctx, store.ProxyMarker{
				PID: os.Getpid(), Port: 8787, RootPath: root, TS: time.Now().Unix(),
			}); err != nil {
				t.Fatal(err)
			}
			ok, err := st.ProxyCovers(ctx, root, tc.url)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.want {
				t.Fatalf("ProxyCovers(%q) = %v, want %v", tc.url, ok, tc.want)
			}
		})
	}
}

// TestProxyCoversRejectsADeadProcessEvenWhenTheAddressMatches — the two halves are checked
// in sequence, so a dead pid must not pass because the address lines up: a proxy that stopped
// leaves traffic going straight out.
func TestProxyCoversRejectsADeadProcessEvenWhenTheAddressMatches(t *testing.T) {
	st := openTemp(t)
	if err := st.SetProxyMarker(ctx, store.ProxyMarker{
		PID: deadPID(t), Port: 8787, RootPath: "/tmp/project", TS: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	ok, err := st.ProxyCovers(ctx, "/tmp/project", coveredURL)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a dead pid must not cover the project, whatever the session points at")
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
