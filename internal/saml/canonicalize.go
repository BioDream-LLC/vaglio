// Exclusive XML Canonicalization (exc-c14n) per https://www.w3.org/TR/xml-exc-c14n/
//
// This is the byte-exact canonical form that an XML signature signs. Getting it wrong means valid signatures appear
// invalid and - worse - an implementation that gets it mostly right will pass in testing against its own signatures
// while rejecting real IdP responses.
//
// The rules that matter for SAML:
//   - Namespace declarations are rendered only if visibly utilised by the element or its attributes.
//   - Inherited namespaces that an element uses but did not declare are rendered as if declared on that element.
//   - Attributes are sorted: namespace declarations first (sorted by prefix), then other attributes (sorted by
//     expanded name: namespace URI then local name).
//   - Empty elements use start-tag/end-tag, never self-closing.
//   - Text nodes are output verbatim; CDATA is replaced by its content with entity escaping.
//   - Processing instructions and comments outside the document element are excluded.
package saml

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// canonicalize produces the exclusive canonical form of an XML subtree.
//
// inclusiveNamespaces is the InclusiveNamespaces PrefixList from the CanonicalizationMethod. In practice SAML responses
// rarely use it, but handling it correctly matters because an IdP may include it.
func canonicalize(raw []byte, inclusiveNamespaces []string) ([]byte, error) {
	node, err := parseToTree(raw)
	if err != nil {
		return nil, fmt.Errorf("saml/c14n: parse: %w", err)
	}
	var buf bytes.Buffer
	inclSet := make(map[string]bool, len(inclusiveNamespaces))
	for _, p := range inclusiveNamespaces {
		inclSet[p] = true
	}
	if err := renderNode(&buf, node, nil, inclSet); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// canonicalizeNode serializes a single already-parsed xmlNode tree.
func canonicalizeNode(n *xmlNode, inclusiveNamespaces []string) []byte {
	inclSet := make(map[string]bool, len(inclusiveNamespaces))
	for _, p := range inclusiveNamespaces {
		inclSet[p] = true
	}
	var buf bytes.Buffer
	_ = renderNode(&buf, n, nil, inclSet)
	return buf.Bytes()
}

// xmlNode is a lightweight DOM used for canonicalization.
type xmlNode struct {
	Space string // namespace URI
	Local string // local name

	// Prefix is the prefix as the document wrote it, empty for a default namespace.
	//
	// Kept because canonical XML preserves prefixes, and reconstructing one from the namespace URI cannot work. Two things go wrong.
	// A URI is routinely bound to more than one prefix in the same scope - a real Keycloak assertion declares the SAML namespace as
	// the default on the element while inheriting it as saml: from the root - so a reverse lookup has to choose, and choosing wrong
	// changes every byte of the canonical form. And a lookup over a Go map is not deterministic, so the same document could verify
	// on one attempt and fail on the next.
	//
	// This was not a hypothetical. Reverse-mapping produced <Assertion xmlns="..."> where Keycloak had written <saml:Assertion>, so
	// every real assertion failed its digest check, and the package's own tests passed throughout because they signed and verified
	// with the same wrong code.
	Prefix   string
	Attrs    []xml.Attr
	Children []*xmlNode
	Text     string // character data (for text nodes: Space and Local are empty)
	IsText   bool
}

// nsDecl is a sorted namespace declaration.
type nsDecl struct {
	Prefix string
	URI    string
}

// renderNode writes the exclusive canonical form of a node and its descendants.
//
// visibleAncestorNS is the set of namespace declarations already rendered by an ancestor element. This matters because
// exc-c14n only renders a namespace if it is visibly utilised, and does not re-render it if an ancestor already output
// the same prefix→URI binding.
func renderNode(w *bytes.Buffer, n *xmlNode, parentNS map[string]string, inclPrefixes map[string]bool) error {
	if n.IsText {
		writeEscapedText(w, n.Text)
		return nil
	}

	// Collect namespace declarations from this element's attributes.
	localNSDecls := make(map[string]string) // prefix → URI from xmlns attrs on this element
	var regularAttrs []xml.Attr
	for _, a := range n.Attrs {
		if a.Name.Space == "xmlns" {
			localNSDecls[a.Name.Local] = a.Value
		} else if a.Name.Space == "" && a.Name.Local == "xmlns" {
			localNSDecls[""] = a.Value
		} else {
			regularAttrs = append(regularAttrs, a)
		}
	}

	// Determine which namespaces are visibly utilised:
	// 1. The element's own namespace.
	// 2. Each attribute's namespace.
	needed := make(map[string]string) // prefix → URI that must appear

	// Element namespace, using the prefix the document wrote.
	//
	// Not looked up by URI. The lookup it replaces returned the default prefix for an element written as saml:Assertion, because the
	// element declared the same URI as its default namespace while inheriting it as saml: from the root - so the canonical form said
	// <Assertion xmlns="..."> where the signer had written <saml:Assertion ...>, and every real assertion failed its digest.
	elemPrefix := n.Prefix
	if n.Space != "" || elemPrefix != "" {
		needed[elemPrefix] = n.Space
	}
	// Default namespace: if the element is in no namespace but the parent declared a default, we need xmlns="".
	if n.Space == "" && parentNS[""] != "" {
		needed[""] = ""
	}

	// Attribute namespaces.
	for i := range regularAttrs {
		a := &regularAttrs[i]
		if a.Name.Space != "" {
			p := prefixForNS(a.Name.Space, localNSDecls, parentNS)
			needed[p] = a.Name.Space
		}
	}

	// Inclusive prefixes: render them even if not visibly utilised by this element, as long as the namespace is in scope.
	for p := range inclPrefixes {
		if _, already := needed[p]; !already {
			if uri, ok := localNSDecls[p]; ok {
				needed[p] = uri
			} else if uri, ok := parentNS[p]; ok {
				needed[p] = uri
			}
		}
	}

	// Remove namespaces already correctly declared by an ancestor (same prefix→URI in parentNS) unless they're
	// inclusive prefixes (which must always be rendered) - actually in exc-c14n, if an ancestor already rendered
	// the same binding, we don't re-render it. We only render if the binding is new or changed.
	var nsToRender []nsDecl
	for p, uri := range needed {
		if parentNS[p] == uri && !inclPrefixes[p] {
			continue // ancestor already output this binding
		}
		nsToRender = append(nsToRender, nsDecl{Prefix: p, URI: uri})
	}

	// Sort namespace declarations: default namespace first (empty prefix sorts first), then by prefix.
	sort.Slice(nsToRender, func(i, j int) bool {
		if nsToRender[i].Prefix == "" && nsToRender[j].Prefix != "" {
			return true
		}
		if nsToRender[j].Prefix == "" && nsToRender[i].Prefix != "" {
			return false
		}
		return nsToRender[i].Prefix < nsToRender[j].Prefix
	})

	// Sort attributes by namespace URI then local name.
	sort.Slice(regularAttrs, func(i, j int) bool {
		if regularAttrs[i].Name.Space != regularAttrs[j].Name.Space {
			return regularAttrs[i].Name.Space < regularAttrs[j].Name.Space
		}
		return regularAttrs[i].Name.Local < regularAttrs[j].Name.Local
	})

	// Build the child's visible namespace context.
	childNS := make(map[string]string, len(parentNS)+len(nsToRender))
	for k, v := range parentNS {
		childNS[k] = v
	}
	for _, d := range nsToRender {
		childNS[d.Prefix] = d.URI
	}
	// Also inherit local declarations that were not rendered (they are still in scope for children).
	for p, uri := range localNSDecls {
		if _, present := childNS[p]; !present {
			childNS[p] = uri
		}
	}

	// Write start tag.
	w.WriteByte('<')
	writeQName(w, elemPrefix, n.Local)

	// Namespace declarations.
	for _, d := range nsToRender {
		w.WriteByte(' ')
		if d.Prefix == "" {
			w.WriteString(`xmlns="`)
		} else {
			w.WriteString("xmlns:")
			w.WriteString(d.Prefix)
			w.WriteString(`="`)
		}
		writeEscapedAttr(w, d.URI)
		w.WriteByte('"')
	}

	// Attributes.
	for _, a := range regularAttrs {
		w.WriteByte(' ')
		if a.Name.Space != "" {
			p := prefixForNS(a.Name.Space, localNSDecls, childNS)
			writeQName(w, p, a.Name.Local)
		} else {
			w.WriteString(a.Name.Local)
		}
		w.WriteString(`="`)
		writeEscapedAttr(w, a.Value)
		w.WriteByte('"')
	}

	w.WriteByte('>')

	// Children.
	for _, child := range n.Children {
		if err := renderNode(w, child, childNS, inclPrefixes); err != nil {
			return err
		}
	}

	// End tag (never self-closing).
	w.WriteString("</")
	writeQName(w, elemPrefix, n.Local)
	w.WriteByte('>')

	return nil
}

// prefixForNS finds the prefix that maps to uri in local declarations or inherited namespaces.
func prefixForNS(uri string, local, inherited map[string]string) string {
	for p, u := range local {
		if u == uri {
			return p
		}
	}
	for p, u := range inherited {
		if u == uri {
			return p
		}
	}
	return ""
}

func writeQName(w *bytes.Buffer, prefix, local string) {
	if prefix != "" {
		w.WriteString(prefix)
		w.WriteByte(':')
	}
	w.WriteString(local)
}

func writeEscapedText(w *bytes.Buffer, s string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			w.WriteString("&amp;")
		case '<':
			w.WriteString("&lt;")
		case '>':
			w.WriteString("&gt;")
		case '\r':
			w.WriteString("&#xD;")
		default:
			w.WriteByte(s[i])
		}
	}
}

