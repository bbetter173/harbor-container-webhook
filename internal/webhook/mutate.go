package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var (
	logger = ctrl.Log.WithName("mutator")
)

// PodContainerProxier mutates init containers and containers to redirect them to the harbor proxy cache if one exists.
type PodContainerProxier struct {
	Client       client.Client
	Decoder      admission.Decoder
	Transformers []ContainerTransformer
	Verbose      bool

	// kube config settings
	KubeClientBurst int
	KubeClientQPS   float32
}

// Handle mutates init containers and containers.
func (p *PodContainerProxier) Handle(ctx context.Context, req admission.Request) admission.Response {
	pod := &corev1.Pod{}

	err := p.Decoder.Decode(req, pod)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Filter transformers based on the pod's namespace
	namespace := req.Namespace
	applicableTransformers := p.filterTransformersByNamespace(namespace)
	if len(applicableTransformers) == 0 {
		return admission.Allowed("no applicable rules for namespace")
	}

	// Temporarily replace transformers with filtered ones for this request
	originalTransformers := p.Transformers
	p.Transformers = applicableTransformers

	initContainers, updatedInit, err := p.updateContainers(ctx, pod.Spec.InitContainers, "init")
	if err != nil {
		p.Transformers = originalTransformers // restore original transformers
		return admission.Errored(http.StatusInternalServerError, err)
	}
	containers, updated, err := p.updateContainers(ctx, pod.Spec.Containers, "normal")

	if err != nil {
		p.Transformers = originalTransformers // restore original transformers on error
		return admission.Errored(http.StatusInternalServerError, err)
	}
	pod.Spec.InitContainers = initContainers
	pod.Spec.Containers = containers

	if !updated && !updatedInit {
		p.Transformers = originalTransformers // restore original transformers when no updates
		return admission.Allowed("no updates")
	}

	// imagePullSecrets - this now uses the namespace-filtered transformers
	imagePullSecrets, err := p.updateImagePullSecrets(p.getPodName(pod), pod.Spec.ImagePullSecrets)
	if err != nil {
		p.Transformers = originalTransformers // restore original transformers on error
		return admission.Errored(http.StatusInternalServerError, err)
	}
	pod.Spec.ImagePullSecrets = imagePullSecrets

	// Restore original transformers after all operations are complete
	p.Transformers = originalTransformers

	marshaledPod, err := json.Marshal(pod)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledPod)
}

func (p *PodContainerProxier) lookupNodeArchAndOS(ctx context.Context, restClient client.Client, nodeName string) (platform, os string, err error) {
	node := corev1.Node{}
	if err = restClient.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		return "", "", fmt.Errorf("failed to lookup node %s: %w", nodeName, err)
	}
	logger.Info(fmt.Sprintf("node %v", node))
	return node.Status.NodeInfo.Architecture, node.Status.NodeInfo.OperatingSystem, nil
}

func (p *PodContainerProxier) updateContainers(ctx context.Context, containers []corev1.Container, _ string) ([]corev1.Container, bool, error) {
	containersReplacement := make([]corev1.Container, 0, len(containers))
	updated := false
	for i := range containers {
		container := containers[i]
		imageRef, err := p.rewriteImage(ctx, container.Image)
		if err != nil {
			return []corev1.Container{}, false, err
		}
		if !updated {
			updated = imageRef != container.Image
		}
		if imageRef != container.Image {
			logger.Info(fmt.Sprintf("rewriting the image of %q from %q to %q", container.Name, container.Image, imageRef))
		}
		container.Image = imageRef
		containersReplacement = append(containersReplacement, container)
	}
	return containersReplacement, updated, nil
}

func (p *PodContainerProxier) rewriteImage(ctx context.Context, imageRef string) (string, error) {
	for _, transformer := range p.Transformers {
		updatedRef, err := transformer.RewriteImage(imageRef)
		if err != nil {
			return "", fmt.Errorf("transformer %q failed to update imageRef %q: %w", transformer.Name(), imageRef, err)
		}
		if updatedRef != imageRef {
			if found, err := transformer.CheckUpstream(ctx, updatedRef); err != nil {
				logger.Info(fmt.Sprintf("transformer %q skipping rewriting %q to %q, could not fetch image manifest: %s", transformer.Name(), imageRef, updatedRef, err.Error()))
				continue
			} else if !found {
				logger.Info(fmt.Sprintf("transformer %q skipping rewriting %q to %q, registry reported image not found.", transformer.Name(), imageRef, updatedRef))
				continue
			}
			logger.Info(fmt.Sprintf("transformer %q rewriting %q to %q", transformer.Name(), imageRef, updatedRef))
			return updatedRef, nil
		}
	}
	return imageRef, nil
}

// PodContainerProxier implements admission.DecoderInjector.
// A decoder will be automatically injected.

// filterTransformersByNamespace returns only the transformers that should apply to the given namespace
func (p *PodContainerProxier) filterTransformersByNamespace(namespace string) []ContainerTransformer {
	applicableTransformers := make([]ContainerTransformer, 0)

	for _, transformer := range p.Transformers {
		// Type assert to access the ShouldApplyToNamespace method
		if ruleTransformer, ok := transformer.(*ruleTransformer); ok {
			if ruleTransformer.ShouldApplyToNamespace(namespace) {
				applicableTransformers = append(applicableTransformers, transformer)
			}
		} else {
			// For any other transformer types, include them (backward compatibility)
			applicableTransformers = append(applicableTransformers, transformer)
		}
	}

	return applicableTransformers
}

// InjectDecoder injects the decoder.
func (p *PodContainerProxier) InjectDecoder(d admission.Decoder) error {
	p.Decoder = d
	return nil
}

func (p *PodContainerProxier) updateImagePullSecrets(podName string, imagePullSecrets []corev1.LocalObjectReference) (newImagePullSecrets []corev1.LocalObjectReference, err error) {
	currentSecrets := imagePullSecrets
	anyUpdated := false

	for _, transformer := range p.Transformers {
		updated, updatedSecrets, err := transformer.RewriteImagePullSecrets(currentSecrets)
		if err != nil {
			return imagePullSecrets, err
		}
		if updated {
			logger.Info(fmt.Sprintf("rewriting the imagePullSecrets of the pod %s from %q to %q", podName, currentSecrets, updatedSecrets))
			currentSecrets = updatedSecrets
			anyUpdated = true
		}
	}

	if !anyUpdated {
		return imagePullSecrets, nil
	}
	return currentSecrets, nil
}

func (p *PodContainerProxier) getPodName(pod *corev1.Pod) (podName string) {
	if pod.Name != "" {
		return pod.Name
	}
	if pod.ObjectMeta.Labels["app.kubernetes.io/name"] != "" {
		return pod.ObjectMeta.Labels["app.kubernetes.io/name"]
	}
	if pod.ObjectMeta.Labels["app"] != "" {
		return pod.ObjectMeta.Labels["app"]
	}
	return pod.GenerateName
}
