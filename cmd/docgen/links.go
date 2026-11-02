// SPDX-FileCopyrightText: The OpenArtifact Authors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	linkRe     = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	headingRe  = regexp.MustCompile(`^ {0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	codeSpanRe = regexp.MustCompile("`[^`]*`")
	mdLinkRe   = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	slugDropRe = regexp.MustCompile(`[^\p{L}\p{N} _-]`)
)

// mdFile is a parsed markdown page: the heading anchors it defines and the
// links it contains, with the line each link is on.
type mdFile struct {
	anchors map[string]bool
	links   []mdLink
}

type mdLink struct {
	target string
	line   int
}

// slug turns heading text into a GitHub-style anchor.
func slug(text string) string {
	text = mdLinkRe.ReplaceAllString(text, "$1")
	text = strings.NewReplacer("`", "", "*", "", "~", "").Replace(text)
	text = strings.ToLower(strings.TrimSpace(text))
	text = slugDropRe.ReplaceAllString(text, "")

	return strings.ReplaceAll(text, " ", "-")
}

func parseMarkdown(data []byte) mdFile {
	f := mdFile{anchors: map[string]bool{}}
	seen := map[string]int{}
	fence := ""

	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			switch fence {
			case "":
				fence = marker
			case marker:
				fence = ""
			}

			continue
		}

		if fence != "" {
			continue
		}

		if m := headingRe.FindStringSubmatch(line); m != nil {
			s := slug(m[1])
			if n := seen[s]; n > 0 {
				f.anchors[fmt.Sprintf("%s-%d", s, n)] = true
			} else {
				f.anchors[s] = true
			}

			seen[s]++
		}

		for _, m := range linkRe.FindAllStringSubmatch(codeSpanRe.ReplaceAllString(line, ""), -1) {
			f.links = append(f.links, mdLink{target: m[1], line: i + 1})
		}
	}

	return f
}

// checkLinks verifies every relative link and heading anchor in the markdown
// under docs/ (plus the repository root pages they point at) and that
// docs/README.md links every page outside docs/reference and docs/assets.
func checkLinks(root string) error {
	docs := filepath.Join(root, "docs")
	pages := map[string]mdFile{}

	load := func(rel string) (mdFile, error) {
		if f, ok := pages[rel]; ok {
			return f, nil
		}

		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return mdFile{}, err
		}

		f := parseMarkdown(data)
		pages[rel] = f

		return f, nil
	}

	var files []string

	err := filepath.WalkDir(docs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() && strings.HasSuffix(p, ".md") {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}

		return nil
	})
	if err != nil {
		return err
	}

	slices.Sort(files)

	var problems []string

	indexed := map[string]bool{}

	for _, rel := range files {
		f, err := load(rel)
		if err != nil {
			return err
		}

		for _, l := range f.links {
			problem := checkLink(root, rel, l.target, load)
			if problem != "" {
				problems = append(problems, fmt.Sprintf("%s:%d: %s", rel, l.line, problem))
			}

			if rel == "docs/README.md" {
				if t, _, _ := strings.Cut(l.target, "#"); t != "" {
					indexed[path.Join("docs", t)] = true
				}
			}
		}
	}

	for _, rel := range files {
		if strings.HasPrefix(rel, "docs/reference/") || rel == "docs/README.md" || indexed[rel] {
			continue
		}

		problems = append(problems, rel+": not linked from docs/README.md")
	}

	if len(problems) > 0 {
		return fmt.Errorf("documentation links are broken:\n  %s", strings.Join(problems, "\n  "))
	}

	return nil
}

func checkLink(root, from, target string, load func(string) (mdFile, error)) string {
	if u, err := url.Parse(target); err != nil || u.Scheme != "" || strings.HasPrefix(target, "//") {
		return ""
	}

	file, frag, _ := strings.Cut(target, "#")
	dest := from

	if file != "" {
		file, _, _ = strings.Cut(file, "?")
		if strings.HasPrefix(file, "/") {
			return "" // site-absolute path, resolved by the site
		}

		dest = path.Join(path.Dir(from), file)
	}

	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(dest)))
	if err != nil {
		return target + " does not exist"
	}

	if frag == "" || info.IsDir() || !strings.HasSuffix(dest, ".md") {
		return ""
	}

	f, err := load(dest)
	if err != nil {
		return err.Error()
	}

	if frag, err = url.PathUnescape(frag); err == nil && !f.anchors[strings.ToLower(frag)] {
		return fmt.Sprintf("%s: no heading anchor #%s in %s", target, frag, dest)
	}

	return ""
}
