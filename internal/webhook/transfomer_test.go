package webhook

import (
	"testing"

	"github.com/indeedeng-alpha/harbor-container-webhook/internal/config"

	"github.com/stretchr/testify/require"
)

func TestRuleTransformer_RewriteImage(t *testing.T) {
	transformer, err := newRuleTransformer(config.ProxyRule{
		Name:     "test rules",
		Matches:  []string{"^docker.io"},
		Excludes: []string{"^docker.io/(library/)?ubuntu:.*$"},
		Replace:  "harbor.example.com/dockerhub-proxy",
	})
	require.NoError(t, err)

	type testcase struct {
		name     string
		image    string
		platform string
		os       string
		expected string
	}
	tests := []testcase{
		{
			name:     "an image from quay should not be rewritten",
			image:    "quay.io/bitnami/sealed-secrets-controller:latest",
			os:       "linux",
			platform: "amd64",
			expected: "quay.io/bitnami/sealed-secrets-controller:latest",
		},
		{
			name:     "an image from quay without a tag should not be rewritten",
			image:    "quay.io/bitnami/sealed-secrets-controller",
			os:       "linux",
			platform: "amd64",
			expected: "quay.io/bitnami/sealed-secrets-controller",
		},
		{
			name:     "an image from dockerhub explicitly excluded should not be rewritten",
			image:    "docker.io/library/ubuntu:latest",
			os:       "linux",
			platform: "amd64",
			expected: "docker.io/library/ubuntu:latest",
		},
		{
			name:     "a bare image from dockerhub explicitly excluded should not be rewritten",
			image:    "ubuntu",
			os:       "linux",
			platform: "amd64",
			expected: "ubuntu",
		},
		{
			name:     "an image from dockerhub should be rewritten",
			image:    "docker.io/library/centos:latest",
			os:       "linux",
			platform: "amd64",
			expected: "harbor.example.com/dockerhub-proxy/library/centos:latest",
		},
		{
			name:     "an image from the std library should be rewritten",
			image:    "centos",
			os:       "linux",
			platform: "amd64",
			expected: "harbor.example.com/dockerhub-proxy/library/centos:latest",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rewritten, err := transformer.RewriteImage(tc.image)
			require.NoError(t, err)
			require.Equal(t, tc.expected, rewritten)
		})
	}
}

