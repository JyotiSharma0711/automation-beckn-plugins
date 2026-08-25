package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/beckn-one/beckn-onix/pkg/model"
	httprequestremap "github.com/extedcouD/HttpRequestRemapper"

	"gopkg.in/yaml.v3"
)

// Config holds the configuration for the Router plugin.
type Config struct {
	RoutingConfig string `json:"routingConfig"`
}

// RoutingConfig represents the structure of the routing configuration file.
type routingConfig struct {
	RoutingRules []routingRule `yaml:"routingRules"`
}

// Router implements Router interface.
type Router struct {
	rules        map[string]map[string]map[string]*model.Route // domain -> version -> endpoint -> route
	careURL      *url.URL                                      // parsed CARE_URL; nil when not configured
	fisTunnelURL *url.URL                                      // parsed FIS_TUNNEL_URL; nil when not configured
	gatewayURL   *url.URL                                      // parsed GATEWAY_URL; nil when not configured
}

// RoutingRule represents a single routing rule.
type routingRule struct {
	Domain      string   `yaml:"domain"`
	Version     string   `yaml:"version"`
	TargetType  string   `yaml:"targetType"` // "url", "publisher", "bpp", or "bap"
	Target      target   `yaml:"target,omitempty"`
	Endpoints   []string `yaml:"endpoints"`
	ActAsProxy  *bool     `yaml:"actAsProxy,omitempty"`
}

// Target contains destination-specific details.
type target struct {
	URL           string `yaml:"url,omitempty"`           // URL for "url" or gateway endpoint for "bpp"/"bap"
	JsonPath      string `yaml:"jsonPath,omitempty"`     // JSONPath to extract URL from http request"
	PublisherID   string `yaml:"publisherId,omitempty"`   // For "msgq" type
	ExcludeAction bool   `yaml:"excludeAction,omitempty"` // For "url" type to exclude appending action to URL path
}

// TargetType defines possible target destinations.
const (
	targetTypeURL       = "url"       // Route to a specific URL
	targetTypeJSONPath  = "jsonPath"  // Route to a URL extracted via JSONPath
	targetTypePublisher = "publisher" // Route to a publisher
	targetTypeBPP       = "bpp"       // Route to a BPP endpoint
	targetTypeBAP       = "bap"       // Route to a BAP endpoint
)

// careEndpoints are the IGM endpoints routed to CARE_URL when use_care is enabled.
var careEndpoints = map[string]bool{
	"issue":           true,
	"on_issue":        true,
	"issue_status":    true,
	"on_issue_status": true,
}

// New initializes a new Router instance with the provided configuration.
// It loads and validates the routing rules from the specified YAML file.
// Returns an error if the configuration is invalid or the rules cannot be loaded.
func New(ctx context.Context, config *Config) (*Router, func() error, error) {
	// Check if config is nil
	if config == nil {
		return nil, nil, fmt.Errorf("config cannot be nil")
	}
	router := &Router{
		rules: make(map[string]map[string]map[string]*model.Route),
	}

	if careURLStr := strings.TrimSpace(os.Getenv("CARE_URL")); careURLStr != "" {
		parsed, err := url.Parse(careURLStr)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid CARE_URL %q: %w", careURLStr, err)
		}
		router.careURL = parsed
	}

	if fisTunnelURLStr := strings.TrimSpace(os.Getenv("FIS_TUNNEL_URL")); fisTunnelURLStr != "" {
		parsed, err := url.Parse(fisTunnelURLStr)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid FIS_TUNNEL_URL %q: %w", fisTunnelURLStr, err)
		}
		router.fisTunnelURL = parsed
		fmt.Printf("[use_tunnel_for_fis] FIS_TUNNEL_URL configured: %s\n", parsed.String())
	} else {
		fmt.Printf("[use_tunnel_for_fis] FIS_TUNNEL_URL not set\n")
	}

	if gatewayURLStr := strings.TrimSpace(os.Getenv("GATEWAY_URL")); gatewayURLStr != "" {
		parsed, err := url.Parse(gatewayURLStr)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid GATEWAY_URL %q: %w", gatewayURLStr, err)
		}
		router.gatewayURL = parsed
		fmt.Printf("[use_gateway] GATEWAY_URL configured: %s\n", parsed.String())
	} else {
		fmt.Printf("[use_gateway] GATEWAY_URL not set\n")
	}

	// Load rules at bootup
	if err := router.loadRules(config.RoutingConfig); err != nil {
		return nil, nil, fmt.Errorf("failed to load routing rules: %w", err)
	}
	return router, nil, nil
}

