package namecheapplugin

// client.go is the one file that talks to Namecheap's XML API. It is ported
// from the compiled-in connector, with two differences: API errors come back
// as *APIError, carrying Namecheap's error numbers so the plugin can code
// them, and nothing here redacts. The plugin scrubs every error at its
// boundary (scrub.go), because the API key travels in the query string and
// net/http prints the URL.

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIBaseURL is Namecheap's production XML endpoint.
const APIBaseURL = "https://api.namecheap.com/xml.response"

// SandboxBaseURL is Namecheap's sandbox XML endpoint. It needs a separate
// sandbox account and key; production credentials do not work there.
const SandboxBaseURL = "https://api.sandbox.namecheap.com/xml.response"

// BaseURL is the endpoint a client calls.
func BaseURL(sandbox bool) string {
	if sandbox {
		return SandboxBaseURL
	}
	return APIBaseURL
}

// Client wraps HTTP calls to the Namecheap XML API.
type Client struct {
	baseURL  string
	http     *http.Client
	apiUser  string
	apiKey   string
	username string
	clientIP string
}

// NewClient creates a Namecheap API client.
func NewClient(apiUser, apiKey, username, clientIP string) *Client {
	return &Client{
		baseURL:  APIBaseURL,
		http:     &http.Client{Timeout: 30 * time.Second},
		apiUser:  apiUser,
		apiKey:   apiKey,
		username: username,
		clientIP: clientIP,
	}
}

// --- XML response structs ---

type domainsGetListResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	Status  string   `xml:"Status,attr"`
	Errors  struct {
		Error []struct {
			Number  string `xml:"Number,attr"`
			Message string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
	CommandResponse struct {
		DomainGetListResult struct {
			Domains []xmlDomain `xml:"Domain"`
		} `xml:"DomainGetListResult"`
	} `xml:"CommandResponse"`
}

type xmlDomain struct {
	ID         string `xml:"ID,attr"`
	Name       string `xml:"Name,attr"`
	Expires    string `xml:"Expires,attr"`
	IsExpired  string `xml:"IsExpired,attr"`
	IsLocked   string `xml:"IsLocked,attr"`
	AutoRenew  string `xml:"AutoRenew,attr"`
	WhoisGuard string `xml:"WhoisGuard,attr"`
}

type domainsGetInfoResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	Status  string   `xml:"Status,attr"`
	Errors  struct {
		Error []struct {
			Number  string `xml:"Number,attr"`
			Message string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
	CommandResponse struct {
		DomainGetInfoResult struct {
			Status        string `xml:"Status,attr"`
			DomainName    string `xml:"DomainName,attr"`
			DomainDetails struct {
				ExpiredDate string `xml:"ExpiredDate"`
			} `xml:"DomainDetails"`
			DNSDetails struct {
				NameServers []string `xml:"Nameserver"`
			} `xml:"DnsDetails"`
		} `xml:"DomainGetInfoResult"`
	} `xml:"CommandResponse"`
}

type dnsGetHostsResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	Status  string   `xml:"Status,attr"`
	Errors  struct {
		Error []struct {
			Number  string `xml:"Number,attr"`
			Message string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
	CommandResponse struct {
		DomainDNSGetHostsResult struct {
			Hosts      []xmlHost `xml:"host"`
			UpperHosts []xmlHost `xml:"Host"`
			EmailType  string    `xml:"EmailType,attr"`
		} `xml:"DomainDNSGetHostsResult"`
	} `xml:"CommandResponse"`
}

type dnsSetCustomResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	Status  string   `xml:"Status,attr"`
	Errors  struct {
		Error []struct {
			Number  string `xml:"Number,attr"`
			Message string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
	CommandResponse struct {
		DomainDNSSetCustomResult struct {
			Domain  string `xml:"Domain,attr"`
			Updated string `xml:"Updated,attr"`
		} `xml:"DomainDNSSetCustomResult"`
	} `xml:"CommandResponse"`
}

type xmlHost struct {
	HostID  string `xml:"HostId,attr"`
	Name    string `xml:"Name,attr"`
	Type    string `xml:"Type,attr"`
	Address string `xml:"Address,attr"`
	TTL     string `xml:"TTL,attr"`
	MXPref  string `xml:"MXPref,attr"`
}

// --- API methods ---

// ListDomains returns all domains in the account.
func (c *Client) ListDomains(ctx context.Context) ([]Domain, error) {
	body, err := c.doRequest(ctx, "namecheap.domains.getList", nil)
	if err != nil {
		return nil, fmt.Errorf("namecheap list domains: %w", err)
	}

	var resp domainsGetListResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("namecheap list domains: parse xml: %w", err)
	}
	if resp.Status != "OK" {
		return nil, newAPIError("namecheap list domains", resp.Errors.Error)
	}

	domains := make([]Domain, len(resp.CommandResponse.DomainGetListResult.Domains))
	for i, d := range resp.CommandResponse.DomainGetListResult.Domains {
		domains[i] = Domain{
			Name:       d.Name,
			Expires:    d.Expires,
			IsExpired:  parseBool(d.IsExpired),
			IsLocked:   parseBool(d.IsLocked),
			AutoRenew:  parseBool(d.AutoRenew),
			WhoisGuard: d.WhoisGuard,
		}
	}
	return domains, nil
}

// GetDomainStatus returns detailed info for a single domain.
func (c *Client) GetDomainStatus(ctx context.Context, domain string) (*DomainStatus, error) {
	body, err := c.doRequest(ctx, "namecheap.domains.getInfo", map[string]string{
		"DomainName": domain,
	})
	if err != nil {
		return nil, fmt.Errorf("namecheap domain status: %w", err)
	}

	var resp domainsGetInfoResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("namecheap domain status: parse xml: %w", err)
	}
	if resp.Status != "OK" {
		return nil, newAPIError("namecheap domain status", resp.Errors.Error)
	}

	info := resp.CommandResponse.DomainGetInfoResult
	registered := strings.EqualFold(info.Status, "Ok") || strings.EqualFold(info.Status, "Active")

	return &DomainStatus{
		Domain:      info.DomainName,
		Registered:  registered,
		Expires:     info.DomainDetails.ExpiredDate,
		NameServers: info.DNSDetails.NameServers,
	}, nil
}

// ListDNSRecords returns DNS host records for a domain.
// The Namecheap API requires the domain split into SLD and TLD.
func (c *Client) ListDNSRecords(ctx context.Context, sld, tld string) ([]DNSRecord, error) {
	set, err := c.GetDNSRecordSet(ctx, sld, tld)
	if err != nil {
		return nil, err
	}
	return set.Records, nil
}

func (c *Client) GetDNSRecordSet(ctx context.Context, sld, tld string) (*DNSRecordSet, error) {
	body, err := c.doRequest(ctx, "namecheap.domains.dns.getHosts", map[string]string{
		"SLD": sld,
		"TLD": tld,
	})
	if err != nil {
		return nil, fmt.Errorf("namecheap list dns: %w", err)
	}

	var resp dnsGetHostsResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("namecheap list dns: parse xml: %w", err)
	}
	if resp.Status != "OK" {
		return nil, newAPIError("namecheap list dns", resp.Errors.Error)
	}

	result := resp.CommandResponse.DomainDNSGetHostsResult
	hosts := append(result.Hosts, result.UpperHosts...)
	records := make([]DNSRecord, len(hosts))
	for i, h := range hosts {
		id, _ := strconv.Atoi(h.HostID)
		ttl, _ := strconv.Atoi(h.TTL)
		mxPref, _ := strconv.Atoi(h.MXPref)
		records[i] = DNSRecord{
			ID:     id,
			Type:   h.Type,
			Host:   h.Name,
			Value:  h.Address,
			TTL:    ttl,
			MXPref: mxPref,
		}
	}
	return &DNSRecordSet{EmailType: result.EmailType, Records: records}, nil
}

// SetDNSRecords replaces the full DNS host record set for a domain.
func (c *Client) SetDNSRecords(ctx context.Context, sld, tld string, records []DNSRecord) error {
	current, err := c.GetDNSRecordSet(ctx, sld, tld)
	if err != nil {
		return err
	}
	return c.SetDNSRecordSet(ctx, sld, tld, DNSRecordSet{EmailType: current.EmailType, Records: records})
}

func (c *Client) SetDNSRecordSet(ctx context.Context, sld, tld string, set DNSRecordSet) error {
	if err := set.Validate(); err != nil {
		return err
	}
	params := map[string]string{
		"SLD":       sld,
		"TLD":       tld,
		"EmailType": set.EmailType,
	}
	for i, record := range set.Records {
		n := strconv.Itoa(i + 1)
		params["HostName"+n] = record.Host
		params["RecordType"+n] = record.Type
		params["Address"+n] = record.Value
		if record.TTL > 0 {
			params["TTL"+n] = strconv.Itoa(record.TTL)
		}
		if record.MXPref > 0 {
			params["MXPref"+n] = strconv.Itoa(record.MXPref)
		}
	}

	body, err := c.doRequest(ctx, "namecheap.domains.dns.setHosts", params)
	if err != nil {
		return fmt.Errorf("namecheap set dns: %w", err)
	}

	var resp struct {
		XMLName         xml.Name `xml:"ApiResponse"`
		Status          string   `xml:"Status,attr"`
		CommandResponse struct {
			Result struct {
				IsSuccess string `xml:"IsSuccess,attr"`
			} `xml:"DomainDNSSetHostsResult"`
		} `xml:"CommandResponse"`
		Errors struct {
			Error []struct {
				Number  string `xml:"Number,attr"`
				Message string `xml:",chardata"`
			} `xml:"Error"`
		} `xml:"Errors"`
	}
	if err := xml.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("namecheap set dns: parse xml: %w", err)
	}
	if resp.Status != "OK" {
		return newAPIError("namecheap set dns", resp.Errors.Error)
	}
	if !parseBool(resp.CommandResponse.Result.IsSuccess) {
		return fmt.Errorf("namecheap set dns: API did not confirm IsSuccess")
	}
	return nil
}

