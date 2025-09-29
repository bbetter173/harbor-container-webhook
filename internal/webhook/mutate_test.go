package webhook

import (
	"context"
	"testing"

	"github.com/indeedeng-alpha/harbor-container-webhook/internal/config"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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
			rewritten, imagePullSecrets, err := proxier.rewriteImage(context.TODO(), tc.image)
			require.NoError(t, err)
			require.Equal(t, tc.expected, rewritten)
			// Image pull secrets should be empty for these test cases since the rules don't define any
			require.Empty(t, imagePullSecrets)
		})
	}
}

func TestPodContainerProxier_rewriteImageWithSecrets(t *testing.T) {
	transformers, err := MakeTransformers([]config.ProxyRule{
		{
			Name:    "docker.io proxy with secrets",
			Matches: []string{"^docker.io"},
			Replace: "harbor.example.com/dockerhub-proxy",
			ImagePullSecrets: []config.ImagePullSecret{
				{Name: "harbor-secret"},
				{Name: "backup-secret"},
			},
		},
		{
			Name:    "quay.io proxy without secrets",
			Matches: []string{"^quay.io"},
			Replace: "harbor.example.com/quay-proxy",
		},
	}, nil)
	require.NoError(t, err)
	proxier := PodContainerProxier{
		Transformers: transformers,
	}

	tests := []struct {
		name                string
		image               string
		expectedImage       string
		expectedSecretNames []string
		shouldRewrite       bool
	}{
		{
			name:                "docker.io image should be rewritten with secrets",
			image:               "docker.io/library/nginx:latest",
			expectedImage:       "harbor.example.com/dockerhub-proxy/library/nginx:latest",
			expectedSecretNames: []string{"harbor-secret", "backup-secret"},
			shouldRewrite:       true,
		},
		{
			name:                "quay.io image should be rewritten without secrets",
			image:               "quay.io/bitnami/nginx:latest",
			expectedImage:       "harbor.example.com/quay-proxy/bitnami/nginx:latest",
			expectedSecretNames: []string{},
			shouldRewrite:       true,
		},
		{
			name:                "gcr.io image should not be rewritten",
			image:               "gcr.io/project/image:latest",
			expectedImage:       "gcr.io/project/image:latest",
			expectedSecretNames: []string{},
			shouldRewrite:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rewritten, imagePullSecrets, err := proxier.rewriteImage(context.TODO(), tc.image)
			require.NoError(t, err)
			require.Equal(t, tc.expectedImage, rewritten)

			if tc.shouldRewrite {
				require.Len(t, imagePullSecrets, len(tc.expectedSecretNames))
				actualNames := make([]string, len(imagePullSecrets))
				for i, secret := range imagePullSecrets {
					actualNames[i] = secret.Name
				}
				require.ElementsMatch(t, tc.expectedSecretNames, actualNames)
			} else {
				require.Empty(t, imagePullSecrets)
			}
		})
	}
}

func TestPodContainerProxier_mergeImagePullSecrets(t *testing.T) {
	proxier := PodContainerProxier{}

	tests := []struct {
		name     string
		existing []corev1.LocalObjectReference
		toAdd    []corev1.LocalObjectReference
		expected []string
	}{
		{
			name:     "merge with empty existing",
			existing: []corev1.LocalObjectReference{},
			toAdd: []corev1.LocalObjectReference{
				{Name: "secret1"},
				{Name: "secret2"},
			},
			expected: []string{"secret1", "secret2"},
		},
		{
			name: "merge with existing secrets",
			existing: []corev1.LocalObjectReference{
				{Name: "existing-secret"},
			},
			toAdd: []corev1.LocalObjectReference{
				{Name: "new-secret1"},
				{Name: "new-secret2"},
			},
			expected: []string{"existing-secret", "new-secret1", "new-secret2"},
		},
		{
			name: "deduplication - avoid adding duplicates",
			existing: []corev1.LocalObjectReference{
				{Name: "secret1"},
				{Name: "secret2"},
			},
			toAdd: []corev1.LocalObjectReference{
				{Name: "secret2"}, // duplicate
				{Name: "secret3"}, // new
			},
			expected: []string{"secret1", "secret2", "secret3"},
		},
		{
			name:     "add to empty list",
			existing: []corev1.LocalObjectReference{},
			toAdd:    []corev1.LocalObjectReference{},
			expected: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := proxier.mergeImagePullSecrets(tc.existing, tc.toAdd)

			actualNames := make([]string, len(result))
			for i, secret := range result {
				actualNames[i] = secret.Name
			}

			require.ElementsMatch(t, tc.expected, actualNames)
		})
	}
}

