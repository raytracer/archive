package app

import (
	"regexp"
	"strings"
)

var synonymSeeds = map[string][]string{
	"invoice":  {"bill", "receipt", "statement", "payment"},
	"contract": {"agreement", "deal", "terms"},
	"tax":      {"vat", "irs", "revenue", "finance"},
	"medical":  {"health", "doctor", "clinic", "hospital"},
	"bank":     {"account", "statement", "finance"},
	"car":      {"vehicle", "auto", "automobile"},
}

var tokenRe = regexp.MustCompile(`[\pL\pN]+`)

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func normalizeTags(tags []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(tag, "#")))
		tag = strings.ReplaceAll(tag, ",", " ")
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

func expandQuery(q string) []string {
	tokens := tokenRe.FindAllString(strings.ToLower(q), -1)
	seen := map[string]bool{}
	var out []string
	for _, token := range tokens {
		if !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
		for _, syn := range synonymSeeds[token] {
			if !seen[syn] {
				seen[syn] = true
				out = append(out, syn)
			}
		}
	}
	return out
}

func ftsQuery(tokens []string) string {
	if len(tokens) == 0 {
		return `""`
	}
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.ReplaceAll(token, `"`, "")
		if token != "" {
			parts = append(parts, token+"*")
		}
	}
	return strings.Join(parts, " OR ")
}