// LoadRules reads and parses routing rules from the YAML configuration file.
func (r *Router) loadRules(configPath string) error {
	if configPath == "" {
		return fmt.Errorf("routingConfig path is empty")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("error reading config file at %s: %w", configPath, err)
	}
	var config routingConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("error parsing YAML: %w", err)
	}


	// Validate rules
	if err := validateRules(config.RoutingRules); err != nil {
		return fmt.Errorf("invalid routing rules: %w", err)
	}
	// Build the optimized rule map
	for _, rule := range config.RoutingRules {
		// For v2.x.x, warn if domain is provided and normalize to wildcard "*"
		domain := rule.Domain
		// if isV2Version(rule.Version) {
		// 	if domain != "" {
		// 		fmt.Printf("WARNING: Domain field '%s' is not needed for version %s and will be ignored. Consider removing it from your config.\n", domain, rule.Version)
		// 	}
		// 	domain = "*"
		// }

		if(rule.ActAsProxy == nil){
			rule.ActAsProxy = new(bool)
			*rule.ActAsProxy = true
		}
		fmt.Printf("Routing rule loaded: domain=%s, version=%s, targetType=%s, endpoints=%v, actAsProxy=%v\n",
			domain, rule.Version, rule.TargetType, rule.Endpoints, *rule.ActAsProxy)

		// Initialize domain map if not exists
		if _, ok := r.rules[domain]; !ok {
			r.rules[domain] = make(map[string]map[string]*model.Route)
		}

		// Initialize version map if not exists
		if _, ok := r.rules[domain][rule.Version]; !ok {
			r.rules[domain][rule.Version] = make(map[string]*model.Route)
		}

		// Add all endpoints for this rule
		for _, endpoint := range rule.Endpoints {
			var route *model.Route
			switch rule.TargetType {
			case targetTypePublisher:
				route = &model.Route{
					TargetType:  rule.TargetType,
					PublisherID: rule.Target.PublisherID,
					ActAsProxy:  *rule.ActAsProxy,
				}
			case targetTypeURL:
				parsedURL, err := url.Parse(rule.Target.URL)
				if err != nil {
					return fmt.Errorf("invalid URL in rule: %w", err)
				}
				if !rule.Target.ExcludeAction {
					parsedURL.Path = joinPath(parsedURL, endpoint)
				}
				route = &model.Route{
					TargetType: rule.TargetType,
					URL:        parsedURL,
					ActAsProxy: *rule.ActAsProxy,
				}
			case targetTypeBPP, targetTypeBAP:
				var parsedURL *url.URL
				if rule.Target.URL != "" {
					parsedURL, err = url.Parse(rule.Target.URL)
					if err != nil {
						return fmt.Errorf("invalid URL in rule: %w", err)
					}
					parsedURL.Path = joinPath(parsedURL, endpoint)
				}
				route = &model.Route{
					TargetType: rule.TargetType,
					URL:        parsedURL,
					ActAsProxy: *rule.ActAsProxy,
				}
			case targetTypeJSONPath:
				route = &model.Route{
					TargetType: rule.TargetType,
					JsonPath:  rule.Target.JsonPath,
					ActAsProxy: *rule.ActAsProxy,
					URL:        nil,
				}
			}
			// Check for conflicting v2 rules
			// if isV2Version(rule.Version) {
			// 	if _, exists := r.rules[domain][rule.Version][endpoint]; exists {
			// 		return fmt.Errorf("duplicate endpoint '%s' found for version %s. For v2.x.x, domain is ignored, so you can only define each endpoint once per version. Please remove the duplicate rule", endpoint, rule.Version)
			// 	}
			// }
			r.rules[domain][rule.Version][endpoint] = route
			fmt.Printf("  Mapped endpoint '%s' to route: %+v\n", endpoint, route)
		}
	}

	return nil
}

