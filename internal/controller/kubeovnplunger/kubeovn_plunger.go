package kubeovnplunger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cozystack/cozystack/internal/sse"
	"github.com/cozystack/cozystack/pkg/ovnstatus"
	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var (
	srv *sse.Server
)

const (
	rescanInterval = 1 * time.Minute
)

// KubeOVNPlunger watches the ovn-central cluster members
type KubeOVNPlunger struct {
	client.Client
	Scheme     *runtime.Scheme
	ClientSet  kubernetes.Interface
	REST       *rest.Config
	Registry   prometheus.Registerer
	metrics    metrics
	lastLeader map[string]string
	seenCIDs   map[string]map[string]struct{}
}

// Reconcile runs the checks on the ovn-central members to see if their views of the cluster are consistent
func (r *KubeOVNPlunger) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	deploy := &appsv1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, deploy); err != nil {
		return ctrl.Result{}, err
	}

	iphints := map[string]string{}
	for _, env := range deploy.Spec.Template.Spec.Containers[0].Env {
		if env.Name != "NODE_IPS" {
			continue
		}
		for _, ip := range strings.Split(env.Value, ",") {
			iphints[ip] = ""
		}
		break
	}
	if len(iphints) == 0 {
		l.Info("WARNING: running without IP hints, some error conditions cannot be detected")
	}
	pods := &corev1.PodList{}

	if err := r.List(ctx, pods, client.InNamespace(req.Namespace), client.MatchingLabels(map[string]string{"app": req.Name})); err != nil {
		return ctrl.Result{}, fmt.Errorf("list ovn-central pods: %w", err)
	}

	nbmv := make([]ovnstatus.MemberView, 0, len(pods.Items))
	sbmv := make([]ovnstatus.MemberView, 0, len(pods.Items))
	nbSnaps := make([]ovnstatus.HealthSnapshot, 0, len(pods.Items))
	sbSnaps := make([]ovnstatus.HealthSnapshot, 0, len(pods.Items))
	// TODO: get real iphints
	for i := range pods.Items {
		o := ovnstatus.OVNClient{}
		o.ApplyDefaults()
		o.Runner = func(ctx context.Context, bin string, args ...string) (string, error) {
			cmd := append([]string{bin}, args...)
			eo := ExecOptions{
				Namespace: req.Namespace,
				Pod:       pods.Items[i].Name,
				Container: pods.Items[i].Spec.Containers[0].Name,
				Command:   cmd,
			}
			res, err := r.ExecPod(ctx, eo)
			if err != nil {
				return "", err
			}
			return res.Stdout, nil
		}
		nb, sb, err1, err2 := o.HealthBoth(ctx)
		if err1 != nil || err2 != nil {
			l.Error(fmt.Errorf("health check failed: nb=%w, sb=%w", err1, err2), "pod", pods.Items[i].Name)
			continue
		}
		nbSnaps = append(nbSnaps, nb)
		sbSnaps = append(sbSnaps, sb)
		nbmv = append(nbmv, ovnstatus.BuildMemberView(nb))
		sbmv = append(sbmv, ovnstatus.BuildMemberView(sb))
	}
	r.recordAndPruneCIDs("nb", cidFromSnaps(nbSnaps))
	r.recordAndPruneCIDs("sb", cidFromSnaps(sbSnaps))
	nbmv = ovnstatus.NormalizeViews(nbmv)
	sbmv = ovnstatus.NormalizeViews(sbmv)
	nbecv := ovnstatus.AnalyzeConsensusWithIPHints(nbmv, &ovnstatus.Hints{ExpectedIPs: iphints})
	sbecv := ovnstatus.AnalyzeConsensusWithIPHints(sbmv, &ovnstatus.Hints{ExpectedIPs: iphints})
	expected := len(iphints)
	r.WriteClusterMetrics("nb", nbSnaps, nbecv, expected)
	r.WriteClusterMetrics("sb", sbSnaps, sbecv, expected)
	r.WriteMemberMetrics("nb", nbSnaps, nbmv, nbecv)
	r.WriteMemberMetrics("sb", sbSnaps, sbmv, sbecv)
	srv.Publish(nbecv.PrettyString() + sbecv.PrettyString())
	return ctrl.Result{}, nil
}

