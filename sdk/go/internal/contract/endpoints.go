package contract

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

var endpointParts = regexp.MustCompile(`^(https?)://(\[[0-9a-f:]+\]|[a-z0-9.-]+)(?::([0-9]+))?(/.*)?$`)
var endpointLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var endpointNumeric = regexp.MustCompile(`^(?:[0-9]+|0x[a-f0-9]+)$`)
var endpointPath = regexp.MustCompile(`^[A-Za-z0-9._~!$&'()*+,;=:%/-]*$`)
var endpointPort = regexp.MustCompile(`^[1-9][0-9]{0,4}$`)

// ValidateEndpoint preserves the original configured issuer string.
func ValidateEndpoint(value, profile string) error {
	if (profile != "production" && profile != "loopback") || len(value) > 2048 {
		return ErrInvalidArgument
	}
	for _, c := range value {
		if c < 0x21 || c > 0x7e || strings.ContainsRune("\\?#@", c) {
			return ErrInvalidArgument
		}
	}
	p := endpointParts.FindStringSubmatch(value)
	if p == nil {
		return ErrInvalidArgument
	}
	host := p[2]
	bracketed := strings.HasPrefix(host, "[")
	if bracketed {
		host = host[1 : len(host)-1]
	}
	ip := net.ParseIP(host)
	if bracketed {
		if ip == nil || !strings.Contains(host, ":") {
			return ErrInvalidArgument
		}
	} else if ip == nil {
		labels := strings.Split(host, ".")
		if len(host) > 253 || endpointNumeric.MatchString(labels[len(labels)-1]) {
			return ErrInvalidArgument
		}
		for _, label := range labels {
			if !endpointLabel.MatchString(label) {
				return ErrInvalidArgument
			}
		}
	}
	if p[3] != "" {
		port, err := strconv.Atoi(p[3])
		if err != nil || !endpointPort.MatchString(p[3]) || port > 65535 {
			return ErrInvalidArgument
		}
	}
	if p[1] == "http" && (profile != "loopback" || (host != "localhost" && host != "127.0.0.1" && host != "::1")) {
		return ErrInvalidArgument
	}
	if !endpointPath.MatchString(p[4]) {
		return ErrInvalidArgument
	}
	for _, segment := range strings.Split(p[4], "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || !utf8.ValidString(decoded) || decoded == "." || decoded == ".." {
			return ErrInvalidArgument
		}
		for _, c := range decoded {
			if c <= 0x20 || c == 0x7f || c == '/' || c == '\\' {
				return ErrInvalidArgument
			}
		}
	}
	if _, err := url.Parse(value); err != nil {
		return ErrInvalidArgument
	}
	return nil
}

type EndpointConfig struct {
	Issuer, TransportProfile string
	OpenFGA                  struct{ URL string }
}

func ValidateConfigEndpoints(c EndpointConfig) error {
	profile := c.TransportProfile
	if profile == "" {
		profile = "production"
	}
	if err := ValidateEndpoint(c.Issuer, profile); err != nil {
		return err
	}
	return ValidateEndpoint(c.OpenFGA.URL, profile)
}

var oidcClientID = regexp.MustCompile(`^[A-Za-z0-9_-]+(@[A-Za-z0-9_-]+)?$`)

// ValidClientID permits ZITADEL's project-qualified OAuth client identifiers.
func ValidClientID(value string) bool {
	return len(value) <= 128 && oidcClientID.MatchString(value)
}
