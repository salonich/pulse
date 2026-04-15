// Package controller implements the LLMBackend reconciliation loop.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	pulseaiv1alpha1 "github.com/velorai/pulse/api/v1alpha1"
	"github.com/velorai/pulse/internal/observability"
	"github.com/velorai/pulse/internal/pricing"
)

const (
	injectLabel      = "pulse.velorai.com/inject"
	injectLabelValue = "enabled"
	pricingCMName    = "pulse-pricing"
	pricingCMNS      = "pulse-system"
	requeuePeriod    = 5 * time.Minute
)

// LLMBackendReconciler reconciles LLMBackend objects.
//
// +kubebuilder:rbac:groups=pulse.velorai.com,resources=llmbackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=pulse.velorai.com,resources=llmbackends/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=pulse.velorai.com,resources=llmbackends/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=grafana.integreatly.org,resources=grafanadashboards,verbs=get;list;watch;create;update;patch;delete
type LLMBackendReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Pricing *pricing.Table
}

// SetupWithManager registers the controller with the manager.
func (r *LLMBackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&pulseaiv1alpha1.LLMBackend{}).
		Complete(r)
}

// Reconcile is called whenever an LLMBackend is created, updated, or deleted.
// It is idempotent: running it N times produces the same cluster state.
func (r *LLMBackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("llmbackend", req.NamespacedName)

	var backend pulseaiv1alpha1.LLMBackend
	if err := r.Get(ctx, req.NamespacedName, &backend); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil // ownerRefs handle cleanup
		}
		return ctrl.Result{}, fmt.Errorf("fetching LLMBackend: %w", err)
	}

	// Work on a copy of status so we can patch at the end.
	orig := backend.DeepCopy()

	var reconcileErr error

	// Step 1: label the target namespace so the webhook injects sidecars.
	if err := r.ensureNamespaceLabel(ctx, backend.Spec.TargetService.Namespace); err != nil {
		logger.Error(err, "failed to label namespace")
		r.setCondition(&backend, pulseaiv1alpha1.ConditionSidecarInjected, metav1.ConditionFalse,
			"NamespaceLabelFailed", err.Error())
		reconcileErr = err
	} else {
		r.setCondition(&backend, pulseaiv1alpha1.ConditionSidecarInjected, metav1.ConditionTrue,
			"NamespaceLabelled", "Namespace labelled for sidecar injection")
	}

	// Step 2: ensure the cluster-wide pricing ConfigMap exists.
	if err := r.ensurePricingConfigMap(ctx, &backend); err != nil {
		logger.Error(err, "failed to ensure pricing ConfigMap")
		reconcileErr = err
	}

	// Step 3: create/update ServiceMonitor.
	if backend.Spec.Observability.Prometheus {
		if err := r.ensureServiceMonitor(ctx, &backend); err != nil {
			logger.Error(err, "failed to ensure ServiceMonitor")
			r.setCondition(&backend, pulseaiv1alpha1.ConditionPrometheusConfigured,
				metav1.ConditionFalse, "ServiceMonitorFailed", err.Error())
			reconcileErr = err
		} else {
			r.setCondition(&backend, pulseaiv1alpha1.ConditionPrometheusConfigured,
				metav1.ConditionTrue, "ServiceMonitorReady", "ServiceMonitor created/updated")
		}
	}

	// Step 4: create/update GrafanaDashboard.
	if backend.Spec.Observability.Grafana {
		if err := r.ensureGrafanaDashboard(ctx, &backend); err != nil {
			logger.Error(err, "failed to ensure GrafanaDashboard")
			r.setCondition(&backend, pulseaiv1alpha1.ConditionGrafanaDashboardReady,
				metav1.ConditionFalse, "GrafanaDashboardFailed", err.Error())
			reconcileErr = err
		} else {
			r.setCondition(&backend, pulseaiv1alpha1.ConditionGrafanaDashboardReady,
				metav1.ConditionTrue, "GrafanaDashboardReady", "GrafanaDashboard created/updated")
		}
	}

	// Step 5: set top-level Ready condition.
	if reconcileErr == nil {
		r.setCondition(&backend, pulseaiv1alpha1.ConditionReady, metav1.ConditionTrue,
			"ReconcileSucceeded", "All resources reconciled successfully")
	} else {
		r.setCondition(&backend, pulseaiv1alpha1.ConditionReady, metav1.ConditionFalse,
			"ReconcileFailed", reconcileErr.Error())
	}

	backend.Status.ObservedGeneration = backend.Generation

	// Patch status.
	if err := r.Status().Patch(ctx, &backend, client.MergeFrom(orig)); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("patching status: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: requeuePeriod}, reconcileErr
}

