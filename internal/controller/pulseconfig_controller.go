package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	pulseaiv1alpha1 "github.com/velorai/pulse/api/v1alpha1"
)

const (
	ebpfAgentName = "pulse-ebpf-agent"

	conditionEBPFDaemonSet = "EBPFDaemonSetReady"
)

// PulseConfigReconciler watches the cluster-wide PulseConfig and reconciles
// capture-mode-dependent resources (the eBPF DaemonSet today).
//
// It also caches the active capture mode so the webhook and the LLMBackend
// reconciler can read it without making an API call per request — see Mode().
//
// +kubebuilder:rbac:groups=pulse.velorai.com,resources=pulseconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=pulse.velorai.com,resources=pulseconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
type PulseConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// SystemNamespace is where the eBPF DaemonSet is deployed (typically pulse-system).
	SystemNamespace string

	mu             sync.RWMutex
	cachedMode     string
	cachedAgentImg string
}

// Mode returns the cached capture mode. Defaults to sidecar when no PulseConfig
// has been seen yet — preserving the existing behaviour of unconfigured clusters.
func (r *PulseConfigReconciler) Mode() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.cachedMode == "" {
		return pulseaiv1alpha1.CaptureMethodSidecar
	}
	return r.cachedMode
}

// EBPFAgentImage returns the cached image used for the eBPF DaemonSet.
func (r *PulseConfigReconciler) EBPFAgentImage() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cachedAgentImg
}

// SetupWithManager registers the controller with the manager.
func (r *PulseConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&pulseaiv1alpha1.PulseConfig{}).
		Owns(&appsv1.DaemonSet{}).
		Complete(r)
}

func (r *PulseConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("pulseconfig", req.NamespacedName)

	var cfg pulseaiv1alpha1.PulseConfig
	if err := r.Get(ctx, req.NamespacedName, &cfg); err != nil {
		if apierrors.IsNotFound(err) {
			// PulseConfig deleted: revert cache to default. Owner-ref GC handles the DaemonSet.
			r.mu.Lock()
			r.cachedMode = ""
			r.cachedAgentImg = ""
			r.mu.Unlock()
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("fetching PulseConfig: %w", err)
	}
	orig := cfg.DeepCopy()

	mode := cfg.Spec.CaptureMethod
	if mode == "" {
		mode = pulseaiv1alpha1.CaptureMethodSidecar
	}
	agentImg := cfg.Spec.EBPFAgentImage
	if agentImg == "" {
		agentImg = "ghcr.io/velorai/pulse-ebpf-agent:latest"
	}

	// Update the cache before any error path returns — so Mode() reflects spec
	// even if a downstream resource reconcile is still failing.
	r.mu.Lock()
	r.cachedMode = mode
	r.cachedAgentImg = agentImg
	r.mu.Unlock()

	// Reconcile the eBPF DaemonSet. Either ensure or delete depending on mode.
	condStatus := metav1.ConditionTrue
	condReason := "Reconciled"
	condMsg := ""

	switch mode {
	case pulseaiv1alpha1.CaptureMethodEBPF:
		if err := r.ensureEBPFDaemonSet(ctx, &cfg, agentImg); err != nil {
			logger.Error(err, "failed to ensure eBPF DaemonSet")
			condStatus = metav1.ConditionFalse
			condReason = "EnsureFailed"
			condMsg = err.Error()
		} else {
			condMsg = "eBPF DaemonSet present"
		}
	case pulseaiv1alpha1.CaptureMethodSidecar:
		if err := r.deleteEBPFDaemonSet(ctx); err != nil {
			logger.Error(err, "failed to delete eBPF DaemonSet")
			condStatus = metav1.ConditionFalse
			condReason = "DeleteFailed"
			condMsg = err.Error()
		} else {
			condReason = "Disabled"
			condMsg = "capture mode is sidecar; no eBPF DaemonSet expected"
		}
	default:
		condStatus = metav1.ConditionFalse
		condReason = "UnknownMode"
		condMsg = fmt.Sprintf("unrecognised captureMethod %q", mode)
	}

	setPulseConfigCondition(&cfg, conditionEBPFDaemonSet, condStatus, condReason, condMsg)
	cfg.Status.ObservedGeneration = cfg.Generation

	if err := r.Status().Patch(ctx, &cfg, client.MergeFrom(orig)); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("patching status: %w", err)
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// ensureEBPFDaemonSet upserts the cluster-wide eBPF capture DaemonSet.
// Owner-ref is the PulseConfig so deleting the PulseConfig also removes the DS.
func (r *PulseConfigReconciler) ensureEBPFDaemonSet(ctx context.Context, cfg *pulseaiv1alpha1.PulseConfig, image string) error {
	if err := r.ensureEBPFServiceAccount(ctx, cfg); err != nil {
		return err
	}

	desired := buildEBPFDaemonSet(r.SystemNamespace, image, cfg.Spec.CollectorEndpoint)
	if err := controllerutil.SetControllerReference(cfg, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner ref on DaemonSet: %w", err)
	}

	existing := &appsv1.DaemonSet{}
	err := r.Get(ctx, types.NamespacedName{Name: ebpfAgentName, Namespace: r.SystemNamespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("fetching DaemonSet: %w", err)
	}

	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec = desired.Spec
	existing.Labels = desired.Labels
	return r.Patch(ctx, existing, patch)
}

func (r *PulseConfigReconciler) ensureEBPFServiceAccount(ctx context.Context, cfg *pulseaiv1alpha1.PulseConfig) error {
	desired := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ebpfAgentName,
			Namespace: r.SystemNamespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "pulse-operator"},
		},
	}
	if err := controllerutil.SetControllerReference(cfg, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner ref on SA: %w", err)
	}
	existing := &corev1.ServiceAccount{}
	err := r.Get(ctx, types.NamespacedName{Name: ebpfAgentName, Namespace: r.SystemNamespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	return err
}

// deleteEBPFDaemonSet removes the DaemonSet if present. Idempotent.
func (r *PulseConfigReconciler) deleteEBPFDaemonSet(ctx context.Context) error {
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: ebpfAgentName, Namespace: r.SystemNamespace},
	}
	if err := r.Delete(ctx, ds); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting DaemonSet: %w", err)
	}
	return nil
}