// validateRules performs basic validation on the loaded routing rules.
func validateRules(rules []routingRule) error {
	for _, rule := range rules {
		// Ensure version and TargetType are present
		if rule.Version == "" || rule.TargetType == "" {
			return fmt.Errorf("invalid rule: version and targetType are required")
		}

		// Domain is required only for v1.x.x
		// if !isV2Version(rule.Version) && rule.Domain == "" {
		// 	return fmt.Errorf("invalid rule: domain is required for version %s", rule.Version)
		// }

		// Validate based on TargetType
		switch rule.TargetType {
		case targetTypeURL:
			if rule.Target.URL == "" {
				return fmt.Errorf("invalid rule: url is required for targetType 'url'")
			}
			if _, err := url.Parse(rule.Target.URL); err != nil {
				return fmt.Errorf("invalid URL - %s: %w", rule.Target.URL, err)
			}
		case targetTypePublisher:
			if rule.Target.PublisherID == "" {
				return fmt.Errorf("invalid rule: publisherID is required for targetType 'publisher'")
			}
		case targetTypeBPP, targetTypeBAP:
			if rule.Target.URL != "" {
				if _, err := url.Parse(rule.Target.URL); err != nil {
					return fmt.Errorf("invalid URL - %s defined in routing config for target type %s: %w", rule.Target.URL, rule.TargetType, err)
				}
			}
			continue
		case targetTypeJSONPath:
			if rule.Target.JsonPath == "" {
				return fmt.Errorf("invalid rule: jsonPath is required for targetType 'jsonPath'")
			}
		default:
			return fmt.Errorf("invalid rule: unknown targetType '%s'", rule.TargetType)
		}
	}
	return nil
}

