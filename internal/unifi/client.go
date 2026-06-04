package unifi

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// paginatedResponse is the wrapper for list endpoints in the integration API.
type paginatedResponse struct {
	Offset     int             `json:"offset"`
	Limit      int             `json:"limit"`
	Count      int             `json:"count"`
	TotalCount int             `json:"totalCount"`
	Data       json.RawMessage `json:"data"`
}

// Site represents a UniFi site.
type Site struct {
	ID                string `json:"id"`
	InternalReference string `json:"internalReference"`
	Name              string `json:"name"`
}

// NetworkList represents a traffic matching list (the UniFi integration API
// calls these "traffic matching lists"; the UI calls them "network lists").
type NetworkList struct {
	Type  string             `json:"type"` // PORTS, IPV4_ADDRESSES, IPV6_ADDRESSES
	ID    string             `json:"id"`
	Name  string             `json:"name"`
	Items []TrafficMatchItem `json:"items,omitempty"`
}

// TrafficMatchItem represents an item in a traffic matching list.
// For IPV4_ADDRESSES: type is IP_ADDRESS (value), SUBNET (value), or IP_ADDRESS_RANGE (start/stop).
// For PORTS lists, value/start/stop can be numeric in API responses.
type TrafficMatchItem struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
	Start string `json:"start,omitempty"`
	Stop  string `json:"stop,omitempty"`
}

// UnmarshalJSON accepts both string and numeric scalar fields for value/start/stop.
func (t *TrafficMatchItem) UnmarshalJSON(data []byte) error {
	type rawTrafficMatchItem struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
		Start json.RawMessage `json:"start"`
		Stop  json.RawMessage `json:"stop"`
	}

	var raw rawTrafficMatchItem
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var err error
	t.Type = raw.Type
	t.Value, err = scalarToString(raw.Value)
	if err != nil {
		return fmt.Errorf("decode item.value: %w", err)
	}
	t.Start, err = scalarToString(raw.Start)
	if err != nil {
		return fmt.Errorf("decode item.start: %w", err)
	}
	t.Stop, err = scalarToString(raw.Stop)
	if err != nil {
		return fmt.Errorf("decode item.stop: %w", err)
	}

	return nil
}

func scalarToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}

	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return s, nil
	}

	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&n); err == nil {
		return n.String(), nil
	}

	var b bool
	if err := json.Unmarshal(trimmed, &b); err == nil {
		if b {
			return "true", nil
		}
		return "false", nil
	}

	return "", fmt.Errorf("unsupported scalar type: %s", string(trimmed))
}

// networkListUpdate is the request body for creating/updating a traffic matching list.
type networkListUpdate struct {
	Type  string             `json:"type"`
	Name  string             `json:"name"`
	Items []TrafficMatchItem `json:"items"`
}

// Client is an authenticated HTTP client for the UniFi integration API.
type Client struct {
	baseURL    string
	site       string
	siteID     string // resolved UUID
	apiBase    string // auto-detected: /proxy/network/integration/v1 or /integration/v1
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a client that authenticates via API key.
// Set skipTLSVerify to true to disable certificate validation (for self-signed certs).
func NewClient(baseURL, site, apiKey string, skipTLSVerify bool) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}

	normalizedBaseURL, err := normalizeControllerBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	c := &Client{
		baseURL: normalizedBaseURL,
		site:    site,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: skipTLSVerify},
			},
		},
	}
	return c, nil
}

func normalizeControllerBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid controller URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("controller URL must use http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("controller URL must include a host")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("controller URL must not include credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("controller URL must not include query or fragment")
	}
	if parsed.Path != "" && strings.Trim(parsed.Path, "/") != "" {
		return "", fmt.Errorf("controller URL must be the controller origin only (no path)")
	}

	normalized := &url.URL{
		Scheme: strings.ToLower(parsed.Scheme),
		Host:   strings.ToLower(parsed.Host),
	}
	return normalized.String(), nil
}

// detectAPIBase probes both the UDM proxy path and the legacy path, caching the
// one that returns valid JSON.  UDM/Dream Machine devices serve the integration
// API at /proxy/network/integration/v1; older Cloud Key / self-hosted installs
// use /integration/v1 directly.
func (c *Client) detectAPIBase() (string, error) {
	if c.apiBase != "" {
		return c.apiBase, nil
	}
	candidates := []string{
		"/proxy/network/integration/v1",
		"/integration/v1",
	}
	for _, base := range candidates {
		body, err := c.doRequest(http.MethodGet, base+"/sites?limit=1", nil)
		if err != nil {
			continue
		}
		var page paginatedResponse
		if json.Unmarshal(body, &page) == nil {
			c.apiBase = base
			return c.apiBase, nil
		}
	}
	return "", fmt.Errorf("could not reach integration API at %s — tried /proxy/network/integration/v1 and /integration/v1; check URL and API key", c.baseURL)
}