func setPulseConfigCondition(
	cfg *pulseaiv1alpha1.PulseConfig,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	now := metav1.Now()
	for i, c := range cfg.Status.Conditions {
		if c.Type == condType {
			if c.Status == status && c.Reason == reason {
				return
			}
			cfg.Status.Conditions[i] = metav1.Condition{
				Type:               condType,
				Status:             status,
				Reason:             reason,
				Message:            message,
				LastTransitionTime: now,
				ObservedGeneration: cfg.Generation,
			}
			return
		}
	}
	cfg.Status.Conditions = append(cfg.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
		ObservedGeneration: cfg.Generation,
	})
}

// buildEBPFDaemonSet returns the desired DaemonSet spec. The pod requires
// hostPID + hostNetwork + privileged to load BPF programs and observe
// per-pod traffic.
func buildEBPFDaemonSet(namespace, image, collectorEndpoint string) *appsv1.DaemonSet {
	if collectorEndpoint == "" {
		collectorEndpoint = "http://pulse-collector.pulse-system:9091"
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       ebpfAgentName,
		"app.kubernetes.io/component":  "ebpf-agent",
		"app.kubernetes.io/managed-by": "pulse-operator",
	}

	hostPathDirCreate := corev1.HostPathDirectoryOrCreate
	hostPathDir := corev1.HostPathDirectory

	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ebpfAgentName,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					HostPID:            true,
					HostNetwork:        true,
					ServiceAccountName: ebpfAgentName,
					Tolerations: []corev1.Toleration{
						{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
						{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute},
					},
					Containers: []corev1.Container{{
						Name:  "ebpf-agent",
						Image: image,
						Env: []corev1.EnvVar{
							{Name: "PULSE_COLLECTOR_URL", Value: collectorEndpoint},
							{Name: "PULSE_TARGET_PORTS", Value: "443,8080"},
							{Name: "PULSE_NAMESPACE", ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
							}},
							{Name: "PULSE_LLMBACKEND_NAME", Value: "ebpf-node-capture"},
						},
						SecurityContext: &corev1.SecurityContext{
							Privileged: ptrBool(true),
							Capabilities: &corev1.Capabilities{
								Add: []corev1.Capability{"BPF", "SYS_ADMIN", "SYS_RESOURCE", "NET_ADMIN"},
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "bpf-fs", MountPath: "/sys/fs/bpf"},
							{Name: "btf", MountPath: "/sys/kernel/btf", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "bpf-fs", VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/sys/fs/bpf", Type: &hostPathDirCreate},
						}},
						{Name: "btf", VolumeSource: corev1.VolumeSource{
							HostPath: &corev1.HostPathVolumeSource{Path: "/sys/kernel/btf", Type: &hostPathDir},
						}},
					},
				},
			},
		},
	}
}

func ptrBool(b bool) *bool { return &b }