func TestPodContainerProxier_updateContainersWithSecrets(t *testing.T) {
	transformers, err := MakeTransformers([]config.ProxyRule{
		{
			Name:    "docker.io proxy with secrets",
			Matches: []string{"^docker.io"},
			Replace: "harbor.example.com/dockerhub-proxy",
			ImagePullSecrets: []config.ImagePullSecret{
				{Name: "harbor-secret"},
			},
		},
	}, nil)
	require.NoError(t, err)
	proxier := PodContainerProxier{
		Transformers: transformers,
	}

	tests := []struct {
		name                  string
		containers            []corev1.Container
		expectedUpdated       bool
		expectedSecretNames   []string
		expectedImageRewrites map[string]string // original -> expected
	}{
		{
			name: "single container with rewrite",
			containers: []corev1.Container{
				{Name: "app", Image: "docker.io/library/nginx:latest"},
			},
			expectedUpdated:     true,
			expectedSecretNames: []string{"harbor-secret"},
			expectedImageRewrites: map[string]string{
				"docker.io/library/nginx:latest": "harbor.example.com/dockerhub-proxy/library/nginx:latest",
			},
		},
		{
			name: "multiple containers with same rule",
			containers: []corev1.Container{
				{Name: "app1", Image: "docker.io/library/nginx:latest"},
				{Name: "app2", Image: "docker.io/library/alpine:latest"},
			},
			expectedUpdated:     true,
			expectedSecretNames: []string{"harbor-secret"}, // should be deduplicated
			expectedImageRewrites: map[string]string{
				"docker.io/library/nginx:latest":  "harbor.example.com/dockerhub-proxy/library/nginx:latest",
				"docker.io/library/alpine:latest": "harbor.example.com/dockerhub-proxy/library/alpine:latest",
			},
		},
		{
			name: "mixed containers - some rewrite, some don't",
			containers: []corev1.Container{
				{Name: "app1", Image: "docker.io/library/nginx:latest"},
				{Name: "app2", Image: "gcr.io/project/image:latest"},
			},
			expectedUpdated:     true,
			expectedSecretNames: []string{"harbor-secret"},
			expectedImageRewrites: map[string]string{
				"docker.io/library/nginx:latest": "harbor.example.com/dockerhub-proxy/library/nginx:latest",
				"gcr.io/project/image:latest":    "gcr.io/project/image:latest", // unchanged
			},
		},
		{
			name: "no containers match",
			containers: []corev1.Container{
				{Name: "app", Image: "gcr.io/project/image:latest"},
			},
			expectedUpdated:     false,
			expectedSecretNames: []string{},
			expectedImageRewrites: map[string]string{
				"gcr.io/project/image:latest": "gcr.io/project/image:latest",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			updatedContainers, updated, secrets, err := proxier.updateContainers(context.TODO(), tc.containers, "normal")
			require.NoError(t, err)
			require.Equal(t, tc.expectedUpdated, updated)

			// Check collected secrets
			actualSecretNames := make([]string, len(secrets))
			for i, secret := range secrets {
				actualSecretNames[i] = secret.Name
			}
			require.ElementsMatch(t, tc.expectedSecretNames, actualSecretNames)

			// Check container image rewrites
			require.Len(t, updatedContainers, len(tc.containers))
			for i, container := range updatedContainers {
				originalImage := tc.containers[i].Image
				expectedImage, exists := tc.expectedImageRewrites[originalImage]
				require.True(t, exists, "Expected image rewrite mapping for %s", originalImage)
				require.Equal(t, expectedImage, container.Image)
			}
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
