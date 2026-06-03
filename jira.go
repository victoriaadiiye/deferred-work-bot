package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type JiraClient struct {
	BaseURL string
	Email   string
	Token   string
	HTTP    *http.Client
}

func NewJiraClient(baseURL, email, token string) *JiraClient {
	return &JiraClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Email:   email,
		Token:   token,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

type JiraSearchInput struct {
	Projects   []string
	Subproject string
	Keywords   []string
	Limit      int
}

type JiraIssue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string   `json:"summary"`
		Description any      `json:"description"`
		Labels      []string `json:"labels"`
		Status      struct {
			Name string `json:"name"`
		} `json:"status"`
	} `json:"fields"`
}

func (c *JiraClient) BuildJQL(in JiraSearchInput) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("project in (%s)", strings.Join(in.Projects, ",")))
	parts = append(parts, "(statusCategory != Done OR resolved > -90d)")
	if in.Subproject != "" {
		parts = append(parts, fmt.Sprintf(`(labels = "%s" OR labels is EMPTY)`, in.Subproject))
	} else {
		parts = append(parts, "labels is EMPTY")
	}
	if len(in.Keywords) > 0 {
		quoted := make([]string, 0, len(in.Keywords))
		for _, k := range in.Keywords {
			k = strings.ReplaceAll(k, `"`, `\"`)
			quoted = append(quoted, fmt.Sprintf(`"%s"`, k))
		}
		parts = append(parts, fmt.Sprintf("text ~ (%s)", strings.Join(quoted, " OR ")))
	}
	return strings.Join(parts, " AND ") + " ORDER BY updated DESC"
}

func (c *JiraClient) Search(in JiraSearchInput) ([]JiraIssue, error) {
	if in.Limit == 0 {
		in.Limit = 20
	}
	return c.searchJQL(c.BuildJQL(in), in.Limit)
}

// SearchEpics returns the open epics in the given projects, most-recently
// updated first, so a deferred-work item can be matched to a likely parent.
// Done epics are excluded — there is no point filing new work under them.
func (c *JiraClient) SearchEpics(projects []string, limit int) ([]JiraIssue, error) {
	if len(projects) == 0 {
		return nil, nil
	}
	if limit == 0 {
		limit = 50
	}
	jql := fmt.Sprintf("project in (%s) AND issuetype = Epic AND statusCategory != Done ORDER BY updated DESC", strings.Join(projects, ","))
	return c.searchJQL(jql, limit)
}

func (c *JiraClient) searchJQL(jql string, limit int) ([]JiraIssue, error) {
	body, _ := json.Marshal(map[string]any{
		"jql":        jql,
		"maxResults": limit,
		"fields":     []string{"summary", "description", "labels", "status"},
	})
	req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/search", bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("jira search %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Issues []JiraIssue `json:"issues"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Issues, nil
}

type CreateIssueInput struct {
	ProjectKey        string
	Summary           string
	Description       string
	IssueType         string
	Labels            []string
	Priority          string
	AssigneeAccountID string
	ParentEpicKey     string
}

type CreatedIssue struct {
	Key string
	URL string
}

func (c *JiraClient) CreateIssue(in CreateIssueInput) (*CreatedIssue, error) {
	fields := map[string]any{
		"project":   map[string]any{"key": in.ProjectKey},
		"summary":   in.Summary,
		"issuetype": map[string]any{"name": in.IssueType},
		"labels":    in.Labels,
	}
	if in.Description != "" {
		fields["description"] = adfFromText(in.Description)
	}
	if in.Priority != "" {
		fields["priority"] = map[string]any{"name": in.Priority}
	}
	if in.AssigneeAccountID != "" {
		fields["assignee"] = map[string]any{"accountId": in.AssigneeAccountID}
	}
	// In team-managed (and modern company-managed) projects an epic is the
	// child issue's parent, so the epic link is set via the parent field.
	if in.ParentEpicKey != "" {
		fields["parent"] = map[string]any{"key": in.ParentEpicKey}
	}
	body, _ := json.Marshal(map[string]any{"fields": fields})
	req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/issue", bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create issue %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &CreatedIssue{Key: out.Key, URL: c.BaseURL + "/browse/" + out.Key}, nil
}

type JiraIssueDetail struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Parent  *struct {
			Key    string `json:"key"`
			Fields struct {
				Summary   string `json:"summary"`
				IssueType struct {
					Name string `json:"name"`
				} `json:"issuetype"`
			} `json:"fields"`
		} `json:"parent"`
	} `json:"fields"`
}

func (c *JiraClient) GetIssue(key string) (*JiraIssueDetail, error) {
	req, _ := http.NewRequest("GET", c.BaseURL+"/rest/api/3/issue/"+key+"?fields=summary,parent", nil)
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Accept", "application/json")
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get issue %d: %s", resp.StatusCode, string(b))
	}
	var out JiraIssueDetail
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *JiraClient) AddComment(issueKey, text string) error {
	body, _ := json.Marshal(map[string]any{"body": adfFromText(text)})
	req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/issue/"+issueKey+"/comment", bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add comment %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (c *JiraClient) AddLabel(issueKey, label string) error {
	body, _ := json.Marshal(map[string]any{
		"update": map[string]any{
			"labels": []map[string]any{{"add": label}},
		},
	})
	req, _ := http.NewRequest("PUT", c.BaseURL+"/rest/api/3/issue/"+issueKey, bytes.NewReader(body))
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add label %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// AddAttachment uploads a file to an issue. Jira requires the multipart field
// name "file" and the X-Atlassian-Token: no-check header to bypass XSRF checks
// on the attachments endpoint.
func (c *JiraClient) AddAttachment(issueKey, filename string, content []byte) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(content); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, _ := http.NewRequest("POST", c.BaseURL+"/rest/api/3/issue/"+issueKey+"/attachments", &buf)
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Atlassian-Token", "no-check")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("add attachment %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// FindAccountID resolves a Jira account ID from a user's email address. It
// returns ("", nil) when no matching user is found so callers can treat an
// unresolved assignee as a soft miss rather than an error.
func (c *JiraClient) FindAccountID(email string) (string, error) {
	if email == "" {
		return "", nil
	}
	req, _ := http.NewRequest("GET", c.BaseURL+"/rest/api/3/user/search?query="+url.QueryEscape(email), nil)
	req.SetBasicAuth(c.Email, c.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("user search %d: %s", resp.StatusCode, string(b))
	}
	var users []struct {
		AccountID string `json:"accountId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return "", err
	}
	if len(users) == 0 {
		return "", nil
	}
	return users[0].AccountID, nil
}
