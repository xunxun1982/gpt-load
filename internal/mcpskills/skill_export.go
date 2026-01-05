package mcpskills

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// SkillExportService handles skill export functionality
type SkillExportService struct {
	db           *gorm.DB
	mcpService   *Service
	groupService *GroupService
}

// NewSkillExportService creates a new skill export service instance
func NewSkillExportService(db *gorm.DB, mcpService *Service, groupService *GroupService) *SkillExportService {
	return &SkillExportService{
		db:           db,
		mcpService:   mcpService,
		groupService: groupService,
	}
}

// ExportGroupAsSkill exports a group as an Anthropic Skill zip package
func (s *SkillExportService) ExportGroupAsSkill(ctx context.Context, groupID uint, serverAddress string, authToken string) (*bytes.Buffer, string, error) {
	// Get group with services
	group, err := s.groupService.GetGroupByID(ctx, groupID)
	if err != nil {
		return nil, "", err
	}

	// Build the skill zip
	zipBuffer, err := s.buildSkillZip(ctx, group, serverAddress, authToken)
	if err != nil {
		return nil, "", err
	}

	// Generate filename
	skillName := "gpt-load-" + normalizeSkillName(group.Name)
	filename := fmt.Sprintf("%s.zip", skillName)

	return zipBuffer, filename, nil
}

// buildSkillZip creates the skill zip package
func (s *SkillExportService) buildSkillZip(ctx context.Context, group *MCPServiceGroupDTO, serverAddress string, authToken string) (*bytes.Buffer, error) {
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	// 1. Generate SKILL.md
	skillMD := s.generateSkillMD(group)
	if err := addFileToZip(zipWriter, "SKILL.md", skillMD); err != nil {
		return nil, err
	}

	// 2. Generate tools/*.md for each service
	for _, svc := range group.Services {
		toolsMD := s.generateToolsMD(&svc)
		filename := fmt.Sprintf("tools/%s.md", svc.Name)
		if err := addFileToZip(zipWriter, filename, toolsMD); err != nil {
			return nil, err
		}
	}

	// 3. Generate mcp-config.json
	mcpConfig := s.generateMCPConfig(group.Services, serverAddress, authToken)
	if err := addFileToZip(zipWriter, "mcp-config.json", mcpConfig); err != nil {
		return nil, err
	}

	// 4. Generate executor.py
	executorPy := s.generateExecutorPy()
	if err := addFileToZip(zipWriter, "executor.py", executorPy); err != nil {
		return nil, err
	}

	// 5. Generate requirements.txt
	if err := addFileToZip(zipWriter, "requirements.txt", "# Optional dependencies\n# pyyaml>=6.0\n"); err != nil {
		return nil, err
	}

	// Close the writer to finalize the ZIP central directory before returning
	if err := zipWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to finalize zip archive: %w", err)
	}

	return buf, nil
}

