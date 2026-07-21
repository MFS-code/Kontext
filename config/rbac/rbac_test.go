package rbac_test

import (
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

type document struct {
	APIVersion string              `json:"apiVersion"`
	Kind       string              `json:"kind"`
	Metadata   metav1.ObjectMeta   `json:"metadata"`
	Rules      []rbacv1.PolicyRule `json:"rules"`
	RoleRef    rbacv1.RoleRef      `json:"roleRef"`
	Subjects   []rbacv1.Subject    `json:"subjects"`
}

type kustomization struct {
	Resources []string `json:"resources"`
}

func TestLeaderElectionRBACIsSeparatedFromWebhookCertificates(t *testing.T) {
	webhookDocuments := readDocuments(t, "webhook_role.yaml")
	webhookRole := requireDocument(t, webhookDocuments, "Role", "webhook-certificate-manager")
	for _, rule := range webhookRole.Rules {
		if slices.Contains(rule.APIGroups, "coordination.k8s.io") ||
			slices.Contains(rule.Resources, "leases") {
			t.Fatal("webhook certificate Role absorbed leader-election permissions")
		}
	}

	leaderDocuments := readDocuments(t, "leader_election_role.yaml")
	leaderRole := requireDocument(t, leaderDocuments, "Role", "leader-election-manager")
	if len(leaderRole.Rules) != 2 {
		t.Fatalf("leader-election Role has %d rules, want 2", len(leaderRole.Rules))
	}
	assertLeaseRule(t, leaderRole.Rules[0], nil, []string{"create"})
	assertLeaseRule(t, leaderRole.Rules[1], []string{"kontext.dev"}, []string{"get", "update"})

	leaderBinding := requireDocument(t, leaderDocuments, "RoleBinding", "leader-election-manager")
	if leaderBinding.RoleRef.Kind != "Role" ||
		leaderBinding.RoleRef.Name != "leader-election-manager" {
		t.Fatalf("leader-election RoleBinding targets %#v", leaderBinding.RoleRef)
	}
	if len(leaderBinding.Subjects) != 1 ||
		leaderBinding.Subjects[0].Name != "controller-manager" ||
		leaderBinding.Subjects[0].Namespace != "kontext-system" {
		t.Fatalf("leader-election RoleBinding subjects = %#v", leaderBinding.Subjects)
	}

	data, err := os.ReadFile("kustomization.yaml")
	if err != nil {
		t.Fatalf("read RBAC kustomization: %v", err)
	}
	var config kustomization
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatalf("decode RBAC kustomization: %v", err)
	}
	if !slices.Contains(config.Resources, "leader_election_role.yaml") {
		t.Fatal("RBAC kustomization omits the dedicated leader-election Role")
	}
}

func assertLeaseRule(
	t *testing.T,
	rule rbacv1.PolicyRule,
	resourceNames []string,
	verbs []string,
) {
	t.Helper()
	if !slices.Equal(rule.APIGroups, []string{"coordination.k8s.io"}) ||
		!slices.Equal(rule.Resources, []string{"leases"}) ||
		!slices.Equal(rule.ResourceNames, resourceNames) ||
		!slices.Equal(rule.Verbs, verbs) {
		t.Fatalf("unexpected leader-election rule: %#v", rule)
	}
}

func readDocuments(t *testing.T, path string) []document {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	}()

	decoder := utilyaml.NewYAMLOrJSONDecoder(file, 4096)
	var documents []document
	for {
		var current document
		err := decoder.Decode(&current)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if strings.TrimSpace(current.Kind) != "" {
			documents = append(documents, current)
		}
	}
	return documents
}

func requireDocument(t *testing.T, documents []document, kind, name string) document {
	t.Helper()
	for _, current := range documents {
		if current.Kind == kind && current.Metadata.Name == name {
			return current
		}
	}
	t.Fatalf("%s %s not found", kind, name)
	return document{}
}
