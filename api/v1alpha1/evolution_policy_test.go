package v1alpha1_test

import (
	"os"
	"reflect"
	"sort"
	"testing"

	"sigs.k8s.io/yaml"
)

type crdDocument struct {
	Spec struct {
		Versions []struct {
			Name   string `yaml:"name"`
			Schema struct {
				OpenAPIV3Schema map[string]any `yaml:"openAPIV3Schema"`
			} `yaml:"schema"`
		} `yaml:"versions"`
	} `yaml:"spec"`
}

func TestCRDsConfineSchemalessJSONToDocumentedFields(t *testing.T) {
	for name, test := range map[string]struct {
		path string
		want []string
	}{
		"Agent": {
			path: "../../config/crd/bases/kontext.dev_agents.yaml",
		},
		"AgentRun": {
			path: "../../config/crd/bases/kontext.dev_agentruns.yaml",
			want: []string{"$.status.output.value"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatalf("read CRD: %v", err)
			}
			var crd crdDocument
			if err := yaml.Unmarshal(data, &crd); err != nil {
				t.Fatalf("decode CRD: %v", err)
			}

			var preserved []string
			for _, version := range crd.Spec.Versions {
				collectPreservedPaths(version.Schema.OpenAPIV3Schema, "$", &preserved)
			}
			sort.Strings(preserved)
			if !reflect.DeepEqual(preserved, test.want) {
				t.Fatalf("unexpected schemaless CRD fields: got %v want %v", preserved, test.want)
			}
		})
	}
}

func collectPreservedPaths(node map[string]any, path string, paths *[]string) {
	if preserved, _ := node["x-kubernetes-preserve-unknown-fields"].(bool); preserved {
		*paths = append(*paths, path)
	}
	if properties, ok := node["properties"].(map[string]any); ok {
		for name, value := range properties {
			if property, ok := value.(map[string]any); ok {
				collectPreservedPaths(property, path+"."+name, paths)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		collectPreservedPaths(items, path+"[]", paths)
	}
	if additional, ok := node["additionalProperties"].(map[string]any); ok {
		collectPreservedPaths(additional, path+"{}", paths)
	}
}
