package parser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	shared "github.com/suenot/socials-auto"
)

// rewriteRT redirects every request whose URL starts with graphPrefix to the
// httptest server `target`. Path + query are preserved, so the parser still
// builds the canonical /{version}/{handle}?fields=… URL and we just intercept
// the host.
type rewriteRT struct {
	prefix string // e.g. "https://graph.facebook.com"
	target string // e.g. "http://127.0.0.1:NNNN"
	next   http.RoundTripper
}

func (r rewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.String(), r.prefix) {
		newURL := r.target + strings.TrimPrefix(req.URL.String(), r.prefix)
		req2, err := http.NewRequestWithContext(req.Context(), req.Method, newURL, req.Body)
		if err != nil {
			return nil, err
		}
		req2.Header = req.Header
		return r.next.RoundTrip(req2)
	}
	return r.next.RoundTrip(req)
}

// newGraphParser returns a parser whose HTTP client rewrites graph.facebook.com
// → srv.URL. Using the real default GraphAPIURL exercises the URL building
// logic as well.
func newGraphParser(t *testing.T, srv *httptest.Server, token string) *FacebookParser {
	t.Helper()
	client := &http.Client{
		Transport: rewriteRT{
			prefix: defaultGraphAPI,
			target: strings.TrimSuffix(srv.URL, "/"),
			next:   http.DefaultTransport,
		},
		Timeout: 5 * time.Second,
	}
	return New(Config{
		AccessToken: token,
		HTTPClient:  client,
	})
}

func TestPlatform(t *testing.T) {
	if p := New(Config{}); p.Platform() != shared.PlatformFacebook {
		t.Fatalf("platform mismatch: %s", p.Platform())
	}
}

// TestFetchChannel_GraphHappyPath: AccessToken set, Graph returns a normal
// Page payload, parser maps it correctly.
func TestFetchChannel_GraphHappyPath(t *testing.T) {
	const body = `{
		"id": "1234567890",
		"name": "Example Page",
		"followers_count": 98765,
		"fan_count": 90000,
		"about": "An example",
		"link": "https://www.facebook.com/example/",
		"verification_status": "blue_verified"
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sanity-check that the parser passed the expected fields + token.
		if !strings.Contains(r.URL.RawQuery, "followers_count") {
			t.Errorf("missing followers_count in fields; got %q", r.URL.RawQuery)
		}
		if !strings.Contains(r.URL.RawQuery, "access_token=tkn") {
			t.Errorf("missing access_token; got %q", r.URL.RawQuery)
		}
		if !strings.HasPrefix(r.URL.Path, "/v19.0/example") {
			t.Errorf("bad path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := newGraphParser(t, srv, "tkn")
	snap, err := p.FetchChannel(context.Background(), "example")
	if err != nil {
		t.Fatalf("FetchChannel: %v", err)
	}
	if snap.Platform != shared.PlatformFacebook {
		t.Errorf("platform = %s", snap.Platform)
	}
	if snap.Handle != "example" {
		t.Errorf("handle = %s", snap.Handle)
	}
	if snap.Followers != 98765 {
		t.Errorf("followers = %d; want 98765", snap.Followers)
	}
	if snap.URL != "https://www.facebook.com/example/" {
		t.Errorf("url = %s", snap.URL)
	}
	if got, _ := snap.Raw["page_id"].(string); got != "1234567890" {
		t.Errorf("raw[page_id] = %v", snap.Raw["page_id"])
	}
	if got, _ := snap.Raw["about"].(string); got != "An example" {
		t.Errorf("raw[about] = %v", snap.Raw["about"])
	}
	if got, _ := snap.Raw["verification_status"].(string); got != "blue_verified" {
		t.Errorf("raw[verification_status] = %v", snap.Raw["verification_status"])
	}
	if got, _ := snap.Raw["source"].(string); got != "graph_api" {
		t.Errorf("raw[source] = %v", snap.Raw["source"])
	}
}

// TestFetchChannel_GraphFallsBackToFanCount: when followers_count is missing
// or zero, we should surface fan_count instead.
func TestFetchChannel_GraphFallsBackToFanCount(t *testing.T) {
	const body = `{"id":"1","name":"X","fan_count":42}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := newGraphParser(t, srv, "tkn")
	snap, err := p.FetchChannel(context.Background(), "x")
	if err != nil {
		t.Fatalf("FetchChannel: %v", err)
	}
	if snap.Followers != 42 {
		t.Errorf("followers = %d; want 42 (from fan_count)", snap.Followers)
	}
}

