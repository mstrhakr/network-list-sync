package npm

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
)

// NetworkList represents an NPM access list in the UI's existing shape.
type NetworkList struct {
	Type  string             `json:"type"`
	ID    string             `json:"id"`
	Name  string             `json:"name"`
	Items []TrafficMatchItem `json:"items,omitempty"`

	SatisfyAny bool `json:"-"`
	PassAuth   bool `json:"-"`

	RawItems   []accessListAuthItem `json:"-"`
	RawClients []accessListClient   `json:"-"`
}

// TrafficMatchItem is the app's internal representation for an IP/CIDR entry.
type TrafficMatchItem struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

type accessListSummary struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type accessListAuthItem struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type accessListClient struct {
	Address   string `json:"address,omitempty"`
	Directive string `json:"directive,omitempty"`
}

type accessListDetail struct {
	ID         int64                `json:"id"`
	Name       string               `json:"name"`
	SatisfyAny bool                 `json:"satisfy_any"`
	PassAuth   bool                 `json:"pass_auth"`
	Items      []accessListAuthItem `json:"items,omitempty"`
	Clients    []accessListClient   `json:"clients,omitempty"`
}

type tokenRequest struct {
	Identity string `json:"identity"`
	Secret   string `json:"secret"`
}

type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
}

type accessListUpdateRequest struct {
	Name       string               `json:"name"`
	SatisfyAny bool                 `json:"satisfy_any"`
	PassAuth   bool                 `json:"pass_auth"`
	Items      []accessListAuthItem `json:"items,omitempty"`
	Clients    []accessListClient   `json:"clients,omitempty"`
}

// Client is an authenticated HTTP client for the Nginx Proxy Manager API.
// Compatibility note: callers currently pass Site as identity and APIKey as secret.
type Client struct {
	baseURL    string
	apiBase    string
	identity   string
	secret     string
	token      string
	httpClient *http.Client
}

// NewClient creates a client for the NPM API.
func NewClient(baseURL, site, apiKey string, skipTLSVerify bool) (*Client, error) {
	identity := strings.TrimSpace(site)
	secret := strings.TrimSpace(apiKey)
	if identity == "" {
		return nil, fmt.Errorf("identity is required (currently provided via site field)")
	}
	if secret == "" {
		return nil, fmt.Errorf("secret is required (currently provided via api_key field)")
	}

	normalizedBaseURL, apiBase, err := normalizeControllerBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	return &Client{
		baseURL:  normalizedBaseURL,
		apiBase:  apiBase,
		identity: identity,
		secret:   secret,
		httpClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: skipTLSVerify},
			},
		},
	}, nil
}

func normalizeControllerBaseURL(raw string) (string, string, error) {
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

func (c *Client) authenticate() error {
	payload, err := json.Marshal(tokenRequest{Identity: c.identity, Secret: c.secret})
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

	var resp tokenResponse
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

func (c *Client) doRawRequest(method, path string, body []byte, bearerToken string) ([]byte, int, string, error) {
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

func (c *Client) doAuthedRequest(method, path string, body []byte) ([]byte, error) {
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
func (c *Client) ListNetworkLists() ([]NetworkList, error) {
	body, err := c.doAuthedRequest(http.MethodGet, "/nginx/access-lists", nil)
	if err != nil {
		return nil, err
	}
	var summaries []accessListSummary
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

// GetNetworkList fetches an access list by its ID.
func (c *Client) GetNetworkList(listID string) (*NetworkList, error) {
	path := "/nginx/access-lists/" + strings.TrimSpace(listID) + "?expand=clients,items"
	body, err := c.doAuthedRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	var detail accessListDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		return nil, fmt.Errorf("decode access list: %w", err)
	}

	nl := &NetworkList{
		Type:       "IPV4_ADDRESSES",
		ID:         strconv.FormatInt(detail.ID, 10),
		Name:       detail.Name,
		SatisfyAny: detail.SatisfyAny,
		PassAuth:   detail.PassAuth,
		RawItems:   detail.Items,
		RawClients: detail.Clients,
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
	return nl, nil
}

// UpdateNetworkList updates an NPM access list.
func (c *Client) UpdateNetworkList(nl *NetworkList) error {
	if nl == nil {
		return fmt.Errorf("network list is required")
	}

	nonAllowClients := make([]accessListClient, 0, len(nl.RawClients))
	for _, existing := range nl.RawClients {
		if !strings.EqualFold(strings.TrimSpace(existing.Directive), "allow") {
			nonAllowClients = append(nonAllowClients, existing)
		}
	}

	allowClients := make([]accessListClient, 0, len(nl.Items))
	for _, item := range nl.Items {
		value := strings.TrimSpace(item.Value)
		if value == "" {
			continue
		}
		allowClients = append(allowClients, accessListClient{Address: value, Directive: "allow"})
	}

	payload, err := json.Marshal(accessListUpdateRequest{
		Name:       nl.Name,
		SatisfyAny: nl.SatisfyAny,
		PassAuth:   nl.PassAuth,
		Items:      nl.RawItems,
		Clients:    append(nonAllowClients, allowClients...),
	})
	if err != nil {
		return fmt.Errorf("encode access list update: %w", err)
	}

	_, err = c.doAuthedRequest(http.MethodPut, "/nginx/access-lists/"+strings.TrimSpace(nl.ID), payload)
	return err
}
