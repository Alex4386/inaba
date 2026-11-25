// openapihost fixes the generated OpenAPI document so it describes the API as
// inaba-server serves it. The generator writes a host's operations at the paths the
// host itself answers (/v1/threads); callers reach them through the server, under the
// host's id (/v1/hosts/{host_id}/threads). Run by `make gen`.
package main

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The services a host serves. Every other service is the server's own.
var hostServices = map[string]bool{"ThreadService": true, "ProjectService": true, "TerminalService": true, "GitService": true, "HousekeepingService": true, "AutomationService": true, "ModuleService": true, "McpService": true, "BrowserService": true, "FileService": true, "ShellService": true, "ProviderConfigService": true, "SystemService": true, "UsageService": true}

const hostParameter = `
name: host_id
in: path
required: true
description: The id of the host, from GET /v1/hosts.
schema:
    type: string
`

// child returns the value under key in a mapping node, or nil.
func child(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func main() {
	path := os.Args[1]
	raw, err := os.ReadFile(path)
	check(err)
	var doc yaml.Node
	check(yaml.Unmarshal(raw, &doc))

	paths := child(doc.Content[0], "paths")
	moved := 0
	for i := 0; i+1 < len(paths.Content); i += 2 {
		key, item := paths.Content[i], paths.Content[i+1]
		onHost := false
		for j := 1; j < len(item.Content); j += 2 {
			if tags := child(item.Content[j], "tags"); tags != nil && len(tags.Content) > 0 && hostServices[tags.Content[0].Value] {
				onHost = true
			}
		}
		if !onHost {
			continue
		}
		key.Value = "/v1/hosts/{host_id}" + strings.TrimPrefix(key.Value, "/v1")
		moved++
		for j := 1; j < len(item.Content); j += 2 {
			operation := item.Content[j]
			var parameter yaml.Node
			check(yaml.Unmarshal([]byte(hostParameter), &parameter))
			parameters := child(operation, "parameters")
			if parameters == nil {
				parameters = &yaml.Node{Kind: yaml.SequenceNode}
				operation.Content = append(operation.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "parameters"}, parameters)
			}
			parameters.Content = append([]*yaml.Node{parameter.Content[0]}, parameters.Content...)
		}
	}
	if moved == 0 {
		check(fmt.Errorf("%s has no host operations; did the service names change?", path))
	}
	out, err := yaml.Marshal(&doc)
	check(err)
	check(os.WriteFile(path, out, 0o644))
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "openapihost:", err)
		os.Exit(1)
	}
}