// TestFetchChannel_InvalidToken: Graph returns code 190 → ErrAuth.
func TestFetchChannel_InvalidToken(t *testing.T) {
	const body = `{"error":{"message":"Invalid OAuth access token.","type":"OAuthException","code":190,"fbtrace_id":"abc"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := newGraphParser(t, srv, "bad")
	_, err := p.FetchChannel(context.Background(), "example")
	if !errors.Is(err, shared.ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
	if !strings.Contains(err.Error(), "190") {
		t.Errorf("expected error to mention code 190, got %q", err)
	}
}

// TestFetchChannel_UnknownObject: Graph returns code 100 (subcode 33 / unknown
// field / object does not exist) → ErrNotFound.
func TestFetchChannel_UnknownObject(t *testing.T) {
	const body = `{"error":{"message":"Unsupported get request. Object with ID 'nope' does not exist","type":"GraphMethodException","code":100,"error_subcode":33}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := newGraphParser(t, srv, "tkn")
	_, err := p.FetchChannel(context.Background(), "nope")
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestFetchChannel_RateLimited429: bare 429 (no JSON body) → ErrRateLimited.
func TestFetchChannel_RateLimited429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := newGraphParser(t, srv, "tkn")
	_, err := p.FetchChannel(context.Background(), "example")
	if !errors.Is(err, shared.ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
}

// TestFetchChannel_5xxTransient: 500 → ErrTransient.
func TestFetchChannel_5xxTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newGraphParser(t, srv, "tkn")
	_, err := p.FetchChannel(context.Background(), "example")
	if !errors.Is(err, shared.ErrTransient) {
		t.Fatalf("want ErrTransient, got %v", err)
	}
}

// TestFetchChannel_NoTokenWithUnreachableCamoufox: no AccessToken, CamoufoxURL
// points at an unresolvable host → fetchViaCamoufox bubbles transport error,
// wrapped as ErrAuth.
func TestFetchChannel_NoTokenWithUnreachableCamoufox(t *testing.T) {
	p := New(Config{CamoufoxURL: "http://camoufox-not-here.invalid:9/"})
	_, err := p.FetchChannel(context.Background(), "example")
	if !errors.Is(err, shared.ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
}

// TestFetchChannel_ViaCamoufox: no token, working camoufox wrapper that
// returns HTML embedding a follower count. Parser must extract the counter
// and return Followers > 0.
func TestFetchChannel_ViaCamoufox(t *testing.T) {
	// HTML mimics the shape of an authenticated Facebook profile page:
	// a <script> with an embedded JSON blob carrying followers_count.
	const fbHTML = `<!doctype html><html><head><title>Evgeniy Soloviov | Facebook</title></head>
<body>
<script type="application/json">
{
  "require": [["RelayPrefetchedStreamCache","next",[],["profile_intro_card_xxxxxxxxxxxxxx",
    {
      "data": {
        "user": {
          "id": "100000123456789",
          "name": "Evgeniy Soloviov",
          "followers_count": 12345,
          "fan_count": 11000
        }
      }
    }
  ]]]
}
</script>
</body></html>`

	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"html": ` + jsonQuote(fbHTML) + `,
			"status": 200,
			"final_url": "https://www.facebook.com/soloviov.evgeniy/",
			"title": "Evgeniy Soloviov | Facebook",
			"cookies_present": true
		}`))
	}))
	defer srv.Close()

	p := New(Config{
		HTTPClient:  srv.Client(),
		CamoufoxURL: srv.URL,
	})
	snap, err := p.FetchChannel(context.Background(), "soloviov.evgeniy")
	if err != nil {
		t.Fatalf("FetchChannel via camoufox: %v", err)
	}
	if gotPath != "/fetch" {
		t.Errorf("path = %q, want /fetch", gotPath)
	}
	var reqBody struct {
		URL     string `json:"url"`
		Profile string `json:"profile"`
	}
	if err := json.Unmarshal(gotBody, &reqBody); err != nil {
		t.Fatalf("body not JSON: %v (raw=%s)", err, string(gotBody))
	}
	if reqBody.Profile != "facebook" {
		t.Errorf("camoufox profile = %q, want facebook", reqBody.Profile)
	}
	if !strings.Contains(reqBody.URL, "soloviov.evgeniy") {
		t.Errorf("camoufox url = %q", reqBody.URL)
	}
	if snap.Followers != 12345 {
		t.Errorf("followers = %d, want 12345", snap.Followers)
	}
	if name, _ := snap.Raw["name"].(string); name != "Evgeniy Soloviov" {
		t.Errorf("name = %v, want Evgeniy Soloviov", snap.Raw["name"])
	}
	if got, _ := snap.Raw["source"].(string); got != "camoufox_html" {
		t.Errorf("source = %v, want camoufox_html", snap.Raw["source"])
	}
	if snap.URL != "https://www.facebook.com/soloviov.evgeniy/" {
		t.Errorf("url = %s", snap.URL)
	}
}

// TestFetchChannel_ViaCamoufox_NoCounter: page renders fine but no follower
// count is exposed. We surface Followers=0 + fetch_note WITHOUT an error.
func TestFetchChannel_ViaCamoufox_NoCounter(t *testing.T) {
	const fbHTML = `<!doctype html><html><head><title>Anonymous User | Facebook</title></head>
<body><div>page rendered, no counters</div></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"html": ` + jsonQuote(fbHTML) + `,
			"status": 200,
			"final_url": "https://www.facebook.com/anon/",
			"title": "Anonymous User | Facebook",
			"cookies_present": true
		}`))
	}))
	defer srv.Close()

	p := New(Config{
		HTTPClient:  srv.Client(),
		CamoufoxURL: srv.URL,
	})
	snap, err := p.FetchChannel(context.Background(), "anon")
	if err != nil {
		t.Fatalf("expected soft success, got err=%v", err)
	}
	if snap.Followers != 0 {
		t.Errorf("followers = %d, want 0", snap.Followers)
	}
	if _, ok := snap.Raw["fetch_note"]; !ok {
		t.Errorf("expected fetch_note diagnostic, got Raw=%v", snap.Raw)
	}
	if name, _ := snap.Raw["name"].(string); name != "Anonymous User" {
		t.Errorf("name (from title) = %v, want Anonymous User", snap.Raw["name"])
	}
}

// TestFetchChannel_ViaCamoufox_502: camoufox returned 502 / error envelope →
// ErrAuth (because that's how camoufox failures surface to FetchChannel).
func TestFetchChannel_ViaCamoufox_502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"navigation timeout"}`))
	}))
	defer srv.Close()

	p := New(Config{
		HTTPClient:  srv.Client(),
		CamoufoxURL: srv.URL,
	})
	_, err := p.FetchChannel(context.Background(), "x")
	if !errors.Is(err, shared.ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
}

