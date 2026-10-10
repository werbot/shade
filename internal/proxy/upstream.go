package proxy

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/werbot/shade/internal/config"
)

// hopByHopHeaders belong to one leg of the connection and must not be passed on. The
// client's Host and Content-Length are dropped too, though they live outside Header: both
// describe the request as it arrives, and the outgoing request sets its own.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// upstreamRequest builds the request that carries body to the configured upstream.
//
// The path of the base URL is kept as a prefix — a gateway may be mounted at
// /anthropic-aws — and the client's own path is appended to it. The query is carried over
// unchanged. Headers are copied as they are, minus the hop-by-hop set and Host and
// Content-Length. api_key_env, when the named variable is set and non-empty, replaces the
// client's own credential, so a subscription session is not overwritten when it is empty;
// the swap is reported on diag, because it moves the session onto another billing without
// breaking anything the user would see.
//
// Whether the answer is a stream is not decided here: that is the media type of the
// response, which does not exist until the caller has sent the request. The caller reads
// it from the response — the request's own media type is application/json for every
// endpoint this proxy serves.
func (s *Server) upstreamRequest(ctx context.Context, r *http.Request, body []byte, cfg config.Config) (*http.Request, error) {
	target, err := url.Parse(cfg.Upstream)
	if err != nil {
		return nil, err
	}
	target.Path = strings.TrimSuffix(target.Path, "/") + r.URL.Path
	target.RawQuery = r.URL.RawQuery

	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = r.Header.Clone()
	for _, h := range hopByHopHeaders {
		req.Header.Del(h)
	}
	req.Header.Del("Host")
	req.Header.Del("Content-Length")

	if env := cfg.APIKeyEnv; env != "" {
		if key := os.Getenv(env); key != "" {
			if req.Header.Get("Authorization") != "" {
				// The session is not failing and nothing looks different to the user — so
				// the one thing that did change is named here: which credential rides on
				// the request now, and that the client's own was dropped.
				fmt.Fprintf(s.opts.Diag, "proxy: %s replaced the client's Authorization with X-Api-Key\n", env)
			}
			req.Header.Del("Authorization")
			req.Header.Set("X-Api-Key", key)
		}
	}
	return req, nil
}
