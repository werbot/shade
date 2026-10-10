package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProxyHeartbeat is how often a running proxy rewrites its marker. A marker older than
// three heartbeats is a proxy that died without clearing it.
const ProxyHeartbeat = 10 * time.Second

// proxyMarkerKey is the meta key the marker lives under. It is a single row: there is one
// proxy per state directory.
const proxyMarkerKey = "proxy"

// ProxyMarker is the liveness record of a running `shade serve`.
type ProxyMarker struct {
	PID      int
	Port     int
	RootPath string
	TS       int64 // unix seconds
}

// SetProxyMarker writes the marker. The write waits out a locked database through execBusy,
// exactly as the migration does: the proxy refreshes this row on a timer while other
// sessions write to the same database.
func (s *Store) SetProxyMarker(ctx context.Context, m ProxyMarker) error {
	value := fmt.Sprintf("%d:%d:%d:%s", m.PID, m.Port, m.TS, m.RootPath)
	if err := execBusy(s.db, `INSERT OR REPLACE INTO meta(key, value) VALUES(?, ?)`, proxyMarkerKey, value); err != nil {
		return fmt.Errorf("writing the proxy marker: %w", err)
	}
	return nil
}

// ClearProxyMarker removes the marker: the proxy is no longer serving.
func (s *Store) ClearProxyMarker(ctx context.Context) error {
	if err := execBusy(s.db, `DELETE FROM meta WHERE key=?`, proxyMarkerKey); err != nil {
		return fmt.Errorf("clearing the proxy marker: %w", err)
	}
	return nil
}

// ProxyCovers reports whether a live proxy is serving this project. It is true only when
// the marker is fresh, its process is alive, and it names the same project: a proxy
// started in another repository does not wrap this one's traffic, so it must not silence
// the gate here.
func (s *Store) ProxyCovers(ctx context.Context, rootPath string) (bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, proxyMarkerKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading the proxy marker: %w", err)
	}
	m, ok := parseProxyMarker(value)
	if !ok {
		return false, nil
	}
	// Younger than three heartbeats, otherwise it is a proxy that died without clearing it.
	if time.Now().Unix()-m.TS >= int64(3*ProxyHeartbeat/time.Second) {
		return false, nil
	}
	if m.RootPath != rootPath {
		return false, nil
	}
	return pidAlive(m.PID), nil
}

// parseProxyMarker reads `<pid>:<port>:<ts>:<root_path>`. SplitN keeps the root path whole:
// a path may contain a colon, while a pid, a port and a timestamp may not.
func parseProxyMarker(s string) (ProxyMarker, bool) {
	parts := strings.SplitN(s, ":", 4)
	if len(parts) != 4 {
		return ProxyMarker{}, false
	}
	pid, errPID := strconv.Atoi(parts[0])
	port, errPort := strconv.Atoi(parts[1])
	ts, errTS := strconv.ParseInt(parts[2], 10, 64)
	if errPID != nil || errPort != nil || errTS != nil {
		return ProxyMarker{}, false
	}
	return ProxyMarker{PID: pid, Port: port, TS: ts, RootPath: parts[3]}, true
}

// pidAlive reports whether the process pid exists. A zero signal does not signal it, only
// asks the kernel whether it is there: ESRCH means it is gone, while EPERM means it exists
// and we merely may not signal it — reading that backwards would make the gate fire while a
// proxy is serving.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
