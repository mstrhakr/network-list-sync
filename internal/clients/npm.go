package clients

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// npmListMeta holds NPM-specific fields that must be preserved across Get/Update
// round-trips. It is cached inside NPMClient keyed by list ID.
type npmListMeta struct {
	satisfyAny bool
	passAuth   bool
	rawItems   []npmAuthItem
	rawClients []npmClient
}

type npmAccessListSummary struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type npmAuthItem struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type npmClient struct {
	Address   string `json:"address,omitempty"`
	Directive string `json:"directive,omitempty"`
}

type npmAccessListDetail struct {
	ID         int64        `json:"id"`
	Name       string       `json:"name"`
	SatisfyAny bool         `json:"satisfy_any"`
	PassAuth   bool         `json:"pass_auth"`
	Items      []npmAuthItem `json:"items,omitempty"`
	Clients    []npmClient   `json:"clients,omitempty"`
}

type npmTokenRequest struct {
	Identity string `json:"identity"`
	Secret   string `json:"secret"`
}

type npmTokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
}

type npmAccessListUpdateRequest struct {
	Name       string       `json:"name"`
	SatisfyAny bool         `json:"satisfy_any"`
	PassAuth   bool         `json:"pass_auth"`
	Items      []npmAuthItem `json:"items,omitempty"`
	Clients    []npmClient   `json:"clients,omitempty"`
}

// NPMClient is an authenticated HTTP client for the Nginx Proxy Manager API.
// Compatibility note: callers currently pass Site as identity and APIKey as secret.
type NPMClient struct {
	baseURL    string
	apiBase    string
	identity   string
	secret     string
	token      string
	httpClient *http.Client

	mu       sync.Mutex
	listMeta map[string]npmListMeta
}

// NewNPMClient creates a client for the NPM API.
func NewNPMClient(baseURL, site, apiKey string, skipTLSVerify bool) (*NPMClient, error) {
	identity := strings.TrimSpace(site)
	secret := strings.TrimSpace(apiKey)
	if identity == "" {
		return nil, fmt.Errorf("identity is required (currently provided via site field)")
	}
	if secret == "" {
		return nil, fmt.Errorf("secret is required (currently provided via api_key field)")
	}

	normalizedBaseURL, apiBase, err := normalizeNPMBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	return &NPMClient{
		baseURL:  normalizedBaseURL,
		apiBase:  apiBase,
		identity: identity,
		secret:   secret,
		httpClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: skipTLSVerify}, //nolint:gosec
			},
		},
		listMeta: make(map[string]npmListMeta),
	}, nil
}

func normalizeNPMBaseURL(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", "", fmt.Errorf("URL must use http or https")
	}
	if parsed.Host == "" {
		return "", "", fmt.Errorf("URL must include a host")
	}
	if parsed.User != nil {
		return "", "", fmt.Errorf("URL must not include credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("URL must not include query or fragment")
	}

	path := strings.Trim(strings.TrimSpace(parsed.Path), "/")
	apiBase := "/api"
	if path != "" {
		if strings.EqualFold(path, "api") {
			apiBase = "/api"
		} else {
			return "", "", fmt.Errorf("URL path must be empty or /api")
		}
	}

	normalized := &url.URL{
		Scheme: strings.ToLower(parsed.Scheme),
		Host:   strings.ToLower(parsed.Host),
	}
	return normalized.String(), apiBase, nil
}

func (c *NPMClient) authenticate() error {
	payload, err := json.Marshal(npmTokenRequest{Identity: c.identity, Secret: c.secret})
	if err != nil {
		return fmt.Errorf("encode token request: %w", err)
	}

	body, status, ct, err := c.doRawRequest(http.MethodPost, "/tokens", payload, "")
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "..."
		}
		return fmt.Errorf("token request failed HTTP %d: %s", status, snippet)
	}
	if strings.HasPrefix(ct, "text/html") || (len(body) > 0 && body[0] == '<') {
		return fmt.Errorf("token endpoint returned HTML instead of JSON")
	}

	var resp npmTokenResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("decode token response: %w", err)
	}
	if strings.TrimSpace(resp.Token) != "" {
		c.token = strings.TrimSpace(resp.Token)
		return nil
	}
	if strings.TrimSpace(resp.AccessToken) != "" {
		c.token = strings.TrimSpace(resp.AccessToken)
		return nil
	}
	return fmt.Errorf("token response missing token")
}

func (c *NPMClient) doRawRequest(method, path string, body []byte, bearerToken string) ([]byte, int, string, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, 0, "", fmt.Errorf("request path must start with '/'")
	}
	if strings.Contains(path, "://") {
		return nil, 0, "", fmt.Errorf("request path must not contain a URL scheme")
	}

	fullURL := c.baseURL + c.apiBase + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, fullURL, bodyReader)
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, "", fmt.Errorf("read body: %w", err)
	}
	return respBody, resp.StatusCode, resp.Header.Get("Content-Type"), nil
}

