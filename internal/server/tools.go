// tools.go — MCP tool definitions for the Octopus MCP server.
//
// Each tool provides AI clients with access to data or actions relevant
// to this server's domain. Tools are always registered (unlike example
// tools, which can be toggled off).
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// discussionBodyPreviewLen is the maximum number of characters to include from
// a discussion body in the tool response.
const discussionBodyPreviewLen = 300

// listDiscussionsInput defines the input for the list_discussions tool.
type listDiscussionsInput struct {
	Owner string `json:"owner" jsonschema:"description=The GitHub repository owner (user or organization)"`
	Repo  string `json:"repo"  jsonschema:"description=The GitHub repository name"`
	First int    `json:"first,omitempty" jsonschema:"description=Number of discussions to return (default 20 max 100)"`
}

// Discussion holds the fields we surface from the GitHub GraphQL response.
type Discussion struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Category  string `json:"category"`
	Author    string `json:"author"`
	CreatedAt string `json:"createdAt"`
	BodyText  string `json:"bodyText"`
}

func registerTools(server *mcp.Server) {
	// list_discussions — Fetches GitHub Discussions for a repository using the
	// GitHub GraphQL API. Requires a GITHUB_TOKEN environment variable.
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_discussions",
		Description: "List GitHub Discussions for a repository",
		InputSchema: map[string]interface{}{
			"type":  "object",
			"title": "ListDiscussionsInput",
			"properties": map[string]interface{}{
				"owner": map[string]interface{}{
					"type":        "string",
					"title":       "Owner",
					"description": "GitHub repository owner (user or organization)",
				},
				"repo": map[string]interface{}{
					"type":        "string",
					"title":       "Repo",
					"description": "GitHub repository name",
				},
				"first": map[string]interface{}{
					"type":        "integer",
					"title":       "First",
					"description": "Number of discussions to return (default 20, max 100)",
					"default":     20,
				},
			},
			"required": []string{"owner", "repo"},
		},
		OutputSchema: map[string]interface{}{
			"type":  "array",
			"title": "Discussions",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"number":    map[string]interface{}{"type": "integer"},
					"title":     map[string]interface{}{"type": "string"},
					"url":       map[string]interface{}{"type": "string"},
					"category":  map[string]interface{}{"type": "string"},
					"author":    map[string]interface{}{"type": "string"},
					"createdAt": map[string]interface{}{"type": "string"},
					"bodyText":  map[string]interface{}{"type": "string"},
				},
			},
		},
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: boolPtr(false),
			IdempotentHint:  true,
			OpenWorldHint:   boolPtr(true), // Calls the GitHub API
		},
	}, listDiscussionsHandler)
}

// githubGraphQLRequest sends a GraphQL query to the GitHub API and decodes the
// response into dest.
func githubGraphQLRequest(ctx context.Context, token, query string, variables map[string]interface{}, dest interface{}) error {
	body := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.github.com/graphql", bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(respBytes))
	}

	if err := json.Unmarshal(respBytes, dest); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}

func listDiscussionsHandler(ctx context.Context, _ *mcp.CallToolRequest, input listDiscussionsInput) (*mcp.CallToolResult, any, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "GITHUB_TOKEN environment variable is not set. Create a token at https://github.com/settings/tokens with the repo scope."},
			},
			IsError: true,
		}, nil, nil
	}

	first := input.First
	if first <= 0 {
		first = 20
	}
	if first > 100 {
		first = 100
	}

	query := `
query ListDiscussions($owner: String!, $repo: String!, $first: Int!) {
  repository(owner: $owner, name: $repo) {
    discussions(first: $first, orderBy: {field: CREATED_AT, direction: DESC}) {
      nodes {
        number
        title
        url
        bodyText
        createdAt
        category {
          name
        }
        author {
          login
        }
      }
    }
  }
}
`

	variables := map[string]interface{}{
		"owner": input.Owner,
		"repo":  input.Repo,
		"first": first,
	}

	var result struct {
		Data struct {
			Repository struct {
				Discussions struct {
					Nodes []struct {
						Number    int    `json:"number"`
						Title     string `json:"title"`
						URL       string `json:"url"`
						BodyText  string `json:"bodyText"`
						CreatedAt string `json:"createdAt"`
						Category  struct {
							Name string `json:"name"`
						} `json:"category"`
						Author struct {
							Login string `json:"login"`
						} `json:"author"`
					} `json:"nodes"`
				} `json:"discussions"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := githubGraphQLRequest(ctx, token, query, variables, &result); err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Failed to fetch discussions: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	if len(result.Errors) > 0 {
		msgs := make([]string, len(result.Errors))
		for i, e := range result.Errors {
			msgs[i] = e.Message
		}
		errorText := "GitHub API errors: " + fmt.Sprintf("%v", msgs)
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: errorText},
			},
			IsError: true,
		}, nil, nil
	}

	nodes := result.Data.Repository.Discussions.Nodes
	discussions := make([]Discussion, 0, len(nodes))
	for _, n := range nodes {
		bodyText := n.BodyText
		if len(bodyText) > discussionBodyPreviewLen {
			bodyText = bodyText[:discussionBodyPreviewLen] + "..."
		}
		discussions = append(discussions, Discussion{
			Number:    n.Number,
			Title:     n.Title,
			URL:       n.URL,
			Category:  n.Category.Name,
			Author:    n.Author.Login,
			CreatedAt: n.CreatedAt,
			BodyText:  bodyText,
		})
	}

	jsonBytes, err := json.MarshalIndent(discussions, "", "  ")
	if err != nil {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("Failed to encode discussions: %v", err)},
			},
			IsError: true,
		}, nil, nil
	}

	if len(discussions) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("No discussions found in %s/%s", input.Owner, input.Repo)},
			},
		}, discussions, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(jsonBytes)},
		},
	}, discussions, nil
}
