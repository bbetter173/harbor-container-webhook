package webhook

import (
	"context"
	"testing"

	"github.com/indeedeng-alpha/harbor-container-webhook/internal/config"

	"github.com/stretchr/testify/require"
)

func TestPodContainerProxier_rewriteImage(t *testing.T) {
	transformers, err := MakeTransformers([]config.ProxyRule{
		{
			Name:     "docker.io proxy cache except ubuntu",
			Matches:  []string{"^docker.io"},
			Excludes: []string{"^docker.io/(library/)?ubuntu:.*$"},
			Replace:  "harbor.example.com/dockerhub-proxy",
		},
		{
			Name:    "quay.io proxy cache",
			Matches: []string{"^quay.io"},
			Replace: "harbor.example.com/quay-proxy",
		},
		{
			Name:    "docker.io proxy cache but only ubuntu",
			Matches: []string{"^docker.io/(library/)?ubuntu"},
			Replace: "harbor.example.com/ubuntu-proxy",
		},
	}, nil)
	require.NoError(t, err)
	proxier := PodContainerProxier{
		Transformers: transformers,
	}

	type testcase struct {
		name     string
		image    string
		platform string
		os       string
		expected string
	}
	tests := []testcase{
		{
			name:     "an image from quay should be rewritten",
			image:    "quay.io/bitnami/sealed-secrets-controller:latest",
			os:       "linux",
			platform: "amd64",
			expected: "harbor.example.com/quay-proxy/bitnami/sealed-secrets-controller:latest",
		},
		{
			name:     "an image from quay without a tag should be rewritten",
			image:    "quay.io/bitnami/sealed-secrets-controller",
			os:       "linux",
			platform: "amd64",
			expected: "harbor.example.com/quay-proxy/bitnami/sealed-secrets-controller:latest",
		},
		{
			name:     "an image from docker.io with ubuntu should be rewritten to the ubuntu proxy",
			image:    "docker.io/library/ubuntu:latest",
			os:       "linux",
			platform: "amd64",
			expected: "harbor.example.com/ubuntu-proxy/library/ubuntu:latest",
		},
		{
			name:     "a bare ubuntu image from docker.io should be rewritten to the ubuntu proxy",
			image:    "ubuntu",
			os:       "linux",
			platform: "amd64",
			expected: "harbor.example.com/ubuntu-proxy/library/ubuntu:latest",
		},
		{
			name:     "an image from docker.io should be rewritten",
			image:    "docker.io/library/centos:latest",
			os:       "linux",
			platform: "amd64",
			expected: "harbor.example.com/dockerhub-proxy/library/centos:latest",
		},
		{
			name:     "a bare image from docker.io should be rewritten",
			image:    "centos",
			os:       "linux",
			platform: "amd64",
			expected: "harbor.example.com/dockerhub-proxy/library/centos:latest",
		},
		{
			name:     "an image from gcr should not be rewritten",
			image:    "k8s.gcr.io/kubernetes",
			os:       "linux",
			platform: "amd64",
			expected: "k8s.gcr.io/kubernetes",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rewritten, err := proxier.rewriteImage(context.TODO(), tc.image)
			require.NoError(t, err)
			require.Equal(t, tc.expected, rewritten)
		})
	}
}

func TestPodContainerProxier_filterTransformersByNamespace(t *testing.T) {
	// Create transformers with different namespace filtering rules
	transformers, err := MakeTransformers([]config.ProxyRule{
		{
			Name:             "kube-system-only",
			Matches:          []string{"^docker.io"},
			Replace:          "harbor.example.com/kube-proxy",
			NamespaceMatches: []string{"^kube-system$"},
		},
		{
			Name:              "exclude-kube-namespaces",
			Matches:           []string{"^quay.io"},
			Replace:           "harbor.example.com/quay-proxy",
			NamespaceExcludes: []string{"^kube-.*$"},
		},
		{
			Name:    "no-namespace-filter",
			Matches: []string{"^gcr.io"},
			Replace: "harbor.example.com/gcr-proxy",
		},
		{
			Name:              "complex-namespace-rules",
			Matches:           []string{"^registry.io"},
			Replace:           "harbor.example.com/registry-proxy",
			NamespaceMatches:  []string{"^prod-.*$", "^staging-.*$"},
			NamespaceExcludes: []string{"^prod-test$"},
		},
	}, nil)
	require.NoError(t, err)

	proxier := PodContainerProxier{
		Transformers: transformers,
	}

	tests := []struct {
		name                     string
		namespace                string
		expectedTransformerNames []string
	}{
		{
			name:      "kube-system namespace should get kube-system-only and no-namespace-filter",
			namespace: "kube-system",
			expectedTransformerNames: []string{
				"kube-system-only",
				"no-namespace-filter",
			},
		},
		{
			name:      "kube-public namespace should get no-namespace-filter only",
			namespace: "kube-public",
			expectedTransformerNames: []string{
				"no-namespace-filter",
			},
		},
		{
			name:      "default namespace should get exclude-kube-namespaces and no-namespace-filter",
			namespace: "default",
			expectedTransformerNames: []string{
				"exclude-kube-namespaces",
				"no-namespace-filter",
			},
		},
		{
			name:      "prod-main namespace should get complex-namespace-rules and others except kube-system-only",
			namespace: "prod-main",
			expectedTransformerNames: []string{
				"exclude-kube-namespaces",
				"no-namespace-filter",
				"complex-namespace-rules",
			},
		},
		{
			name:      "prod-test namespace should get exclude-kube-namespaces and no-namespace-filter (excluded from complex)",
			namespace: "prod-test",
			expectedTransformerNames: []string{
				"exclude-kube-namespaces",
				"no-namespace-filter",
			},
		},
		{
			name:      "staging-app namespace should get complex-namespace-rules and others except kube-system-only",
			namespace: "staging-app",
			expectedTransformerNames: []string{
				"exclude-kube-namespaces",
				"no-namespace-filter",
				"complex-namespace-rules",
			},
		},
		{
			name:      "random namespace should get exclude-kube-namespaces and no-namespace-filter only",
			namespace: "random",
			expectedTransformerNames: []string{
				"exclude-kube-namespaces",
				"no-namespace-filter",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			filtered := proxier.filterTransformersByNamespace(tc.namespace)

			// Check that we got the expected number of transformers
			require.Len(t, filtered, len(tc.expectedTransformerNames))

			// Check that we got the expected transformers by name
			actualNames := make([]string, len(filtered))
			for i, transformer := range filtered {
				actualNames[i] = transformer.Name()
			}

			require.ElementsMatch(t, tc.expectedTransformerNames, actualNames)
		})
	}
}