func (c *NPMClient) doAuthedRequest(method, path string, body []byte) ([]byte, error) {
	if strings.TrimSpace(c.token) == "" {
		if err := c.authenticate(); err != nil {
			return nil, err
		}
	}

	for attempt := 0; attempt < 2; attempt++ {
		respBody, status, ct, err := c.doRawRequest(method, path, body, c.token)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			c.token = ""
			if err := c.authenticate(); err != nil {
				return nil, err
			}
			continue
		}
		if status < 200 || status >= 300 {
			snippet := string(respBody)
			if len(snippet) > 200 {
				snippet = snippet[:200] + "..."
			}
			return nil, fmt.Errorf("HTTP %d: %s", status, snippet)
		}
		if strings.HasPrefix(ct, "text/html") || (len(respBody) > 0 && respBody[0] == '<') {
			snippet := string(respBody)
			if len(snippet) > 200 {
				snippet = snippet[:200] + "..."
			}
			return nil, fmt.Errorf("server returned HTML instead of JSON: %s", snippet)
		}
		return respBody, nil
	}
	return nil, fmt.Errorf("request failed")
}

// ListNetworkLists fetches all NPM access lists.
func (c *NPMClient) ListNetworkLists() ([]NetworkList, error) {
	body, err := c.doAuthedRequest(http.MethodGet, "/nginx/access-lists", nil)
	if err != nil {
		return nil, err
	}
	var summaries []npmAccessListSummary
	if err := json.Unmarshal(body, &summaries); err != nil {
		return nil, fmt.Errorf("decode access lists: %w", err)
	}

	lists := make([]NetworkList, 0, len(summaries))
	for _, s := range summaries {
		id := strconv.FormatInt(s.ID, 10)
		detail, err := c.GetNetworkList(id)
		if err != nil {
			return nil, fmt.Errorf("fetch access list %s: %w", id, err)
		}
		lists = append(lists, *detail)
	}
	return lists, nil
}

// GetNetworkList fetches an access list by its ID and caches NPM-specific metadata
// needed to preserve auth credentials and non-allow entries during updates.
func (c *NPMClient) GetNetworkList(listID string) (*NetworkList, error) {
	path := "/nginx/access-lists/" + strings.TrimSpace(listID) + "?expand=clients,items"
	body, err := c.doAuthedRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var detail npmAccessListDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		return nil, fmt.Errorf("decode access list: %w", err)
	}

	nl := &NetworkList{
		Type:       "IPV4_ADDRESSES",
		ID:         strconv.FormatInt(detail.ID, 10),
		Name:       detail.Name,
		SatisfyAny: detail.SatisfyAny,
		PassAuth:   detail.PassAuth,
	}

	for _, client := range detail.Clients {
		if strings.EqualFold(strings.TrimSpace(client.Directive), "allow") && strings.TrimSpace(client.Address) != "" {
			itemType := "IP_ADDRESS"
			if strings.Contains(client.Address, "/") {
				itemType = "SUBNET"
			}
			nl.Items = append(nl.Items, TrafficMatchItem{Type: itemType, Value: client.Address})
		}
	}

	// Cache NPM-specific metadata for use in UpdateNetworkList.
	c.mu.Lock()
	c.listMeta[nl.ID] = npmListMeta{
		satisfyAny: detail.SatisfyAny,
		passAuth:   detail.PassAuth,
		rawItems:   detail.Items,
		rawClients: detail.Clients,
	}
	c.mu.Unlock()

	return nl, nil
}

// UpdateNetworkList updates an NPM access list, preserving auth credentials and
// non-allow client entries that were fetched during the preceding GetNetworkList call.
func (c *NPMClient) UpdateNetworkList(nl *NetworkList) error {
	if nl == nil {
		return fmt.Errorf("network list is required")
	}

	c.mu.Lock()
	meta, ok := c.listMeta[nl.ID]
	c.mu.Unlock()

	if !ok {
		// No cached meta: fetch the list to populate the cache, then proceed.
		if _, err := c.GetNetworkList(nl.ID); err == nil {
			c.mu.Lock()
			meta = c.listMeta[nl.ID]
			c.mu.Unlock()
		}
	}

	// Preserve non-allow client entries (deny rules, etc.).
	nonAllowClients := make([]npmClient, 0, len(meta.rawClients))
	for _, existing := range meta.rawClients {
		if !strings.EqualFold(strings.TrimSpace(existing.Directive), "allow") {
			nonAllowClients = append(nonAllowClients, existing)
		}
	}

	allowClients := make([]npmClient, 0, len(nl.Items))
	for _, item := range nl.Items {
		value := strings.TrimSpace(item.Value)
		if value == "" {
			continue
		}
		allowClients = append(allowClients, npmClient{Address: value, Directive: "allow"})
	}

	payload, err := json.Marshal(npmAccessListUpdateRequest{
		Name:       nl.Name,
		SatisfyAny: meta.satisfyAny,
		PassAuth:   meta.passAuth,
		Items:      meta.rawItems,
		Clients:    append(nonAllowClients, allowClients...),
	})
	if err != nil {
		return fmt.Errorf("encode access list update: %w", err)
	}

	_, err = c.doAuthedRequest(http.MethodPut, "/nginx/access-lists/"+strings.TrimSpace(nl.ID), payload)
	return err
}
