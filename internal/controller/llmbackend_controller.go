// Package controller implements the LLMBackend reconciliation loop.
package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	pulseaiv1alpha1 "github.com/velorai/pulse/api/v1alpha1"
	"github.com/velorai/pulse/internal/pricing"
)

const (
	injectLabel        = "pulse.velorai.com/inject"
	injectLabelValue   = "enabled"
	pricingCMName      = "pulse-pricing"
	pricingCMNS        = "pulse-system"
	proxyContainerName = "pulse-proxy"
	requeuePeriod      = 5 * time.Minute
)

// PulseConfigReader exposes the cluster-wide capture mode to the LLMBackend
// reconciler. It is satisfied by internal/controller.PulseConfigReconciler;
// the interface lets tests inject a fake without depending on the watch.
type PulseConfigReader interface {
	Mode() string
}

// LLMBackendReconciler reconciles LLMBackend objects.
//
// +kubebuilder:rbac:groups=pulse.velorai.com,resources=llmbackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=pulse.velorai.com,resources=llmbackends/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=pulse.velorai.com,resources=llmbackends/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanadashboards,verbs=get;list;watch;create;update;patch;delete
type LLMBackendReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Pricing     *pricing.Table
	PulseConfig PulseConfigReader
}

// SetupWithManager registers the controller with the manager.
func (r *LLMBackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&pulseaiv1alpha1.LLMBackend{}).
		Complete(r)
}

// Reconcile is called whenever an LLMBackend is created, updated, or deleted.
// Idempotent: running it N times produces the same cluster state.
func (r *LLMBackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("llmbackend", req.NamespacedName)

	var backend pulseaiv1alpha1.LLMBackend
	if err := r.Get(ctx, req.NamespacedName, &backend); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil // ownerRefs handle cleanup
		}
		return ctrl.Result{}, fmt.Errorf("fetching LLMBackend: %w", err)
	}
	orig := backend.DeepCopy()

	mode := pulseaiv1alpha1.CaptureMethodSidecar
	if r.PulseConfig != nil {
		if m := r.PulseConfig.Mode(); m != "" {
			mode = m
		}
	}

	// Step 1: prerequisite resources that don't fit the OwnedResource shape.
	if err := r.ensureNamespaceLabel(ctx, backend.Spec.TargetService.Namespace); err != nil {
		logger.Error(err, "failed to label namespace")
		// Surface as a sidecars condition since this gates injection.
		r.setCondition(&backend, pulseaiv1alpha1.ConditionSidecarsInjected, metav1.ConditionFalse,
			"NamespaceLabelFailed", err.Error())
	}
	pricingCondStatus := metav1.ConditionTrue
	if err := r.ensurePricingConfigMap(ctx); err != nil {
		logger.Error(err, "failed to ensure pricing ConfigMap")
		r.setCondition(&backend, pulseaiv1alpha1.ConditionPricingConfigMapReady,
			metav1.ConditionFalse, "EnsureFailed", err.Error())
		pricingCondStatus = metav1.ConditionFalse
	} else {
		r.setCondition(&backend, pulseaiv1alpha1.ConditionPricingConfigMapReady,
			metav1.ConditionTrue, "Reconciled", "pulse-pricing ConfigMap up to date")
	}

	// Step 2: drive every OwnedResource through the same path.
	allOK := pricingCondStatus == metav1.ConditionTrue
	for _, res := range ownedResources {
		if r.reconcileOwned(ctx, &backend, res) != metav1.ConditionTrue {
			allOK = false
		}
	}

	// Step 3: ground-truth pod-level status. Skip in eBPF mode — sidecars
	// are not expected to exist when capture happens at the kernel.
	if mode == pulseaiv1alpha1.CaptureMethodEBPF {
		r.setCondition(&backend, pulseaiv1alpha1.ConditionSidecarsInjected,
			metav1.ConditionTrue, "EBPFMode", "capture is via eBPF DaemonSet")
		backend.Status.SidecarInjectedPods = 0
	} else {
		injected, total, err := r.countInjectedPods(ctx, &backend)
		if err != nil {
			logger.Error(err, "failed to count injected pods")
			r.setCondition(&backend, pulseaiv1alpha1.ConditionSidecarsInjected,
				metav1.ConditionFalse, "PodListFailed", err.Error())
			allOK = false
		} else {
			backend.Status.SidecarInjectedPods = injected
			switch {
			case total == 0:
				// No target pods exist yet — this is normal during rollout.
				r.setCondition(&backend, pulseaiv1alpha1.ConditionSidecarsInjected,
					metav1.ConditionTrue, "NoTargetPods", "target service has no pods yet")
			case injected == 0:
				r.setCondition(&backend, pulseaiv1alpha1.ConditionSidecarsInjected,
					metav1.ConditionFalse, "NoneInjected",
					fmt.Sprintf("0 of %d target pods have pulse-proxy", total))
				allOK = false
			default:
				r.setCondition(&backend, pulseaiv1alpha1.ConditionSidecarsInjected,
					metav1.ConditionTrue, "Injected",
					fmt.Sprintf("%d of %d target pods have pulse-proxy", injected, total))
			}
		}
	}

	// Step 4: roll up Ready as the AND of every per-resource condition.
	if allOK {
		r.setCondition(&backend, pulseaiv1alpha1.ConditionReady, metav1.ConditionTrue,
			"AllResourcesReady", "every owned resource is reconciled")
	} else {
		r.setCondition(&backend, pulseaiv1alpha1.ConditionReady, metav1.ConditionFalse,
			"DegradedResources", "one or more owned resources are not ready")
	}

	backend.Status.ObservedGeneration = backend.Generation

	if err := r.Status().Patch(ctx, &backend, client.MergeFrom(orig)); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("patching status: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: requeuePeriod}, nil
}

