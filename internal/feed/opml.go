package feed

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// OPMLItem is one subscription found in an OPML document.
type OPMLItem struct {
	Title   string
	FeedURL string
	SiteURL string
	Type    string
}

// OPML is the normalized import result.
type OPML struct {
	Items   []OPMLItem
	Skipped int
}

// ParseOPML reads outline elements with an xmlUrl attribute. Only public
// HTTP(S) subscription URLs are kept; duplicates are collapsed after
// canonicalization.
func ParseOPML(data []byte) (OPML, error) {
	if bytes.Contains(bytes.ToUpper(data), []byte("<!ENTITY")) {
		return OPML{}, errors.New("opml: entity declarations are not supported")
	}
	root, err := ParseXML(data)
	if err != nil {
		return OPML{}, err
	}
	if !strings.EqualFold(root.Name, "opml") {
		return OPML{}, fmt.Errorf("opml: unexpected root <%s>", root.Name)
	}
	result := OPML{}
	seen := make(map[string]bool)
	var walk func(*Node)
	walk = func(node *Node) {
		if node == nil {
			return
		}
		if node.Name == "outline" {
			raw := node.Attr("xmlUrl")
			typ := strings.ToLower(node.Attr("type"))
			if raw != "" {
				canonical, err := CanonicalURL(raw)
				if err != nil {
					result.Skipped++
				} else if seen[canonical] {
					result.Skipped++
				} else {
					seen[canonical] = true
					title := strings.TrimSpace(node.Attr("title"))
					if title == "" {
						title = strings.TrimSpace(node.Attr("text"))
					}
					siteURL := ""
					if rawSite := node.Attr("htmlUrl"); rawSite != "" {
						siteURL, _ = CanonicalURL(rawSite)
					}
					result.Items = append(result.Items, OPMLItem{
						Title:   truncate(cleanText(title), 500),
						FeedURL: canonical,
						SiteURL: siteURL,
						Type:    typ,
					})
				}
			}
		}
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
	return result, nil
}

// CanonicalURL normalizes a public HTTP(S) URL for deduplication. Query
// strings are preserved because feed URLs can contain signed parameters.
func CanonicalURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("invalid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("only HTTP(S) subscriptions are supported")
	}
	if u.User != nil {
		return "", errors.New("URLs with credentials are not supported")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return "", errors.New("missing host")
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	hostport := host
	if port != "" {
		hostport = net.JoinHostPort(host, port)
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	u.Scheme = scheme
	u.Host = hostport
	u.Path = path
	u.RawPath = ""
	u.Fragment = ""
	return u.String(), nil
}
