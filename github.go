package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var prURLRe = regexp.MustCompile(`https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)`)

type PRRef struct {
	Owner  string
	Repo   string
	Number string
}

func ParsePRRefs(text string) []PRRef {
	matches := prURLRe.FindAllStringSubmatch(text, -1)
	seen := map[string]bool{}
	var out []PRRef
	for _, m := range matches {
		key := m[1] + "/" + m[2] + "/" + m[3]
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, PRRef{Owner: m[1], Repo: m[2], Number: m[3]})
	}
	return out
}

type GitHubPR struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type GitHubClient struct {
	Token string
	HTTP  *http.Client
}

func NewGitHubClient() *GitHubClient {
	return &GitHubClient{
		Token: os.Getenv("GITHUB_TOKEN"),
		HTTP:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (g *GitHubClient) FetchPR(ref PRRef) (*GitHubPR, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%s", ref.Owner, ref.Repo, ref.Number)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	resp, err := g.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github %d: %s", resp.StatusCode, string(b))
	}
	var pr GitHubPR
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

var jiraKeyRe = regexp.MustCompile(`[A-Z][A-Z0-9]+-\d+`)

func ExtractJiraKeys(texts ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, k := range jiraKeyRe.FindAllString(strings.ToUpper(t), -1) {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}
