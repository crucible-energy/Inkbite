package rssconv

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/LynnColeArt/Inkbite"
	htmlconv "github.com/LynnColeArt/Inkbite/converters/html"
)

const priority = 30

var (
	preciseExtensions = map[string]struct{}{
		".atom": {},
		".rss":  {},
	}
	preciseMIMETypes = map[string]struct{}{
		"application/atom":     {},
		"application/atom+xml": {},
		"application/rss":      {},
		"application/rss+xml":  {},
	}
	candidateExtensions = map[string]struct{}{
		".xml": {},
	}
	candidateMIMETypes = map[string]struct{}{
		"application/xml": {},
		"text/xml":        {},
	}
)

type rssFeed struct {
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Description string    `xml:"description"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title          string `xml:"title"`
	Description    string `xml:"description"`
	PubDate        string `xml:"pubDate"`
	ContentEncoded string `xml:"encoded"`
}

type atomFeed struct {
	Title    string      `xml:"title"`
	Subtitle string      `xml:"subtitle"`
	Entries  []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title   string `xml:"title"`
	Summary string `xml:"summary"`
	Content string `xml:"content"`
	Updated string `xml:"updated"`
}

// Converter extracts feed metadata and entries from RSS and Atom XML.
type Converter struct {
	html *htmlconv.Converter
}

// New returns an RSS/Atom converter.
func New() *Converter {
	return &Converter{
		html: htmlconv.New(),
	}
}

func (c *Converter) Name() string {
	return "rss"
}

func (c *Converter) Priority() float64 {
	return priority
}

func (c *Converter) Accepts(
	_ context.Context,
	r io.ReadSeeker,
	info inkbite.StreamInfo,
	_ inkbite.ConvertOptions,
) bool {
	if _, ok := preciseExtensions[info.Extension]; ok {
		return true
	}
	if _, ok := preciseMIMETypes[info.MIMEType]; ok {
		return true
	}
	if _, ok := candidateExtensions[info.Extension]; ok {
		root, err := rootElement(r)
		return err == nil && isFeedRoot(root)
	}
	if _, ok := candidateMIMETypes[info.MIMEType]; ok {
		root, err := rootElement(r)
		return err == nil && isFeedRoot(root)
	}
	return false
}

func (c *Converter) Convert(
	ctx context.Context,
	r io.ReadSeeker,
	info inkbite.StreamInfo,
	_ inkbite.ConvertOptions,
) (inkbite.Result, error) {
	if err := ctx.Err(); err != nil {
		return inkbite.Result{}, err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return inkbite.Result{}, err
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return inkbite.Result{}, err
	}

	decoded := inkbite.DecodeText(data, info.Charset)
	root, err := rootElementFromString(decoded)
	if err != nil {
		return inkbite.Result{}, err
	}

	switch root {
	case "rss":
		var feed rssFeed
		if err := xml.Unmarshal([]byte(decoded), &feed); err != nil {
			return inkbite.Result{}, err
		}
		return c.convertRSS(ctx, feed)
	case "feed":
		var feed atomFeed
		if err := xml.Unmarshal([]byte(decoded), &feed); err != nil {
			return inkbite.Result{}, err
		}
		return c.convertAtom(ctx, feed)
	default:
		return inkbite.Result{}, inkbite.UnsupportedFormatError{Info: info}
	}
}

func (c *Converter) convertRSS(ctx context.Context, feed rssFeed) (inkbite.Result, error) {
	var parts []string
	title := strings.TrimSpace(feed.Channel.Title)
	if title != "" {
		parts = append(parts, "# "+title)
	}
	desc, err := c.renderMaybeHTML(ctx, feed.Channel.Description)
	if err != nil {
		return inkbite.Result{}, fmt.Errorf("RSS channel description: %w", err)
	}
	if desc != "" {
		parts = append(parts, desc)
	}
	for _, item := range feed.Channel.Items {
		var section []string
		if title := strings.TrimSpace(item.Title); title != "" {
			section = append(section, "## "+title)
		}
		if pubDate := strings.TrimSpace(item.PubDate); pubDate != "" {
			section = append(section, "Published on: "+pubDate)
		}
		content := strings.TrimSpace(item.ContentEncoded)
		if content == "" {
			content = strings.TrimSpace(item.Description)
		}
		rendered, err := c.renderMaybeHTML(ctx, content)
		if err != nil {
			return inkbite.Result{}, fmt.Errorf("RSS item content: %w", err)
		}
		if rendered != "" {
			section = append(section, rendered)
		}
		if len(section) > 0 {
			parts = append(parts, strings.Join(section, "\n\n"))
		}
	}

	return inkbite.Result{
		Markdown: strings.Join(parts, "\n\n"),
		Title:    title,
	}, nil
}

func (c *Converter) convertAtom(ctx context.Context, feed atomFeed) (inkbite.Result, error) {
	var parts []string
	title := strings.TrimSpace(feed.Title)
	if title != "" {
		parts = append(parts, "# "+title)
	}
	subtitle, err := c.renderMaybeHTML(ctx, feed.Subtitle)
	if err != nil {
		return inkbite.Result{}, fmt.Errorf("Atom subtitle: %w", err)
	}
	if subtitle != "" {
		parts = append(parts, subtitle)
	}
	for _, entry := range feed.Entries {
		var section []string
		if title := strings.TrimSpace(entry.Title); title != "" {
			section = append(section, "## "+title)
		}
		if updated := strings.TrimSpace(entry.Updated); updated != "" {
			section = append(section, "Updated on: "+updated)
		}
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			content = strings.TrimSpace(entry.Summary)
		}
		rendered, err := c.renderMaybeHTML(ctx, content)
		if err != nil {
			return inkbite.Result{}, fmt.Errorf("Atom entry content: %w", err)
		}
		if rendered != "" {
			section = append(section, rendered)
		}
		if len(section) > 0 {
			parts = append(parts, strings.Join(section, "\n\n"))
		}
	}

	return inkbite.Result{
		Markdown: strings.Join(parts, "\n\n"),
		Title:    title,
	}, nil
}

func (c *Converter) renderMaybeHTML(ctx context.Context, value string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.Contains(value, "<") && strings.Contains(value, ">") {
		result, err := c.html.Convert(ctx, strings.NewReader(value), inkbite.StreamInfo{}, inkbite.ConvertOptions{})
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(result.Markdown), nil
	}
	return value, nil
}

func rootElement(r io.ReadSeeker) (string, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return rootElementFromString(string(data))
}

func rootElementFromString(value string) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(value))
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", err
		}
		if start, ok := token.(xml.StartElement); ok {
			return strings.ToLower(start.Name.Local), nil
		}
	}
}

func isFeedRoot(root string) bool {
	return root == "feed" || root == "rss"
}