// Route determines the routing destination based on the request context.
func (r *Router) Route(ctx context.Context, url *url.URL, body []byte, request *http.Request) (*model.Route, error) {
	// Parse the body to extract domain and version
	var requestBody struct {
		Context struct {
			Domain  string `json:"domain"`
			Version string `json:"version,omitempty"`
			CoreVersion string `json:"core_version,omitempty"`
			BPPURI  string `json:"bpp_uri,omitempty"`
			BAPURI  string `json:"bap_uri,omitempty"`
		} `json:"context"`
	}
	if err := json.Unmarshal(body, &requestBody); err != nil {
		return nil, fmt.Errorf("error parsing request body: %w", err)
	}

	// Extract the endpoint from the URL
	endpoint := path.Base(url.Path)

	version := requestBody.Context.Version
	if(version == ""){
		version = requestBody.Context.CoreVersion
	}

	// For v2.x.x, ignore domain and use wildcard; for v1.x.x, use actual domain
	domain := requestBody.Context.Domain
	// if isV2Version(version) {
	// 	domain = "*"
	// }

	// Lookup route in the optimized map
	domainRules, ok := r.rules[domain]
	if !ok {
		if domain == "*" {
			return nil, fmt.Errorf("no routing rules found for version %s", version)
		}
		return nil, fmt.Errorf("no routing rules found for domain %s", requestBody.Context.Domain)
	}

	versionRules, ok := domainRules[version]
	if !ok {
		if domain == "*" {
			return nil, fmt.Errorf("no routing rules found for version %s", version)
		}
		return nil, fmt.Errorf("no routing rules found for domain %s version %s", requestBody.Context.Domain, version)
	}

	route, ok := versionRules[endpoint]
	if !ok {
		if domain == "*" {
			return nil, fmt.Errorf("endpoint '%s' is not supported for version %s in routing config", endpoint, version)
		}
		return nil, fmt.Errorf("endpoint '%s' is not supported for domain %s and version %s in routing config",
			endpoint, requestBody.Context.Domain, version)
	}

	// useCare override: route to CARE_URL only for IGM endpoints (issue, on_issue,
	// issue_status, on_issue_status) when the session has use_care enabled AND the
	// matched route acts as a proxy.
	if request != nil && route.ActAsProxy && careEndpoints[endpoint] {
		if c, err := request.Cookie("use_care"); err == nil && c.Value == "true" {
			if r.careURL == nil {
				return nil, fmt.Errorf("use_care enabled but CARE_URL not configured")
			}
			target := *r.careURL
			target.Path = joinPath(&target, endpoint)
			return &model.Route{
				TargetType: targetTypeURL,
				URL:        &target,
				ActAsProxy: true,
			}, nil
		}
	}

	// useGateway override: route to GATEWAY_URL only for the search endpoint
	// when the session has use_gateway enabled AND the matched route acts as a proxy.
	// Checked before use_tunnel_for_fis so the search-specific rule wins over the
	// catch-all tunnel override.
	if request != nil && route.ActAsProxy && endpoint == "search" {
		if c, err := request.Cookie("use_gateway"); err == nil && c.Value == "true" {
			if r.gatewayURL == nil {
				return nil, fmt.Errorf("use_gateway enabled but GATEWAY_URL not configured")
			}
			target := *r.gatewayURL
			target.Path = joinPath(&target, endpoint)
			return &model.Route{
				TargetType: targetTypeURL,
				URL:        &target,
				ActAsProxy: true,
			}, nil
		}
	}

	// useTunnelForFis override: route to FIS_TUNNEL_URL when the session has it
	// enabled AND the matched route acts as a proxy.
	// If bpp_uri is present in the request body, send directly to the NP instead
	// of routing through the tunnel.
	if request != nil && route.ActAsProxy {
		if c, err := request.Cookie("use_tunnel_for_fis"); err == nil && c.Value == "true" {
			if strings.TrimSpace(requestBody.Context.BPPURI) != "" {
				// bpp_uri is present — route directly to NP, skip the tunnel
				return handleProtocolMapping(route, requestBody.Context.BPPURI, endpoint)
			} else {
				// No bpp_uri — route through FIS tunnel
				if r.fisTunnelURL == nil {
					return nil, fmt.Errorf("use_tunnel_for_fis enabled but FIS_TUNNEL_URL not configured")
				}
				target := *r.fisTunnelURL
				target.Path = joinPath(&target, endpoint)
				return &model.Route{
					TargetType: targetTypeURL,
					URL:        &target,
					ActAsProxy: true,
				}, nil
			}
		}
	}

	// Handle BPP/BAP routing with request URIs
	switch route.TargetType {
	case targetTypeBPP:
		return handleProtocolMapping(route, requestBody.Context.BPPURI, endpoint)
	case targetTypeBAP:
		return handleProtocolMapping(route, requestBody.Context.BAPURI, endpoint)
	case targetTypeJSONPath:
		value, err := GetValueFromRequest(request, route.JsonPath)
		if err != nil {
			return nil, fmt.Errorf("failed to extract URL using JSONPath '%s': %w", route.JsonPath, err)
		}
		urlStr, ok := value.(string)
		if !ok || strings.TrimSpace(urlStr) == "" {
			return nil, fmt.Errorf("extracted value using JSONPath '%s' is not a valid non-empty string", route.JsonPath)
		}
		targetURL, err := url.Parse(urlStr)
		if err != nil {
			return nil, fmt.Errorf("invalid URL extracted using JSONPath '%s': %w", route.JsonPath, err)
		}
		targetURL.Path = joinPath(targetURL, endpoint)
		return &model.Route{
			TargetType:  targetTypeURL,
			URL:         targetURL,
			ActAsProxy:  route.ActAsProxy,
		},nil
	}

	return route, nil
}

// handleProtocolMapping handles both BPP and BAP routing with proper URL construction
func handleProtocolMapping(route *model.Route, npURI, endpoint string) (*model.Route, error) {
	target := strings.TrimSpace(npURI)
	if len(target) == 0 {
		if route.URL == nil {
			return nil, fmt.Errorf("could not determine destination for endpoint '%s': neither request contained a %s URI nor was a default URL configured in routing rules", endpoint, strings.ToUpper(route.TargetType))
		}
		return &model.Route{
			TargetType:  targetTypeURL,
			URL:         route.URL,
			ActAsProxy:  route.ActAsProxy,
		}, nil
	}
	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("invalid %s URI - %s in request body for %s: %w", strings.ToUpper(route.TargetType), target, endpoint, err)
	}
	targetURL.Path = joinPath(targetURL, endpoint)
	return &model.Route{
		TargetType:  targetTypeURL,
		URL:         targetURL,
		ActAsProxy:  route.ActAsProxy,
	}, nil
}

func joinPath(u *url.URL, endpoint string) string {
	if u.Path == "" {
		u.Path = "/"
	}
	return path.Join(u.Path, endpoint)
}


func GetValueFromRequest(r *http.Request, jsonPath string) (any, error) {
	return httprequestremap.EvalJSONPathFromRequest(r, jsonPath, nil), nil
}