// SetCustomNameservers switches a domain to the provided nameserver set.
func (c *Client) SetCustomNameservers(ctx context.Context, domain string, nameservers []string) (*DomainNameserverUpdate, error) {
	sld, tld, err := SplitDomain(domain)
	if err != nil {
		return nil, err
	}
	clean := make([]string, 0, len(nameservers))
	for _, ns := range nameservers {
		ns = strings.TrimSpace(ns)
		if ns == "" {
			continue
		}
		clean = append(clean, ns)
	}
	if len(clean) < 2 {
		return nil, fmt.Errorf("namecheap set custom nameservers: at least two nameservers are required")
	}

	body, err := c.doRequest(ctx, "namecheap.domains.dns.setCustom", map[string]string{
		"SLD":         sld,
		"TLD":         tld,
		"NameServers": strings.Join(clean, ","),
	})
	if err != nil {
		return nil, fmt.Errorf("namecheap set custom nameservers: %w", err)
	}

	var resp dnsSetCustomResponse
	if err := xml.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("namecheap set custom nameservers: parse xml: %w", err)
	}
	if resp.Status != "OK" {
		return nil, newAPIError("namecheap set custom nameservers", resp.Errors.Error)
	}

	return &DomainNameserverUpdate{
		Domain:      resp.CommandResponse.DomainDNSSetCustomResult.Domain,
		Updated:     parseBool(resp.CommandResponse.DomainDNSSetCustomResult.Updated),
		NameServers: clean,
	}, nil
}

// --- internal helpers ---

func (c *Client) doRequest(ctx context.Context, command string, extra map[string]string) ([]byte, error) {
	params := url.Values{}
	params.Set("ApiUser", c.apiUser)
	params.Set("ApiKey", c.apiKey)
	params.Set("UserName", c.username)
	params.Set("ClientIp", c.clientIP)
	params.Set("Command", command)
	for k, v := range extra {
		params.Set(k, v)
	}

	reqURL := c.baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

func parseBool(s string) bool {
	return strings.EqualFold(s, "true")
}

type xmlError = struct {
	Number  string `xml:"Number,attr"`
	Message string `xml:",chardata"`
}

// APIError is Namecheap reporting a command as failed. Numbers are Namecheap's
// own error numbers, which is how the plugin tells a rejected key or an
// address missing from the API allow-list from any other failure.
type APIError struct {
	Action  string
	Numbers []string
	Message string
}

func (e *APIError) Error() string { return e.Action + ": " + e.Message }

func newAPIError(action string, errors []xmlError) *APIError {
	e := &APIError{Action: action}
	if len(errors) == 0 {
		e.Message = "unknown API error"
		return e
	}
	msgs := make([]string, len(errors))
	for i, x := range errors {
		msgs[i] = strings.TrimSpace(x.Message)
		e.Numbers = append(e.Numbers, strings.TrimSpace(x.Number))
	}
	e.Message = strings.Join(msgs, "; ")
	return e
}
