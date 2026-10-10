package proxy

import (
	"bytes"
	"context"
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
// client's own credential, so a subscription session is not overwritten when it is empty.
//
// The returned string is the media type of the request built. The media type of the
// response — the one that decides whether the answer is a stream — is not known until the
// request has been sent, so the caller reads it from the response.
func (s *Server) upstreamRequest(ctx context.Context, r *http.Request, body []byte) (*http.Request, string, error) {
	cfg, err := config.Load(s.opts.Home, s.opts.Project)
	if err != nil {
		return nil, "", err
	}
	target, err := url.Parse(cfg.Upstream)
	if err != nil {
		return nil, "", err
	}
	target.Path = strings.TrimSuffix(target.Path, "/") + r.URL.Path
	target.RawQuery = r.URL.RawQuery

	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header = r.Header.Clone()
	for _, h := range hopByHopHeaders {
		req.Header.Del(h)
	}
	req.Header.Del("Host")
	req.Header.Del("Content-Length")

	if env := cfg.APIKeyEnv; env != "" {
		if key := os.Getenv(env); key != "" {
			req.Header.Del("Authorization")
			req.Header.Set("X-Api-Key", key)
		}
	}
	return req, req.Header.Get("Content-Type"), nil
}
