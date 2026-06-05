package clients

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

// paginatedResponse is the wrapper for list endpoints in the UniFi integration API.
type paginatedResponse struct {
	Offset     int             `json:"offset"`
	Limit      int             `json:"limit"`
	Count      int             `json:"count"`
	TotalCount int             `json:"totalCount"`
	Data       json.RawMessage `json:"data"`
}

// unifiSite represents a UniFi site.
type unifiSite struct {
	ID                string `json:"id"`
	InternalReference string `json:"internalReference"`
	Name              string `json:"name"`
}

// unifiNetworkListUpdate is the request body for creating/updating a traffic matching list.
type unifiNetworkListUpdate struct {
	Type  string             `json:"type"`
	Name  string             `json:"name"`
	Items []TrafficMatchItem `json:"items"`
}

// UniFiClient is an authenticated HTTP client for the UniFi integration API.
type UniFiClient struct {
	baseURL    string
	site       string
	siteID     string // resolved UUID
	apiBase    string // auto-detected: /proxy/network/integration/v1 or /integration/v1
	apiKey     string
	httpClient *http.Client
}

// NewUniFiClient creates a UniFi client that authenticates via API key.
// Set skipTLSVerify to true to disable certificate validation (for self-signed certs).
func NewUniFiClient(baseURL, site, apiKey string, skipTLSVerify bool) (*UniFiClient, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}

	normalizedBaseURL, err := normalizeUniFiBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	c := &UniFiClient{
		baseURL: normalizedBaseURL,
		site:    site,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: skipTLSVerify}, //nolint:gosec
			},
		},
	}
	return c, nil
}

func normalizeUniFiBaseURL(raw string) (string, error) {
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
// one that returns valid JSON. UDM/Dream Machine devices serve the integration
// API at /proxy/network/integration/v1; older Cloud Key / self-hosted installs
// use /integration/v1 directly.
func (c *UniFiClient) detectAPIBase() (string, error) {
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
func (c *UniFiClient) resolveSiteID() (string, error) {
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

	var sites []unifiSite
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
func (c *UniFiClient) ListNetworkLists() ([]NetworkList, error) {
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
func (c *UniFiClient) GetNetworkList(listID string) (*NetworkList, error) {
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
func (c *UniFiClient) UpdateNetworkList(nl *NetworkList) error {
	siteID, err := c.resolveSiteID()
	if err != nil {
		return err
	}

	update := unifiNetworkListUpdate{
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

func (c *UniFiClient) doRequest(method, path string, body []byte) ([]byte, error) {
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