// ensureNamespaceLabel adds pulse.velorai.com/inject=enabled to the target namespace.
func (r *LLMBackendReconciler) ensureNamespaceLabel(ctx context.Context, ns string) error {
	var namespace corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: ns}, &namespace); err != nil {
		return fmt.Errorf("fetching namespace %s: %w", ns, err)
	}
	if namespace.Labels[injectLabel] == injectLabelValue {
		return nil // already set
	}
	patch := client.MergeFrom(namespace.DeepCopy())
	if namespace.Labels == nil {
		namespace.Labels = make(map[string]string)
	}
	namespace.Labels[injectLabel] = injectLabelValue
	return r.Patch(ctx, &namespace, patch)
}

// ensurePricingConfigMap creates or updates the pulse-pricing ConfigMap in pulse-system.
func (r *LLMBackendReconciler) ensurePricingConfigMap(ctx context.Context, backend *pulseaiv1alpha1.LLMBackend) error {
	pricingJSON, err := r.Pricing.JSON()
	if err != nil {
		return fmt.Errorf("serialising pricing data: %w", err)
	}

	cm := &corev1.ConfigMap{
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
		return r.Create(ctx, cm)
	}
	if err != nil {
		return fmt.Errorf("fetching pricing ConfigMap: %w", err)
	}

	patch := client.MergeFrom(existing.DeepCopy())
	existing.Data = cm.Data
	return r.Patch(ctx, existing, patch)
}

// ensureServiceMonitor creates or patches the ServiceMonitor owned by this LLMBackend.
func (r *LLMBackendReconciler) ensureServiceMonitor(ctx context.Context, backend *pulseaiv1alpha1.LLMBackend) error {
	desired := observability.ServiceMonitorForBackend(backend.Namespace, backend.Name)
	return r.ensureUnstructured(ctx, backend, desired)
}

// ensureGrafanaDashboard creates or patches the GrafanaDashboard owned by this LLMBackend.
func (r *LLMBackendReconciler) ensureGrafanaDashboard(ctx context.Context, backend *pulseaiv1alpha1.LLMBackend) error {
	desired := observability.GrafanaDashboardForBackend(backend.Namespace, backend.Name)
	return r.ensureUnstructured(ctx, backend, desired)
}

// ensureUnstructured creates or updates an unstructured resource and sets ownerReference.
func (r *LLMBackendReconciler) ensureUnstructured(ctx context.Context, backend *pulseaiv1alpha1.LLMBackend, desired *unstructured.Unstructured) error {
	// Set ownerReference so the resource is garbage-collected when the LLMBackend is deleted.
	if err := controllerutil.SetControllerReference(backend, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on %s/%s: %w",
			desired.GetKind(), desired.GetName(), err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())

	err := r.Get(ctx, types.NamespacedName{
		Namespace: desired.GetNamespace(),
		Name:      desired.GetName(),
	}, existing)

	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("fetching %s: %w", desired.GetKind(), err)
	}

	// Patch spec only — preserve status and metadata managed by the target controller.
	desiredSpec, _, _ := unstructured.NestedMap(desired.Object, "spec")
	if desiredSpec == nil {
		return nil
	}
	specJSON, err := json.Marshal(map[string]interface{}{"spec": desiredSpec})
	if err != nil {
		return fmt.Errorf("marshalling spec patch: %w", err)
	}
	return r.Patch(ctx, existing, client.RawPatch(types.MergePatchType, specJSON))
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
				return // no change
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
