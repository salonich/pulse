package controller

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	pulseaiv1alpha1 "github.com/velorai/pulse/api/v1alpha1"
)

// OwnedResource is one declarative resource owned by an LLMBackend.
//
// New resources (PrometheusRule, HTTPRoute, RateLimitPolicy, ...) become a
// short struct that satisfies this interface — no more bespoke ensureX
// methods on the reconciler.
type OwnedResource interface {
	// Name is a human-readable label used in log/event messages.
	Name() string
	// ConditionType is the metav1.Condition.Type written by the driver.
	ConditionType() string
	// Enabled reports whether this resource should exist for this backend.
	// When false the driver skips reconcile and clears any existing condition.
	Enabled(*pulseaiv1alpha1.LLMBackend) bool
	// Build constructs the desired state. The driver sets ownerRef before write.
	Build(*pulseaiv1alpha1.LLMBackend) *unstructured.Unstructured
}

// reconcileOwned creates-or-patches one OwnedResource and writes its
// per-resource condition. Returns the condition status it set so the caller
// can roll up Ready.
func (r *LLMBackendReconciler) reconcileOwned(
	ctx context.Context,
	backend *pulseaiv1alpha1.LLMBackend,
	res OwnedResource,
) metav1.ConditionStatus {
	if !res.Enabled(backend) {
		// Resource is disabled; surface that as a positive condition so Ready
		// rollup doesn't trip on a deliberate opt-out.
		r.setCondition(backend, res.ConditionType(), metav1.ConditionTrue,
			"Disabled", res.Name()+" is disabled in spec")
		return metav1.ConditionTrue
	}

	desired := res.Build(backend)
	if err := controllerutil.SetControllerReference(backend, desired, r.Scheme); err != nil {
		r.setCondition(backend, res.ConditionType(), metav1.ConditionFalse,
			"OwnerRefFailed", fmt.Sprintf("setting owner ref: %v", err))
		return metav1.ConditionFalse
	}

	if err := r.upsertUnstructured(ctx, desired); err != nil {
		r.setCondition(backend, res.ConditionType(), metav1.ConditionFalse,
			"UpsertFailed", err.Error())
		return metav1.ConditionFalse
	}

	r.setCondition(backend, res.ConditionType(), metav1.ConditionTrue,
		"Reconciled", res.Name()+" is up to date")
	return metav1.ConditionTrue
}

// upsertUnstructured creates the resource if absent or patches its spec if present.
// Status and metadata managed by the target controller are preserved.
func (r *LLMBackendReconciler) upsertUnstructured(ctx context.Context, desired *unstructured.Unstructured) error {
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
		return fmt.Errorf("fetching %s/%s: %w", desired.GetKind(), desired.GetName(), err)
	}

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
