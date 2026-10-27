// Package toml implements the TOML subset used by LUMO configuration files:
// comments, tables, dotted keys, arrays of tables, quoted strings, integers,
// floats, booleans and possibly multi-line arrays.
//
// It is intentionally small: the full TOML grammar is not supported.
package toml

import (
	"fmt"
	"strconv"
	"strings"
)

// Parse decodes src into a nested map. Leaf values are string, int64,
// float64 or bool; arrays are []any; tables are map[string]any.
func Parse(src string) (map[string]any, error) {
	root := map[string]any{}
	cur := root
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(stripComment(lines[i]))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			arr := strings.HasPrefix(line, "[[")
			var name string
			if arr {
				if !strings.HasSuffix(line, "]]") {
					return nil, fmt.Errorf("line %d: bad table header %q", i+1, line)
				}
				name = strings.TrimSpace(line[2 : len(line)-2])
			} else {
				if !strings.HasSuffix(line, "]") {
					return nil, fmt.Errorf("line %d: bad table header %q", i+1, line)
				}
				name = strings.TrimSpace(line[1 : len(line)-1])
			}
			parts, err := splitKey(name)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, err)
			}
			if arr {
				tbl, err := extendArrayTable(root, parts)
				if err != nil {
					return nil, fmt.Errorf("line %d: %w", i+1, err)
				}
				cur = tbl
			} else {
				tbl, err := ensureTable(root, parts)
				if err != nil {
					return nil, fmt.Errorf("line %d: %w", i+1, err)
				}
				cur = tbl
			}
			continue
		}
		eq := strings.Index(line, "=")
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected key = value, got %q", i+1, line)
		}
		parts, err := splitKey(strings.TrimSpace(line[:eq]))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		val := strings.TrimSpace(line[eq+1:])
		for arrayOpen(val) {
			if i+1 >= len(lines) {
				return nil, fmt.Errorf("line %d: unterminated array", i+1)
			}
			i++
			val += " " + strings.TrimSpace(stripComment(lines[i]))
		}
		v, err := parseValue(val)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if err := assign(cur, parts, v); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
	}
	return root, nil
}

// stripComment removes a trailing # comment that is not inside a string.
func stripComment(line string) string {
	inStr, esc := false, false
	for i, r := range line {
		if inStr {
			switch {
			case esc:
				esc = false
			case r == '\\':
				esc = true
			case r == '"':
				inStr = false
			}
			continue
		}
		switch r {
		case '"':
			inStr = true
		case '#':
			return line[:i]
		}
	}
	return line
}

// arrayOpen reports whether the value has more '[' than ']' outside strings.
func arrayOpen(s string) bool {
	depth, inStr, esc := 0, false, false
	for _, r := range s {
		if inStr {
			switch {
			case esc:
				esc = false
			case r == '\\':
				esc = true
			case r == '"':
				inStr = false
			}
			continue
		}
		switch r {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
		}
	}
	return depth > 0
}

func splitKey(s string) ([]string, error) {
	var parts []string
	for _, p := range strings.Split(s, ".") {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
			p = p[1 : len(p)-1]
		}
		if p == "" {
			return nil, fmt.Errorf("empty key segment in %q", s)
		}
		parts = append(parts, p)
	}
	return parts, nil
}

func ensureTable(root map[string]any, parts []string) (map[string]any, error) {
	cur := root
	for _, p := range parts {
		switch v := cur[p].(type) {
		case nil:
			t := map[string]any{}
			cur[p] = t
			cur = t
		case map[string]any:
			cur = v
		default:
			return nil, fmt.Errorf("key %q is not a table", p)
		}
	}
	return cur, nil
}

func extendArrayTable(root map[string]any, parts []string) (map[string]any, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("empty table name")
	}
	parent, err := ensureTable(root, parts[:len(parts)-1])
	if err != nil {
		return nil, err
	}
	last := parts[len(parts)-1]
	arr, _ := parent[last].([]any)
	if parent[last] != nil && arr == nil {
		return nil, fmt.Errorf("key %q is not an array of tables", last)
	}
	tbl := map[string]any{}
	parent[last] = append(arr, tbl)
	return tbl, nil
}

func assign(cur map[string]any, parts []string, v any) error {
	parent, err := ensureTable(cur, parts[:len(parts)-1])
	if err != nil {
		return err
	}
	last := parts[len(parts)-1]
	if existing, ok := parent[last].(map[string]any); ok {
		if nv, ok := v.(map[string]any); ok {
			for k, val := range nv {
				existing[k] = val
			}
			return nil
		}
	}
	parent[last] = v
	return nil
}

func parseValue(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty value")
	}
	switch {
	case s[0] == '"':
		return parseString(s)
	case s == "true":
		return true, nil
	case s == "false":
		return false, nil
	case s[0] == '[':
		return parseArray(s)
	default:
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, nil
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, nil
		}
		return nil, fmt.Errorf("unsupported value %q", s)
	}
}

func parseString(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("bad string %q", s)
	}
	body := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(body) {
			return "", fmt.Errorf("dangling escape in %q", s)
		}
		switch body[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			return "", fmt.Errorf("unsupported escape \\%c", body[i])
		}
	}
	return b.String(), nil
}

func parseArray(s string) ([]any, error) {
	items := []any{}
	depth, inStr, esc := 0, false, false
	start := -1
	flush := func(end int) error {
		if start < 0 {
			return nil
		}
		tok := strings.TrimSpace(s[start:end])
		start = -1
		if tok == "" {
			return nil
		}
		v, err := parseValue(tok)
		if err != nil {
			return err
		}
		items = append(items, v)
		return nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
			if start < 0 {
				start = i
			}
		case ' ', '\t':
		case '[':
			depth++
			if depth == 1 {
				start = i + 1
			} else if start < 0 {
				start = i
			}
		case ']':
			depth--
			if depth == 0 {
				if err := flush(i); err != nil {
					return nil, err
				}
				if rest := strings.TrimSpace(s[i+1:]); rest != "" {
					return nil, fmt.Errorf("trailing data after array: %q", rest)
				}
				return items, nil
			}
		case ',':
			if depth == 1 {
				if err := flush(i); err != nil {
					return nil, err
				}
			}
		default:
			if start < 0 {
				start = i
			}
		}
	}
	return nil, fmt.Errorf("unterminated array: %q", s)
}
