package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	fetchMaxResult      = 100_000
	fetchMaxBody        = 2 << 20 // raw bytes read off the wire before reduction
	fetchDefaultTimeout = 30 * time.Second
	fetchMaxTimeout     = 2 * time.Minute
	fetchMaxRedirects   = 5
)

type fetchArgs struct {
	URL          string `json:"url"`                     // http or https, required
	Timeout      int    `json:"timeout,omitempty"`       // ms, capped by the max
	IgnoreRobots bool   `json:"ignore_robots,omitempty"` // per-call override, permission-visible
}

// FetchDisplay is the typed data the UI renders for a fetch: the URL is
// the consequence a permission ask shows (doc 01 section 5.3).
type FetchDisplay struct {
	URL         string
	ContentType string
	Bytes       int
}

// fetchTool retrieves an http or https URL and reduces it to text. It
// is the one core tool that reaches outside the machine, so it carries
// the private-address guard and strict budgets (doc 04 section 9).
type fetchTool struct {
	Base
	guard func(net.IP) error
}

// NewFetch builds the fetch tool with the SSRF guard on.
func NewFetch() Tool { return fetchTool{guard: guardAddress} }

func (fetchTool) Name() string { return "fetch" }

func (fetchTool) Schema() Schema {
	return Schema{
		Name:        "fetch",
		Description: "GET one http or https URL and read it as markdown or text; no search, no POST.",
		Params: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {"type": "string", "description": "The http or https URL to fetch."},
				"timeout": {"type": "integer", "description": "Timeout in milliseconds, capped by the configured maximum."},
				"ignore_robots": {"type": "boolean", "description": "Fetch even when robots directives disallow it; a deliberate per-call override."}
			},
			"required": ["url"]
		}`),
	}
}

func (fetchTool) MaxResultSize() int { return fetchMaxResult }

// fetch observes a remote resource without mutating local state, so a
// research fan-out parallelizes its fetches (doc 04 section 9.5).
func (fetchTool) IsReadOnly(json.RawMessage) bool        { return true }
func (fetchTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (fetchTool) ValidateInput(_ context.Context, raw json.RawMessage, _ *ToolContext) error {
	var a fetchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments did not decode: %v", err)
	}
	if a.URL == "" {
		return fmt.Errorf("url is required")
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		return fmt.Errorf("url did not parse: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("url has no host")
	}
	return nil
}

func (f fetchTool) Call(ctx context.Context, raw json.RawMessage, _ *ToolContext, _ ProgressFunc) (*Result, error) {
	var a fetchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	timeout := fetchDefaultTimeout
	if a.Timeout > 0 {
		timeout = min(time.Duration(a.Timeout)*time.Millisecond, fetchMaxTimeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := f.client()
	if !a.IgnoreRobots {
		if err := checkRobots(ctx, client, a.URL); err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ari-fetch")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBody))
	if err != nil {
		return nil, fmt.Errorf("fetch of %s failed mid-body: %v", a.URL, err)
	}
	mediaType := resp.Header.Get("Content-Type")
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil {
		mediaType = parsed
	}

	var text string
	switch {
	case mediaType == "text/html", mediaType == "application/xhtml+xml":
		text = htmlToMarkdown(string(body))
	case strings.HasPrefix(mediaType, "text/"),
		mediaType == "application/json",
		mediaType == "application/xml",
		strings.HasSuffix(mediaType, "+json"),
		strings.HasSuffix(mediaType, "+xml"):
		text = string(body)
	default:
		return nil, fmt.Errorf("%s is %s (%d bytes); fetch cannot display it", a.URL, mediaType, len(body))
	}

	if resp.StatusCode != http.StatusOK {
		text = fmt.Sprintf("HTTP %d %s\n\n%s", resp.StatusCode, http.StatusText(resp.StatusCode), text)
	}
	return &Result{
		Model:   text,
		Display: FetchDisplay{URL: a.URL, ContentType: mediaType, Bytes: len(body)},
	}, nil
}

// client builds an http client whose dialer runs the guard on the
// resolved IP of every connection, so every redirect hop is re-checked
// and a hostname resolving to a private address is caught at the socket,
// not the URL string (doc 04 section 9.4).
func (f fetchTool) client() *http.Client {
	guard := f.guard
	dialer := &net.Dialer{
		Control: func(_, address string, _ syscall.RawConn) error {
			if guard == nil {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("fetch could not verify the address %q", host)
			}
			return guard(ip)
		},
	}
	return &http.Client{
		Transport: &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= fetchMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", fetchMaxRedirects)
			}
			return nil
		},
	}
}

// metadataIP is the cloud metadata endpoint every provider parks at the
// same link-local address; the guard names it separately because it is
// the highest-value SSRF target there is.
var metadataIP = net.IPv4(169, 254, 169, 254)

// errBlockedAddress names the class of address, never the address
// itself resolved from attacker-controlled input.
func errBlockedAddress(kind string) error {
	return fmt.Errorf("fetch refused: the URL resolves to a %s address, which fetch never reaches; this guard is not a flag fetch can drop", kind)
}

// guardAddress rejects connections to addresses the agent should never
// reach from a fetch: loopback, private RFC1918, link-local, and the
// cloud metadata address. Applied to the resolved IP of every request
// and every redirect hop (doc 04 section 9.4).
func guardAddress(ip net.IP) error {
	switch {
	case ip.Equal(metadataIP):
		return errBlockedAddress("cloud metadata endpoint")
	case ip.IsLoopback():
		return errBlockedAddress("loopback")
	case ip.IsPrivate():
		return errBlockedAddress("private network")
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return errBlockedAddress("link-local")
	case ip.IsUnspecified():
		return errBlockedAddress("unspecified")
	}
	return nil
}

// checkRobots honors the site's robots directives for automated
// fetching by default (doc 04 section 9.3). An unreachable or absent
// robots.txt allows the fetch; only an explicit disallow refuses.
func checkRobots(ctx context.Context, client *http.Client, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	robotsURL := u.Scheme + "://" + u.Host + "/robots.txt"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ari-fetch")
	resp, err := client.Do(req)
	if err != nil {
		// The guard also runs on the robots fetch; a blocked address must
		// not be softened into "no robots, go ahead".
		if strings.Contains(err.Error(), "fetch refused:") {
			return err
		}
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if robotsDisallows(string(body), path) {
		return fmt.Errorf("robots directives for %s disallow fetching %s; set ignore_robots to override deliberately", u.Host, path)
	}
	return nil
}

// robotsDisallows is a modest robots.txt reader: it honors Disallow
// lines in the User-agent: * group (and any ari group) by path prefix.
// Allow precedence and wildcards are out of scope until a real need
// shows.
func robotsDisallows(robots, path string) bool {
	applies := false
	for line := range strings.SplitSeq(robots, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		field = strings.ToLower(strings.TrimSpace(field))
		value = strings.TrimSpace(value)
		switch field {
		case "user-agent":
			applies = value == "*" || strings.EqualFold(value, "ari-fetch") || strings.EqualFold(value, "ari")
		case "disallow":
			if applies && value != "" && strings.HasPrefix(path, value) {
				return true
			}
		}
	}
	return false
}