func TestRuleTransformer_NamespaceFilteringErrors(t *testing.T) {
	tests := []struct {
		name              string
		namespaceMatches  []string
		namespaceExcludes []string
		expectError       bool
	}{
		{
			name:             "valid namespace matches regex",
			namespaceMatches: []string{"^kube-.*$"},
			expectError:      false,
		},
		{
			name:              "valid namespace excludes regex",
			namespaceExcludes: []string{"^kube-.*$"},
			expectError:       false,
		},
		{
			name:             "invalid namespace matches regex",
			namespaceMatches: []string{"[invalid"},
			expectError:      true,
		},
		{
			name:              "invalid namespace excludes regex",
			namespaceExcludes: []string{"[invalid"},
			expectError:       true,
		},
		{
			name:             "multiple valid namespace matches",
			namespaceMatches: []string{"^kube-.*$", "^default$"},
			expectError:      false,
		},
		{
			name:              "multiple valid namespace excludes",
			namespaceExcludes: []string{"^kube-.*$", "^system.*$"},
			expectError:       false,
		},
		{
			name:             "one invalid among multiple namespace matches",
			namespaceMatches: []string{"^kube-.*$", "[invalid"},
			expectError:      true,
		},
		{
			name:              "one invalid among multiple namespace excludes",
			namespaceExcludes: []string{"^kube-.*$", "[invalid"},
			expectError:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newRuleTransformer(config.ProxyRule{
				Name:              "test rule",
				Matches:           []string{"^docker.io"},
				Replace:           "harbor.example.com/proxy",
				NamespaceMatches:  tc.namespaceMatches,
				NamespaceExcludes: tc.namespaceExcludes,
			})

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "failed to compile namespace")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRuleTransformer_NamespaceFilteringEdgeCases(t *testing.T) {
	transformer, err := newRuleTransformer(config.ProxyRule{
		Name:              "test rule",
		Matches:           []string{"^docker.io"},
		Replace:           "harbor.example.com/proxy",
		NamespaceMatches:  []string{"^prod-.*$"},
		NamespaceExcludes: []string{"^prod-test$"},
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		namespace string
		expected  bool
	}{
		{
			name:      "empty namespace string should not match",
			namespace: "",
			expected:  false,
		},
		{
			name:      "whitespace-only namespace should not match",
			namespace: "   ",
			expected:  false,
		},
		{
			name:      "namespace with special characters",
			namespace: "prod-app-123_test",
			expected:  true,
		},
		{
			name:      "namespace with hyphens",
			namespace: "prod-my-app",
			expected:  true,
		},
		{
			name:      "namespace with numbers",
			namespace: "prod-123",
			expected:  true,
		},
		{
			name:      "case sensitive matching - uppercase should not match",
			namespace: "PROD-app",
			expected:  false,
		},
		{
			name:      "partial match should not work",
			namespace: "my-prod-app",
			expected:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := transformer.ShouldApplyToNamespace(tc.namespace)
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestRuleTransformer_ShouldApplyToNamespace(t *testing.T) {
	tests := []struct {
		name              string
		namespaceMatches  []string
		namespaceExcludes []string
		namespace         string
		expected          bool
	}{
		{
			name:      "no namespace filters - should apply to any namespace",
			namespace: "default",
			expected:  true,
		},
		{
			name:      "no namespace filters - should apply to kube-system",
			namespace: "kube-system",
			expected:  true,
		},
		{
			name:             "namespace matches exact - should apply",
			namespaceMatches: []string{"^kube-system$"},
			namespace:        "kube-system",
			expected:         true,
		},
		{
			name:             "namespace matches exact - should not apply to different namespace",
			namespaceMatches: []string{"^kube-system$"},
			namespace:        "default",
			expected:         false,
		},
		{
			name:             "namespace matches pattern - should apply",
			namespaceMatches: []string{"^kube-.*$"},
			namespace:        "kube-system",
			expected:         true,
		},
		{
			name:             "namespace matches pattern - should apply to kube-public",
			namespaceMatches: []string{"^kube-.*$"},
			namespace:        "kube-public",
			expected:         true,
		},
		{
			name:             "namespace matches pattern - should not apply to default",
			namespaceMatches: []string{"^kube-.*$"},
			namespace:        "default",
			expected:         false,
		},
		{
			name:             "multiple namespace matches - should apply to first match",
			namespaceMatches: []string{"^kube-system$", "^default$"},
			namespace:        "kube-system",
			expected:         true,
		},
		{
			name:             "multiple namespace matches - should apply to second match",
			namespaceMatches: []string{"^kube-system$", "^default$"},
			namespace:        "default",
			expected:         true,
		},
		{
			name:             "multiple namespace matches - should not apply to non-match",
			namespaceMatches: []string{"^kube-system$", "^default$"},
			namespace:        "production",
			expected:         false,
		},
		{
			name:              "namespace excludes exact - should not apply",
			namespaceExcludes: []string{"^kube-system$"},
			namespace:         "kube-system",
			expected:          false,
		},
		{
			name:              "namespace excludes exact - should apply to different namespace",
			namespaceExcludes: []string{"^kube-system$"},
			namespace:         "default",
			expected:          true,
		},
		{
			name:              "namespace excludes pattern - should not apply to kube-system",
			namespaceExcludes: []string{"^kube-.*$"},
			namespace:         "kube-system",
			expected:          false,
		},
		{
			name:              "namespace excludes pattern - should not apply to kube-public",
			namespaceExcludes: []string{"^kube-.*$"},
			namespace:         "kube-public",
			expected:          false,
		},
		{
			name:              "namespace excludes pattern - should apply to default",
			namespaceExcludes: []string{"^kube-.*$"},
			namespace:         "default",
			expected:          true,
		},
		{
			name:              "multiple namespace excludes - should not apply to first exclude",
			namespaceExcludes: []string{"^kube-system$", "^kube-public$"},
			namespace:         "kube-system",
			expected:          false,
		},
		{
			name:              "multiple namespace excludes - should not apply to second exclude",
			namespaceExcludes: []string{"^kube-system$", "^kube-public$"},
			namespace:         "kube-public",
			expected:          false,
		},
		{
			name:              "multiple namespace excludes - should apply to non-excluded",
			namespaceExcludes: []string{"^kube-system$", "^kube-public$"},
			namespace:         "default",
			expected:          true,
		},
		{
			name:              "both matches and excludes - match wins over exclude",
			namespaceMatches:  []string{"^kube-.*$"},
			namespaceExcludes: []string{"^kube-system$"},
			namespace:         "kube-system",
			expected:          false, // exclude takes precedence
		},
		{
			name:              "both matches and excludes - should apply when matches but not excluded",
			namespaceMatches:  []string{"^kube-.*$"},
			namespaceExcludes: []string{"^kube-system$"},
			namespace:         "kube-public",
			expected:          true,
		},
		{
			name:              "both matches and excludes - should not apply when doesn't match",
			namespaceMatches:  []string{"^kube-.*$"},
			namespaceExcludes: []string{"^kube-system$"},
			namespace:         "default",
			expected:          false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transformer, err := newRuleTransformer(config.ProxyRule{
				Name:              "test rule",
				Matches:           []string{"^docker.io"},
				Replace:           "harbor.example.com/proxy",
				NamespaceMatches:  tc.namespaceMatches,
				NamespaceExcludes: tc.namespaceExcludes,
			})
			require.NoError(t, err)

			result := transformer.ShouldApplyToNamespace(tc.namespace)
			require.Equal(t, tc.expected, result)
		})
	}
}