func writeEscapedAttr(w *bytes.Buffer, s string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			w.WriteString("&amp;")
		case '<':
			w.WriteString("&lt;")
		case '"':
			w.WriteString("&quot;")
		case '\t':
			w.WriteString("&#x9;")
		case '\n':
			w.WriteString("&#xA;")
		case '\r':
			w.WriteString("&#xD;")
		default:
			w.WriteByte(s[i])
		}
	}
}

// parseToTree builds a lightweight DOM from raw XML.
//
// Depth is limited to prevent stack exhaustion from deeply nested documents.
func parseToTree(raw []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = true

	var root *xmlNode
	var stack []*xmlNode
	depth := 0

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth > maxXMLDepth {
				return nil, fmt.Errorf("saml/c14n: XML depth exceeds %d", maxXMLDepth)
			}
			// The prefix is read from the source rather than inferred, using the offset the decoder has just passed.
			//
			// Go's decoder resolves a prefix to a namespace URI and discards the prefix, and canonical XML needs the prefix. The
			// alternative - looking the URI up among the declarations in scope - cannot be made correct, because a URI may be bound
			// to several prefixes at once and the map lookup is not deterministic.
			n := &xmlNode{
				Space:  t.Name.Space,
				Local:  t.Name.Local,
				Prefix: writtenPrefix(raw, dec.InputOffset(), t.Name.Local),
				Attrs:  append([]xml.Attr(nil), t.Attr...),
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, n)
			} else {
				root = n
			}
			stack = append(stack, n)

		case xml.EndElement:
			depth--
			stack = stack[:len(stack)-1]

		case xml.CharData:
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, &xmlNode{
					Text:   string(t),
					IsText: true,
				})
			}
		}
	}

	if root == nil {
		return nil, fmt.Errorf("saml/c14n: empty document")
	}
	return root, nil
}