// jsonQuote returns s encoded as a JSON string literal suitable for inlining.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestFetchChannel_NoTokenNoCamoufox: neither configured → ErrAuth with the
// documented hint string.
func TestFetchChannel_NoTokenNoCamoufox(t *testing.T) {
	p := New(Config{})
	_, err := p.FetchChannel(context.Background(), "example")
	if !errors.Is(err, shared.ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
	if !strings.Contains(err.Error(), "FACEBOOK_ACCESS_TOKEN") || !strings.Contains(err.Error(), "CAMOUFOX_URL") {
		t.Errorf("expected hint mentioning both env vars; got %q", err)
	}
}

// TestFetchChannel_EmptyHandle: validate input before doing anything.
func TestFetchChannel_EmptyHandle(t *testing.T) {
	p := New(Config{AccessToken: "tkn"})
	_, err := p.FetchChannel(context.Background(), "")
	if !errors.Is(err, shared.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestFetchRecentPosts_GraphHappyPath: posts edge with two items, both mapped.
func TestFetchRecentPosts_GraphHappyPath(t *testing.T) {
	const body = `{
		"data":[
			{"id":"1_100","created_time":"2026-05-01T12:00:00+0000","message":"hi","permalink_url":"https://www.facebook.com/1_100","shares":{"count":3},"reactions":{"summary":{"total_count":42}},"comments":{"summary":{"total_count":7}}},
			{"id":"1_101","created_time":"2026-05-02T12:00:00+0000","message":"bye","permalink_url":"https://www.facebook.com/1_101","shares":{"count":0},"reactions":{"summary":{"total_count":5}},"comments":{"summary":{"total_count":1}}}
		]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/posts") {
			t.Errorf("expected /posts edge, got %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := newGraphParser(t, srv, "tkn")
	posts, err := p.FetchRecentPosts(context.Background(), "example", time.Time{})
	if err != nil {
		t.Fatalf("FetchRecentPosts: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("got %d posts, want 2", len(posts))
	}
	if posts[0].Likes != 42 || posts[0].Shares != 3 || posts[0].Comments != 7 {
		t.Errorf("post[0] counters wrong: %+v", posts[0])
	}
	if posts[0].PostID != "1_100" {
		t.Errorf("post[0] id = %s", posts[0].PostID)
	}
	if posts[0].Kind != shared.PostKindPost {
		t.Errorf("post[0] kind = %s", posts[0].Kind)
	}
}

// TestFetchRecentPosts_NoTokenNoCamoufox: same hint surface as FetchChannel.
func TestFetchRecentPosts_NoTokenNoCamoufox(t *testing.T) {
	p := New(Config{})
	_, err := p.FetchRecentPosts(context.Background(), "example", time.Time{})
	if !errors.Is(err, shared.ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
	if !strings.Contains(err.Error(), "FACEBOOK_ACCESS_TOKEN") {
		t.Errorf("expected hint mentioning FACEBOOK_ACCESS_TOKEN; got %q", err)
	}
}

// TestFetchViaCamoufox_NotConfigured: low-level branch — empty CamoufoxURL
// returns the canonical "not configured" error.
func TestFetchViaCamoufox_NotConfigured(t *testing.T) {
	p := New(Config{})
	_, _, err := p.fetchViaCamoufox(context.Background(), "https://x")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("empty CamoufoxURL: got %v, want 'not configured'", err)
	}
}
