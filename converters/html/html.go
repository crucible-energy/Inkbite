package htmlconv

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"golang.org/x/net/html"

	"github.com/LynnColeArt/Inkbite"
)

const priority = 40

const (
	maxHTMLBytes      = 32 << 20
	maxHTMLNodes      = 65536
	maxHTMLDepth      = 256
	maxTableSpan      = 32
	maxTableGridCells = 65536
)

// ErrHTMLLimit identifies an input or table expansion outside the bounded profile.
var ErrHTMLLimit = fmt.Errorf("HTML: %w", inkbite.ErrResourceLimit)

var (
	htmlExtensions = map[string]struct{}{
		".htm":  {},
		".html": {},
	}
	htmlMIMETypes = map[string]struct{}{
		"application/xhtml+xml": {},
		"text/html":             {},
	}
)

// Converter transforms HTML documents into Markdown.
type Converter struct{}

// New returns an HTML converter.
func New() *Converter {
	return &Converter{}
}

func (c *Converter) Name() string {
	return "html"
}

func (c *Converter) Priority() float64 {
	return priority
}

func (c *Converter) Accepts(
	_ context.Context,
	_ io.ReadSeeker,
	info inkbite.StreamInfo,
	_ inkbite.ConvertOptions,
) bool {
	if _, ok := htmlExtensions[info.Extension]; ok {
		return true
	}
	if _, ok := htmlMIMETypes[info.MIMEType]; ok {
		return true
	}

	return false
}

func (c *Converter) Convert(
	ctx context.Context,
	r io.ReadSeeker,
	info inkbite.StreamInfo,
	_ inkbite.ConvertOptions,
) (inkbite.Result, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return inkbite.Result{}, err
	}

	if err := ctx.Err(); err != nil {
		return inkbite.Result{}, err
	}
	data, err := io.ReadAll(io.LimitReader(r, maxHTMLBytes+1))
	if err != nil {
		return inkbite.Result{}, err
	}

	if len(data) > maxHTMLBytes {
		return inkbite.Result{}, ErrHTMLLimit
	}
	return c.convertString(ctx, inkbite.DecodeText(data, info.Charset))
}

// ConvertString converts an HTML string into Markdown.
func (c *Converter) ConvertString(input string) (inkbite.Result, error) {
	return c.convertString(context.Background(), input)
}

func (c *Converter) convertString(ctx context.Context, input string) (inkbite.Result, error) {
	if len(input) > maxHTMLBytes {
		return inkbite.Result{}, ErrHTMLLimit
	}
	if err := ctx.Err(); err != nil {
		return inkbite.Result{}, err
	}
	// Bound token-driven DOM allocation before parsing, not only the later walk.
	tokens := html.NewTokenizer(strings.NewReader(input))
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return inkbite.Result{}, err
		}
		if count > 2*maxHTMLNodes {
			return inkbite.Result{}, ErrHTMLLimit
		}
		if tokens.Next() == html.ErrorToken {
			if err := tokens.Err(); err != io.EOF {
				return inkbite.Result{}, err
			}
			break
		}
	}
	doc, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return inkbite.Result{}, err
	}
	if err := validateDOM(ctx, doc); err != nil {
		return inkbite.Result{}, err
	}

	removeNodes(doc, "script", "style")

	title := strings.TrimSpace(findFirstText(doc, "title"))

	target := findFirstNode(doc, "body")
	if target == nil {
		target = doc
	}

	var rendered bytes.Buffer
	if err := html.Render(&rendered, target); err != nil {
		return inkbite.Result{}, err
	}

	// The convenience converter enables CommonMark only. Register the existing
	// table plugin explicitly so table boundaries are not flattened into text.
	engine := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(),
		table.NewTablePlugin(table.WithCellPaddingBehavior(table.CellPaddingBehaviorMinimal)),
	))
	markdown, err := engine.ConvertString(rendered.String(), converter.WithContext(ctx))
	if err != nil {
		return inkbite.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return inkbite.Result{}, err
	}
	if len(markdown) > maxHTMLBytes {
		return inkbite.Result{}, ErrHTMLLimit
	}

	return inkbite.Result{
		Markdown: strings.TrimSpace(markdown),
		Title:    title,
	}, nil
}