// SetupWithManager attaches a generic ticker to trigger a reconcile every <interval> seconds
func (r *KubeOVNPlunger) SetupWithManager(mgr ctrl.Manager, kubeOVNNamespace, appName string) error {
	r.REST = rest.CopyConfig(mgr.GetConfig())
	cs, err := kubernetes.NewForConfig(r.REST)
	if err != nil {
		return fmt.Errorf("build clientset: %w", err)
	}
	r.ClientSet = cs
	ch := make(chan event.GenericEvent, 10)
	mapFunc := func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{Namespace: kubeOVNNamespace, Name: appName},
		}}
	}
	mapper := handler.EnqueueRequestsFromMapFunc(mapFunc)
	srv = sse.New(sse.Options{
		Addr:      ":18080",
		AllowCORS: true,
	})
	r.initMetrics()
	r.lastLeader = make(map[string]string)
	r.seenCIDs = map[string]map[string]struct{}{"nb": {}, "sb": {}}
	if err := ctrl.NewControllerManagedBy(mgr).
		Named("kubeovnplunger").
		WatchesRawSource(source.Channel(ch, mapper)).
		Complete(r); err != nil {
		return err
	}
	_ = mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		go srv.ListenAndServe()
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
		return nil
	}))
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		ticker := time.NewTicker(rescanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				ch <- event.GenericEvent{
					Object: &metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: kubeOVNNamespace,
							Name:      appName,
						},
					},
				}
			}
		}
	}))
}

type ExecOptions struct {
	Namespace string
	Pod       string
	Container string
	Command   []string      // e.g. []string{"sh", "-c", "echo hi"}
	Stdin     io.Reader     // optional
	TTY       bool          // if true, stderr is merged into stdout
	Timeout   time.Duration // optional overall timeout
}

type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode *int // nil if not determinable
}

// ExecPod runs a command in a pod and returns stdout/stderr/exit code.
func (r *KubeOVNPlunger) ExecPod(ctx context.Context, opts ExecOptions) (*ExecResult, error) {
	if opts.Namespace == "" || opts.Pod == "" || opts.Container == "" {
		return nil, fmt.Errorf("namespace, pod, and container are required")
	}

	req := r.ClientSet.CoreV1().RESTClient().
		Post().
		Resource("pods").
		Namespace(opts.Namespace).
		Name(opts.Pod).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: opts.Container,
			Command:   opts.Command,
			Stdin:     opts.Stdin != nil,
			Stdout:    true,
			Stderr:    !opts.TTY,
			TTY:       opts.TTY,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(r.REST, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("spdy executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	streamCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		streamCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	streamErr := exec.StreamWithContext(streamCtx, remotecommand.StreamOptions{
		Stdin:  opts.Stdin,
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    opts.TTY,
	})

	res := &ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if streamErr != nil {
		// Try to surface exit code instead of treating all failures as transport errors
		type exitCoder interface{ ExitStatus() int }
		if ec, ok := streamErr.(exitCoder); ok {
			code := ec.ExitStatus()
			res.ExitCode = &code
			return res, nil
		}
		return res, fmt.Errorf("exec stream: %w", streamErr)
	}
	zero := 0
	res.ExitCode = &zero
	return res, nil
}

func (r *KubeOVNPlunger) recordAndPruneCIDs(db, currentCID string) {

	// Mark current as seen
	if r.seenCIDs[db] == nil {
		r.seenCIDs[db] = map[string]struct{}{}
	}
	if currentCID != "" {
		r.seenCIDs[db][currentCID] = struct{}{}
	}

	// Build a set of "still active" CIDs this cycle (could be none if you failed to collect)
	active := map[string]struct{}{}
	if currentCID != "" {
		active[currentCID] = struct{}{}
	}

	// Any seen CID that isn't active now is stale -> delete all its series
	for cid := range r.seenCIDs[db] {
		if _, ok := active[cid]; ok {
			continue
		}
		r.deleteAllFor(db, cid)
		delete(r.seenCIDs[db], cid)
	}
}
