package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// ServiceBay's own consistency suite only sees templates in that repo, so the
// rules it would enforce for templates/usage-metrics/ are checked here instead:
// a typo in an annotation or an undeclared {{VAR}} would otherwise surface as a
// failed install on the box.
const (
	templateYAML = "templates/usage-metrics/template.yml"
	templateVars = "templates/usage-metrics/variables.json"
)

// Declared in templates/settings.json in ServiceBay; templates reference them
// without re-declaring (#2425).
var globalVars = map[string]bool{"DATA_DIR": true, "PUBLIC_DOMAIN": true, "LAN_IP": true, "HOST_GATEWAY_IP": true}

var mustacheVar = regexp.MustCompile(`\{\{\s*[#^/]?\s*([A-Z_][A-Z0-9_]*)\s*\}\}`)

// stripComments drops whole-line YAML comments, so the rules below read the
// manifest itself and not the prose explaining it.
func stripComments(yaml string) string {
	kept := []string{}
	for _, line := range strings.Split(yaml, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func readTemplate(t *testing.T) (string, map[string]map[string]any) {
	t.Helper()
	yaml, err := os.ReadFile(templateYAML)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	raw, err := os.ReadFile(templateVars)
	if err != nil {
		t.Fatalf("read variables: %v", err)
	}
	var vars map[string]map[string]any
	if err := json.Unmarshal(raw, &vars); err != nil {
		t.Fatalf("parse variables.json: %v", err)
	}
	return stripComments(string(yaml)), vars
}

func TestTemplateCarriesTheMandatoryAnnotations(t *testing.T) {
	yaml, _ := readTemplate(t)

	for _, annotation := range []string{
		`servicebay.label: "Usage Metrics"`,
		`servicebay.schema-version: "1"`,
		`servicebay.ports: "{{USAGE_METRICS_PORT}}/tcp"`,
		"servicebay.healthcheck: |",
		"url: http://localhost:{{USAGE_METRICS_PORT}}/healthz",
	} {
		if !strings.Contains(yaml, annotation) {
			t.Errorf("template.yml is missing %q", annotation)
		}
	}
}

func TestTemplateVariablesAndPlaceholdersMatch(t *testing.T) {
	yaml, vars := readTemplate(t)

	used := map[string]bool{}
	for _, m := range mustacheVar.FindAllStringSubmatch(yaml, -1) {
		used[m[1]] = true
	}
	for name := range used {
		if _, ok := vars[name]; !ok && !globalVars[name] {
			t.Errorf("{{%s}} is used in template.yml but declared nowhere", name)
		}
	}
	for name := range vars {
		if !used[name] {
			t.Errorf("variable %s is declared but never rendered", name)
		}
	}
}

// ADR 0007: an isolated netns with an explicit hostPort. A published port
// without one deploys cleanly and is unreachable.
func TestTemplateStaysOffHostNetworkAndPublishesEveryPort(t *testing.T) {
	yaml, _ := readTemplate(t)

	if strings.Contains(yaml, "hostNetwork") {
		t.Error("template.yml sets hostNetwork; this service has no reason to leave its own netns")
	}
	lines := strings.Split(yaml, "\n")
	ports := 0
	for i, line := range lines {
		if !strings.Contains(line, "containerPort:") {
			continue
		}
		ports++
		if i+1 >= len(lines) || !strings.Contains(lines[i+1], "hostPort:") {
			t.Errorf("containerPort on line %d has no hostPort — unreachable from the host", i+1)
		}
	}
	if ports == 0 {
		t.Error("template.yml publishes no container port")
	}
}

// The counter database is the whole state of the service: it has to live on the
// mount, at the path the app actually opens.
func TestTemplateMountsTheDataPathTheAppWritesTo(t *testing.T) {
	yaml, _ := readTemplate(t)

	if !strings.Contains(yaml, "value: "+defaultDBPath) {
		t.Errorf("template.yml does not set USAGE_METRICS_DB_PATH to %s", defaultDBPath)
	}
	if !strings.Contains(yaml, "mountPath: /data") || !strings.Contains(yaml, "path: {{DATA_DIR}}/usage-metrics") {
		t.Error("template.yml does not bind-mount {{DATA_DIR}}/usage-metrics at /data")
	}
	if strings.Contains(yaml, ".mustache") {
		t.Error("a companion config file would be re-rendered on every deploy over the data directory")
	}
}

// No literal credential in the repo: the token is generated per install.
func TestTemplateDeclaresTheTokenAsAGeneratedSecret(t *testing.T) {
	yaml, vars := readTemplate(t)

	token, ok := vars["USAGE_METRICS_TOKEN"]
	if !ok {
		t.Fatal("USAGE_METRICS_TOKEN is not declared")
	}
	if token["type"] != "secret" {
		t.Errorf(`USAGE_METRICS_TOKEN type = %v, want "secret"`, token["type"])
	}
	if _, hasDefault := token["default"]; hasDefault {
		t.Error("USAGE_METRICS_TOKEN carries a default; a secret variable must have no literal value")
	}
	if !strings.Contains(yaml, `value: "{{USAGE_METRICS_TOKEN}}"`) {
		t.Error("template.yml does not inject the token as a placeholder")
	}
}
