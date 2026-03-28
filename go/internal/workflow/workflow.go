// Package workflow provides WORKFLOW.md parsing.
package workflow

import (
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Workflow represents a loaded WORKFLOW.md file.
type Workflow struct {
	Config         map[string]interface{}
	Prompt         string
	PromptTemplate string
}

// Load reads and parses a WORKFLOW.md file.
func Load(path string) (*Workflow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open workflow file: %w", err)
	}
	defer f.Close()
	return ReadAndParse(f)
}

// ReadAndParse reads from an io.Reader and parses the workflow.
func ReadAndParse(r io.Reader) (*Workflow, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read workflow: %w", err)
	}
	return Parse(string(content))
}

// Parse parses WORKFLOW.md content.
func Parse(content string) (*Workflow, error) {
	// Try to extract YAML from code block first
	config, configErr := extractYAMLFromFencedBlock(content)
	if configErr == nil && len(config) > 0 {
		// Successfully extracted YAML block
		return &Workflow{
			Config:         config,
			Prompt:         "",
			PromptTemplate: "",
		}, nil
	}

	// Fallback: try frontmatter
	lines := strings.Split(content, "\n")

	var frontMatterLines []string
	var promptLines []string
	inFrontMatter := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if i == 0 && trimmed == "---" {
			inFrontMatter = true
			continue
		}

		if inFrontMatter && trimmed == "---" {
			inFrontMatter = false
			continue
		}

		if inFrontMatter {
			frontMatterLines = append(frontMatterLines, line)
		} else {
			promptLines = append(promptLines, line)
		}
	}

	config, err := parseYAMLContent(strings.Join(frontMatterLines, "\n"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse front matter: %w", err)
	}

	prompt := strings.TrimSpace(strings.Join(promptLines, "\n"))

	return &Workflow{
		Config:         config,
		Prompt:         prompt,
		PromptTemplate: prompt,
	}, nil
}

// extractYAMLFromFencedBlock extracts YAML from a fenced code block like ```yaml ... ```
func extractYAMLFromFencedBlock(content string) (map[string]interface{}, error) {
	lines := strings.Split(content, "\n")
	var yamlLines []string
	inBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if !inBlock {
				// Check if it's a yaml block
				lang := strings.TrimPrefix(trimmed, "```")
				if lang == "yaml" || lang == "" {
					inBlock = true
				}
			} else {
				// End of block
				break
			}
			continue
		}

		if inBlock {
			yamlLines = append(yamlLines, line)
		}
	}

	return parseYAMLContent(strings.Join(yamlLines, "\n"))
}

func parseYAMLContent(yamlContent string) (map[string]interface{}, error) {
	yamlContent = strings.TrimSpace(yamlContent)
	if yamlContent == "" {
		return make(map[string]interface{}), nil
	}

	var result map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &result); err != nil {
		return nil, fmt.Errorf("yaml unmarshal failed: %w", err)
	}

	if result == nil {
		return make(map[string]interface{}), nil
	}

	return result, nil
}