// generateSkillMD generates the SKILL.md content
func (s *SkillExportService) generateSkillMD(group *MCPServiceGroupDTO) string {
	var sb strings.Builder

	// Calculate stats
	totalTools := 0
	serviceNames := make([]string, 0, len(group.Services))
	serviceSummaries := make([]string, 0, len(group.Services))
	for _, svc := range group.Services {
		totalTools += svc.ToolCount
		serviceNames = append(serviceNames, svc.Name)
		shortDesc := svc.Description
		if shortDesc == "" {
			shortDesc = svc.DisplayName
		}
		shortDesc = truncateString(shortDesc, 80)
		serviceSummaries = append(serviceSummaries, fmt.Sprintf("%s (%s)", svc.Name, shortDesc))
	}

	// YAML frontmatter
	skillName := "gpt-load-" + normalizeSkillName(group.Name)
	descLine := "External tools: " + strings.Join(serviceSummaries, ", ")
	descLine = truncateString(descLine, 500)

	frontmatter := map[string]interface{}{
		"name":         skillName,
		"display_name": group.DisplayName,
		"description":  descLine,
		"mcp_count":    len(group.Services),
		"tool_count":   totalTools,
		"services":     serviceNames,
	}
	// yaml.Marshal for simple map[string]interface{} with basic types rarely fails
	// If it does fail, use a minimal fallback frontmatter
	frontmatterBytes, err := yaml.Marshal(frontmatter)
	if err != nil {
		frontmatterBytes = []byte(fmt.Sprintf("name: %s\ndisplay_name: %s\n", skillName, group.DisplayName))
	}
	sb.WriteString("---\n")
	sb.WriteString(string(frontmatterBytes))
	sb.WriteString("---\n\n")

	// Title
	sb.WriteString(fmt.Sprintf("# %s\n\n", group.DisplayName))

	// Quick Reference
	sb.WriteString("## Quick Reference\n\n")
	sb.WriteString("| Service | Tool | Description |\n")
	sb.WriteString("|---------|------|-------------|\n")
	for _, svc := range group.Services {
		count := 0
		for _, tool := range svc.Tools {
			if count >= 5 {
				break
			}
			desc := truncateString(tool.Description, 60)
			desc = strings.ReplaceAll(desc, "|", "\\|")
			sb.WriteString(fmt.Sprintf("| %s | `%s` | %s |\n", svc.Name, tool.Name, desc))
			count++
		}
		if len(svc.Tools) > 5 {
			sb.WriteString(fmt.Sprintf("| %s | ... | +%d more tools, see [tools/%s.md](tools/%s.md) |\n",
				svc.Name, len(svc.Tools)-5, svc.Name, svc.Name))
		}
	}
	sb.WriteString("\n")

	// Available Services
	sb.WriteString("## Available Services\n\n")
	for _, svc := range group.Services {
		desc := svc.Description
		if desc == "" {
			desc = svc.DisplayName
		}
		sb.WriteString(fmt.Sprintf("### %s (%d tools)\n\n", svc.Name, svc.ToolCount))
		sb.WriteString(fmt.Sprintf("%s\n\n", desc))
		sb.WriteString(fmt.Sprintf("- [View all tools](tools/%s.md)\n", svc.Name))

		if svc.ToolCount > 0 {
			toolNames := make([]string, 0, svc.ToolCount)
			for _, t := range svc.Tools {
				toolNames = append(toolNames, fmt.Sprintf("`%s`", t.Name))
			}
			sb.WriteString(fmt.Sprintf("- Tools: %s\n", strings.Join(toolNames, ", ")))
		}
		sb.WriteString("\n")
	}

	// How to Use
	sb.WriteString("## How to Use\n\n")
	sb.WriteString("1. Find the tool you need in the Quick Reference table above\n")
	sb.WriteString("2. Read detailed documentation from `tools/{service-name}.md`\n")
	sb.WriteString("3. Execute using the syntax below\n\n")

	// Execution Syntax
	sb.WriteString("## Execution Syntax\n\n")
	sb.WriteString("```bash\n")
	sb.WriteString("python executor.py <service-name> <tool-name> '<json-params>'\n")
	sb.WriteString("```\n\n")

	return sb.String()
}

// generateToolsMD generates the tools documentation for a service
func (s *SkillExportService) generateToolsMD(svc *MCPServiceDTO) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("# %s Tools\n\n", svc.DisplayName))

	for _, tool := range svc.Tools {
		sb.WriteString(fmt.Sprintf("## %s\n\n", tool.Name))
		if tool.Description != "" {
			sb.WriteString(tool.Description + "\n\n")
		}

		if len(tool.InputSchema) > 0 {
			sb.WriteString("**Params:**\n")
			sb.WriteString("```yaml\n")
			paramsYAML := convertInputSchemaToYAML(tool.InputSchema)
			sb.WriteString(paramsYAML)
			sb.WriteString("```\n\n")
		}
	}

	return sb.String()
}