// resolveSiteID resolves the configured site identifier to a UUID.
func (c *Client) resolveSiteID() (string, error) {
	if c.siteID != "" {
		return c.siteID, nil
	}

	// If the site value already looks like a UUID, use it directly but still
	// detect the API base path so subsequent requests go to the right prefix.
	if len(c.site) == 36 && strings.Count(c.site, "-") == 4 {
		if _, err := c.detectAPIBase(); err != nil {
			return "", err
		}
		c.siteID = c.site
		return c.siteID, nil
	}

	apiBase, err := c.detectAPIBase()
	if err != nil {
		return "", err
	}

	body, err := c.doRequest(http.MethodGet, apiBase+"/sites?limit=200", nil)
	if err != nil {
		return "", fmt.Errorf("list sites: %w", err)
	}

	var page paginatedResponse
	if err := json.Unmarshal(body, &page); err != nil {
		return "", fmt.Errorf("decode sites response: %w", err)
	}

	var sites []Site
	if err := json.Unmarshal(page.Data, &sites); err != nil {
		return "", fmt.Errorf("decode sites: %w", err)
	}

	for _, s := range sites {
		if s.InternalReference == c.site || s.Name == c.site || s.ID == c.site {
			c.siteID = s.ID
			return c.siteID, nil
		}
	}

	return "", fmt.Errorf("site %q not found (available: %d sites)", c.site, len(sites))
}

// ListNetworkLists fetches all traffic matching lists from the controller.
func (c *Client) ListNetworkLists() ([]NetworkList, error) {
	siteID, err := c.resolveSiteID()
	if err != nil {
		return nil, err
	}

	body, err := c.doRequest(http.MethodGet,
		c.apiBase+"/sites/"+siteID+"/traffic-matching-lists?limit=200", nil)
	if err != nil {
		return nil, err
	}

	var page paginatedResponse
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("decode traffic matching lists response: %w", err)
	}

	var lists []NetworkList
	if err := json.Unmarshal(page.Data, &lists); err != nil {
		return nil, fmt.Errorf("decode traffic matching lists: %w", err)
	}
	return lists, nil
}

// GetNetworkList fetches a traffic matching list by its ID.
func (c *Client) GetNetworkList(listID string) (*NetworkList, error) {
	siteID, err := c.resolveSiteID()
	if err != nil {
		return nil, err
	}

	body, err := c.doRequest(http.MethodGet,
		c.apiBase+"/sites/"+siteID+"/traffic-matching-lists/"+listID, nil)
	if err != nil {
		return nil, err
	}

	var nl NetworkList
	if err := json.Unmarshal(body, &nl); err != nil {
		return nil, fmt.Errorf("decode traffic matching list: %w", err)
	}
	return &nl, nil
}

// UpdateNetworkList PUTs an updated traffic matching list back to the controller.
func (c *Client) UpdateNetworkList(nl *NetworkList) error {
	siteID, err := c.resolveSiteID()
	if err != nil {
		return err
	}

	update := networkListUpdate{
		Type:  nl.Type,
		Name:  nl.Name,
		Items: nl.Items,
	}
	payload, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("encode traffic matching list: %w", err)
	}

	_, err = c.doRequest(http.MethodPut,
		c.apiBase+"/sites/"+siteID+"/traffic-matching-lists/"+nl.ID, payload)
	return err
}

func (c *Client) doRequest(method, path string, body []byte) ([]byte, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("request path must start with '/'")
	}
	if strings.Contains(path, "://") {
		return nil, fmt.Errorf("request path must not contain a URL scheme")
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(respBody)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet)
	}

	// If the response is HTML (e.g. a login redirect), the API key is wrong
	// or the URL is pointing at the wrong endpoint.
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/html") || (len(respBody) > 0 && respBody[0] == '<') {
		snippet := string(respBody)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return nil, fmt.Errorf("controller returned HTML instead of JSON (check URL and API key): %s", snippet)
	}

	return respBody, nil
}
