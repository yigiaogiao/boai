package feed

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html/charset"
)

// Node is a deliberately small XML tree. Feed formats disagree about
// namespaces and prefixes, so matching on lower-cased local names is more
// robust than encoding/xml struct tags for this use case.
type Node struct {
	Name     string
	Attrs    map[string]string
	Text     strings.Builder
	Children []*Node
}

const (
	maxXMLNodes = 200000
	maxXMLDepth = 100
)

// ParseXML builds a Node tree. It tolerates common feed defects (unquoted
// entities, non-strict markup) but still rejects multiple roots, excessive
// depth and excessive node counts.
func ParseXML(data []byte) (*Node, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.CharsetReader = charset.NewReaderLabel
	var root *Node
	var stack []*Node
	count := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			count++
			if count > maxXMLNodes {
				return nil, errors.New("xml: too many nodes")
			}
			if len(stack) >= maxXMLDepth {
				return nil, errors.New("xml: nesting too deep")
			}
			n := &Node{Name: strings.ToLower(t.Name.Local), Attrs: make(map[string]string, len(t.Attr))}
			for _, attr := range t.Attr {
				name := strings.ToLower(attr.Name.Local)
				if _, exists := n.Attrs[name]; !exists {
					n.Attrs[name] = attr.Value
				}
				// Keep the original spelling too (e.g. xmlUrl) for callers that
				// need a case-sensitive attribute.
				if _, exists := n.Attrs[attr.Name.Local]; !exists {
					n.Attrs[attr.Name.Local] = attr.Value
				}
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("xml: multiple root elements")
				}
				root = n
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text.Write(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("xml: empty document")
	}
	return root, nil
}

// Child returns the first direct child with the given local name.
func (n *Node) Child(name string) *Node {
	if n == nil {
		return nil
	}
	name = strings.ToLower(name)
	for _, child := range n.Children {
		if child.Name == name {
			return child
		}
	}
	return nil
}

// ChildrenNamed returns all direct children with the given local name.
func (n *Node) ChildrenNamed(name string) []*Node {
	if n == nil {
		return nil
	}
	name = strings.ToLower(name)
	var out []*Node
	for _, child := range n.Children {
		if child.Name == name {
			out = append(out, child)
		}
	}
	return out
}

// Attr looks up an attribute case-insensitively.
func (n *Node) Attr(name string) string {
	if n == nil {
		return ""
	}
	if v, ok := n.Attrs[name]; ok {
		return strings.TrimSpace(v)
	}
	if v, ok := n.Attrs[strings.ToLower(name)]; ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// TextValue returns the direct text content, trimmed.
func (n *Node) TextValue() string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.Text.String())
}

// ChildText returns the trimmed direct text of the first matching child.
func (n *Node) ChildText(names ...string) string {
	for _, name := range names {
		if child := n.Child(name); child != nil {
			if value := child.TextValue(); value != "" {
				return value
			}
		}
	}
	return ""
}

// InnerText flattens the element's text, including nested markup. This is the
// fallback for feeds that embed HTML as child elements instead of CDATA.
func (n *Node) InnerText() string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*Node, int)
	walk = func(node *Node, depth int) {
		if depth > maxXMLDepth {
			return
		}
		b.WriteString(node.Text.String())
		for _, child := range node.Children {
			b.WriteByte(' ')
			walk(child, depth+1)
		}
	}
	walk(n, 0)
	return strings.TrimSpace(b.String())
}

// FindFirst does a breadth-first search for the first element with the name.
func (n *Node) FindFirst(name string) *Node {
	if n == nil {
		return nil
	}
	name = strings.ToLower(name)
	queue := []*Node{n}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.Name == name && !sameNode(current, n) {
			return current
		}
		queue = append(queue, current.Children...)
	}
	return nil
}

func sameNode(a, b *Node) bool { return a == b }