// generateMCPConfig generates the mcp-config.json content
func (s *SkillExportService) generateMCPConfig(services []MCPServiceDTO, serverAddress string, authToken string) string {
	config := map[string]interface{}{
		"mcpServers": map[string]interface{}{},
	}

	mcpServers := config["mcpServers"].(map[string]interface{})
	for _, svc := range services {
		url := fmt.Sprintf("%s/mcp/%s?key=%s", serverAddress, svc.Name, authToken)
		mcpServers[svc.Name] = map[string]string{
			"url": url,
		}
	}

	// json.MarshalIndent for simple map structures rarely fails
	// If it does fail, return a minimal valid JSON
	jsonBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return `{"mcpServers": {}}`
	}
	return string(jsonBytes)
}

// generateExecutorPy generates the executor.py script
func (s *SkillExportService) generateExecutorPy() string {
	return `#!/usr/bin/env python3
"""
MCP Tool Executor
Execute MCP tools from command line.

Usage:
    python executor.py <service-name> <tool-name> '<json-params>'

Example:
    python executor.py exa-search search '{"query": "AI news"}'
"""

import json
import sys
import urllib.request
import urllib.error

def load_config():
    """Load MCP configuration from mcp-config.json"""
    with open('mcp-config.json', 'r') as f:
        return json.load(f)

def execute_tool(service_name: str, tool_name: str, params: dict) -> dict:
    """Execute an MCP tool and return the result"""
    config = load_config()

    if service_name not in config.get('mcpServers', {}):
        raise ValueError(f"Service '{service_name}' not found in configuration")

    server_config = config['mcpServers'][service_name]
    url = server_config['url']

    # Build the request
    request_data = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "tools/call",
        "params": {
            "name": tool_name,
            "arguments": params
        }
    }

    data = json.dumps(request_data).encode('utf-8')
    req = urllib.request.Request(
        url,
        data=data,
        headers={'Content-Type': 'application/json'}
    )

    try:
        with urllib.request.urlopen(req) as response:
            result = json.loads(response.read().decode('utf-8'))
            return result
    except urllib.error.HTTPError as e:
        error_body = e.read().decode('utf-8')
        raise RuntimeError(f"HTTP {e.code}: {error_body}")

def main():
    if len(sys.argv) < 4:
        print(__doc__)
        sys.exit(1)

    service_name = sys.argv[1]
    tool_name = sys.argv[2]
    params_json = sys.argv[3]

    try:
        params = json.loads(params_json)
    except json.JSONDecodeError as e:
        print(f"Error: Invalid JSON parameters: {e}")
        sys.exit(1)

    try:
        result = execute_tool(service_name, tool_name, params)
        print(json.dumps(result, indent=2, ensure_ascii=False))
    except Exception as e:
        print(f"Error: {e}")
        sys.exit(1)

if __name__ == '__main__':
    main()
`
}

// Helper functions

func addFileToZip(zipWriter *zip.Writer, filename string, content string) error {
	writer, err := zipWriter.Create(filename)
	if err != nil {
		return err
	}
	_, err = writer.Write([]byte(content))
	return err
}

func normalizeSkillName(name string) string {
	return strings.ReplaceAll(name, "_", "-")
}

func truncateString(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-3]) + "..."
}

func convertInputSchemaToYAML(schema map[string]interface{}) string {
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return ""
	}

	required := make(map[string]bool)
	if reqList, ok := schema["required"].([]interface{}); ok {
		for _, r := range reqList {
			if rs, ok := r.(string); ok {
				required[rs] = true
			}
		}
	}

	params := make(map[string]map[string]interface{})
	for name, prop := range properties {
		propMap, ok := prop.(map[string]interface{})
		if !ok {
			continue
		}

		param := make(map[string]interface{})
		if t, ok := propMap["type"].(string); ok {
			param["type"] = t
		}
		if d, ok := propMap["description"].(string); ok {
			param["desc"] = d
		}
		if e, ok := propMap["enum"]; ok {
			param["enum"] = e
		}
		if def, ok := propMap["default"]; ok {
			param["default"] = def
		}
		if required[name] {
			param["required"] = true
		}
		params[name] = param
	}

	// yaml.Marshal for map structures rarely fails
	// If it does fail, return empty string
	yamlBytes, err := yaml.Marshal(params)
	if err != nil {
		return ""
	}
	return string(yamlBytes)
}