// validateDOM bounds recursion before the existing DOM walkers and checks span
// expansion before the dependency can allocate modifications. It never rewrites
// oversized spans into fictitious cells; the input fails with a typed limit error.
func validateDOM(ctx context.Context, root *html.Node) error {
	type frame struct {
		node  *html.Node
		depth int
	}
	stack := []frame{{root, 0}}
	nodes := 0
	gridBudget := maxTableGridCells
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		nodes++
		if nodes > maxHTMLNodes || current.depth > maxHTMLDepth {
			return ErrHTMLLimit
		}
		for child := current.node.LastChild; child != nil; child = child.PrevSibling {
			stack = append(stack, frame{child, current.depth + 1})
		}
	}
	// Depth/node bounds are established before recursive per-table walks.
	var check func(*html.Node) error
	check = func(node *html.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node.Type == html.ElementNode && node.Data == "table" {
			rows, maxCells, modifications, maxRowSpan := 0, 0, 0, 1
			var walk func(*html.Node) error
			walk = func(n *html.Node) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if n.Type == html.ElementNode && n.Data == "tr" {
					rows++
					cells := 0
					var collect func(*html.Node) error
					collect = func(cell *html.Node) error {
						if cell.Type == html.ElementNode && (cell.Data == "td" || cell.Data == "th") {
							cells++
							rowSpan, colSpan := 1, 1
							rowSeen, colSeen := false, false
							for _, attr := range cell.Attr {
								if attr.Key != "rowspan" && attr.Key != "colspan" {
									continue
								}
								value, err := strconv.Atoi(attr.Val)
								if err != nil || value < 1 {
									value = 1
								}
								if value > maxTableSpan {
									return ErrHTMLLimit
								}
								// The dependency uses the first attribute. Check every
								// value, but budget the same effective span it renders.
								if attr.Key == "rowspan" && !rowSeen {
									rowSpan, rowSeen = value, true
								}
								if attr.Key == "colspan" && !colSeen {
									colSpan, colSeen = value, true
								}
							}
							added := rowSpan*colSpan - 1
							if added > maxTableGridCells-modifications {
								return ErrHTMLLimit
							}
							modifications += added
							if rowSpan > maxRowSpan {
								maxRowSpan = rowSpan
							}
						}
						for child := cell.FirstChild; child != nil; child = child.NextSibling {
							if err := collect(child); err != nil {
								return err
							}
						}
						return nil
					}
					if err := collect(n); err != nil {
						return err
					}
					if cells > maxCells {
						maxCells = cells
					}
				}
				for child := n.FirstChild; child != nil; child = child.NextSibling {
					if err := walk(child); err != nil {
						return err
					}
				}
				return nil
			}
			if err := walk(node); err != nil {
				return err
			}
			// Conservative dependency bound: modifications can grow/shift a row;
			// fillUpRows then allocates a rectangular grid including a header row.
			height, width := rows+maxRowSpan+1, maxCells+modifications
			if width > 0 && height > gridBudget/width {
				return ErrHTMLLimit
			}
			gridBudget -= height * width
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := check(child); err != nil {
				return err
			}
		}
		return nil
	}
	return check(root)
}

func removeNodes(node *html.Node, names ...string) {
	if node == nil {
		return
	}

	nameSet := make(map[string]struct{}, len(names))
	for _, name := range names {
		nameSet[name] = struct{}{}
	}

	var walk func(*html.Node)
	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil; {
			next := child.NextSibling
			if child.Type == html.ElementNode {
				if _, ok := nameSet[strings.ToLower(child.Data)]; ok {
					current.RemoveChild(child)
					child = next
					continue
				}
			}
			walk(child)
			child = next
		}
	}

	walk(node)
}

func findFirstNode(node *html.Node, name string) *html.Node {
	if node == nil {
		return nil
	}
	if node.Type == html.ElementNode && strings.EqualFold(node.Data, name) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findFirstNode(child, name); found != nil {
			return found
		}
	}
	return nil
}

func findFirstText(node *html.Node, name string) string {
	target := findFirstNode(node, name)
	if target == nil {
		return ""
	}
	return strings.TrimSpace(extractText(target))
}

func extractText(node *html.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == html.TextNode {
		return node.Data
	}

	var builder strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		builder.WriteString(extractText(child))
	}

	return builder.String()
}