// ensureNamespaceLabel adds pulse.velorai.com/inject=enabled to the target namespace.
func (r *LLMBackendReconciler) ensureNamespaceLabel(ctx context.Context, ns string) error {
	var namespace corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: ns}, &namespace); err != nil {
		return fmt.Errorf("fetching namespace %s: %w", ns, err)
	}
	if namespace.Labels[injectLabel] == injectLabelValue {
		return nil
	}
	patch := client.MergeFrom(namespace.DeepCopy())
	if namespace.Labels == nil {
		namespace.Labels = make(map[string]string)
	}
	namespace.Labels[injectLabel] = injectLabelValue
	return r.Patch(ctx, &namespace, patch)
}

// ensurePricingConfigMap creates or updates the pulse-pricing ConfigMap in pulse-system.
// The ConfigMap is cluster-wide (single instance), so it doesn't carry an LLMBackend
// owner ref — its lifecycle is independent of any single backend.
func (r *LLMBackendReconciler) ensurePricingConfigMap(ctx context.Context) error {
	pricingJSON, err := r.Pricing.JSON()
	if err != nil {
		return fmt.Errorf("serialising pricing data: %w", err)
	}

	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pricingCMName,
			Namespace: pricingCMNS,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "pulse-operator"},
		},
		Data: map[string]string{"pricing.json": string(pricingJSON)},
	}

	existing := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: pricingCMName, Namespace: pricingCMNS}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("fetching pricing ConfigMap: %w", err)
	}

	patch := client.MergeFrom(existing.DeepCopy())
	existing.Data = desired.Data
	return r.Patch(ctx, existing, patch)
}

// countInjectedPods returns (injectedCount, totalTargetPodCount). A target
// pod is one that backs the LLMBackend's TargetService — selected by the
// Service's spec.selector applied to pod labels in the target namespace.
//
// "Injected" means the pod has a container named pulse-proxy.
func (r *LLMBackendReconciler) countInjectedPods(ctx context.Context, backend *pulseaiv1alpha1.LLMBackend) (int32, int32, error) {
	var svc corev1.Service
	err := r.Get(ctx, types.NamespacedName{
		Name:      backend.Spec.TargetService.Name,
		Namespace: backend.Spec.TargetService.Namespace,
	}, &svc)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return 0, 0, nil // service not yet created — treat as zero target pods
		}
		return 0, 0, fmt.Errorf("fetching target service: %w", err)
	}
	if len(svc.Spec.Selector) == 0 {
		return 0, 0, nil
	}

	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(backend.Spec.TargetService.Namespace),
		client.MatchingLabels(svc.Spec.Selector),
	); err != nil {
		return 0, 0, fmt.Errorf("listing target pods: %w", err)
	}

	var total, injected int32
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		total++
		for _, c := range p.Spec.Containers {
			if c.Name == proxyContainerName {
				injected++
				break
			}
		}
	}
	return injected, total, nil
}

// setCondition upserts a named condition on the LLMBackend status.
func (r *LLMBackendReconciler) setCondition(
	backend *pulseaiv1alpha1.LLMBackend,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	now := metav1.Now()
	for i, c := range backend.Status.Conditions {
		if c.Type == condType {
			if c.Status == status && c.Reason == reason {
				return
			}
			backend.Status.Conditions[i] = metav1.Condition{
				Type:               condType,
				Status:             status,
				Reason:             reason,
				Message:            message,
				LastTransitionTime: now,
				ObservedGeneration: backend.Generation,
			}
			return
		}
	}
	backend.Status.Conditions = append(backend.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: backend.Generation,
	})
}