// findElement searches the tree for the first element matching the given namespace and local name.
func findElement(n *xmlNode, space, local string) *xmlNode {
	if n.Space == space && n.Local == local {
		return n
	}
	for _, c := range n.Children {
		if c.IsText {
			continue
		}
		if found := findElement(c, space, local); found != nil {
			return found
		}
	}
	return nil
}

// findElementByID searches the tree for an element whose ID attribute matches.
func findElementByID(n *xmlNode, id string) *xmlNode {
	if n.IsText {
		return nil
	}
	for _, a := range n.Attrs {
		if (a.Name.Local == "ID" || a.Name.Local == "Id" || a.Name.Local == "id") && a.Value == id {
			return n
		}
	}
	for _, c := range n.Children {
		if found := findElementByID(c, id); found != nil {
			return found
		}
	}
	return nil
}

// removeSignature returns a copy of a node with Signature child elements removed.
// This is needed for enveloped-signature transform.
func removeSignature(n *xmlNode) *xmlNode {
	if n.IsText {
		return n
	}
	// The prefix is copied like every other part of the name. Omitting it here undid the whole point of recording it: the tree was
	// correct and the clone the digest was taken over was not, which is a good argument for cloning by assignment rather than by
	// listing fields.
	clone := &xmlNode{
		Space:  n.Space,
		Local:  n.Local,
		Prefix: n.Prefix,
		Attrs:  n.Attrs,
	}
	for _, c := range n.Children {
		if !c.IsText && c.Local == "Signature" && c.Space == nsXMLDSig {
			continue
		}
		clone.Children = append(clone.Children, removeSignature(c))
	}
	return clone
}

// extractText returns the concatenation of all text nodes under n.
func extractText(n *xmlNode) string {
	if n.IsText {
		return n.Text
	}
	var sb strings.Builder
	for _, c := range n.Children {
		sb.WriteString(extractText(c))
	}
	return sb.String()
}

// writtenPrefix recovers the prefix a start tag actually used.
//
// end is the decoder's input offset immediately after the tag, so the tag text is the last "<...>" before it. Read from the source
// because that is the only place the information survives: the decoder hands back a resolved namespace URI and drops the prefix, and
// canonical XML has to reproduce what was written.
//
// Returns an empty prefix when the tag carried none, which is also the right answer for an element in a default namespace.
func writtenPrefix(raw []byte, end int64, local string) string {
	if end <= 0 || end > int64(len(raw)) {
		return ""
	}

	// Walk back to the '<' that opens this tag. A tag is short, so this is bounded in practice; the limit is a guard against a
	// pathological document rather than an expected case.
	start := int(end) - 1
	for limit := 0; start >= 0 && limit < 64*1024; start, limit = start-1, limit+1 {
		if raw[start] == '<' {
			break
		}
	}

	if start < 0 || start+1 >= len(raw) {
		return ""
	}

	// The qualified name runs from just after '<' to the first whitespace, '/' or '>'.
	i := start + 1
	j := i

	for j < len(raw) {
		c := raw[j]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '/' || c == '>' {
			break
		}

		j++
	}

	qname := string(raw[i:j])

	colon := strings.IndexByte(qname, ':')
	if colon <= 0 {
		return ""
	}

	// Checked against the local name the decoder reported. If they disagree, the offset arithmetic has found the wrong tag, and
	// returning nothing keeps the old behaviour rather than inventing a prefix - a wrong prefix would be a silent digest failure.
	if qname[colon+1:] != local {
		return ""
	}

	return qname[:colon]
}
