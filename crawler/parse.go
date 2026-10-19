package crawler

import (
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

type assetRef struct{ url, typ string }

type parsedPage struct {
	seo    SEO
	links  []string
	assets []assetRef
}

func parseHTML(r io.Reader, base *url.URL) (parsedPage, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return parsedPage{}, err
	}

	var p parsedPage
	seenLinks := map[string]bool{}
	seenAssets := map[string]bool{}

	addLink := func(href string) {
		u := resolve(base, href)
		if u == "" || seenLinks[u] {
			return
		}
		seenLinks[u] = true
		p.links = append(p.links, u)
	}
	addAsset := func(ref, typ string) {
		u := resolve(base, ref)
		if u == "" || seenAssets[u] {
			return
		}
		seenAssets[u] = true
		p.assets = append(p.assets, assetRef{url: u, typ: typ})
	}

	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Namespace == "" {
			switch n.Data {
			case "title":
				if !p.seo.HasTitle {
					p.seo.HasTitle = true
					p.seo.Title = clean(textOf(n))
				}
			case "meta":
				if strings.EqualFold(attr(n, "name"), "description") && !p.seo.HasDescription {
					p.seo.HasDescription = true
					p.seo.Description = clean(attr(n, "content"))
				}
			case "h1":
				p.seo.HasH1 = true
			case "a":
				addLink(attr(n, "href"))
			case "img":
				addAsset(attr(n, "src"), "image")
			case "script":
				addAsset(attr(n, "src"), "script")
			case "link":
				for _, rel := range strings.Fields(strings.ToLower(attr(n, "rel"))) {
					if rel == "stylesheet" {
						addAsset(attr(n, "href"), "style")
						break
					}
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	walk(doc)
	return p, nil
}

// resolve превращает href в абсолютный http(s) URL без фрагмента.
// Пустые значения, якоря и прочие схемы (mailto:, javascript:, ftp:...) дают "".
func resolve(base *url.URL, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := base.Parse(href)
	if err != nil {
		return ""
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func textOf(n *html.Node) string {
	var sb strings.Builder
	var rec func(*html.Node)
	rec = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			rec(ch)
		}
	}
	rec(n)
	return sb.String()
}

func clean(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
