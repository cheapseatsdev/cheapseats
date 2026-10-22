package audit

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"time"
)

// sitemapXML reads both a urlset and a sitemapindex.
type sitemapXML struct {
	XMLName xml.Name
	URLs    []struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod"`
	} `xml:"url"`
	Sitemaps []struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod"`
	} `xml:"sitemap"`
}

type sitemapEntry struct {
	Loc     string
	LastMod time.Time // zero when not given or unreadable
}

// parseSitemap reads a sitemap (gzipped or not): its pages, or for an index the sitemaps it lists.
func parseSitemap(body []byte) (urls, children []sitemapEntry, err error) {
	if len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		body, err = io.ReadAll(io.LimitReader(zr, 4*MaxBody))
		if err != nil {
			return nil, nil, err
		}
	}
	var s sitemapXML
	if err := xml.Unmarshal(body, &s); err != nil {
		return nil, nil, err
	}
	switch s.XMLName.Local {
	case "urlset":
		for _, u := range s.URLs {
			if loc := strings.TrimSpace(u.Loc); loc != "" {
				urls = append(urls, sitemapEntry{loc, lastMod(u.LastMod)})
			}
		}
	case "sitemapindex":
		for _, u := range s.Sitemaps {
			if loc := strings.TrimSpace(u.Loc); loc != "" {
				children = append(children, sitemapEntry{loc, lastMod(u.LastMod)})
			}
		}
	default:
		return nil, nil, errors.New("not a sitemap: the root element is <" + s.XMLName.Local + ">, not <urlset> or <sitemapindex>")
	}
	return urls, children, nil
}

// lastMod reads a W3C datetime: a date, or a date and time.
func lastMod(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02T15:04Z07:00", "2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
