// Package kubelaunch runs dispatched reviews as Kubernetes Jobs, one per
// River job attempt (spec §2.4 C1). The Job's pod comes from a PodTemplate
// in the manifests (exemplar-runner); its runner container is started as
// `run-job --job ID --config-version V` and its exit code is the
// worker.Exit the dispatcher maps back to River.
//
// River owns retries: a Job never retries (backoffLimit 0), and before a
// Job starts every earlier Job for the same River job is deleted and gone,
// so a re-dispatch after control died never runs two runners side by side.
package kubelaunch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/koblas/exemplar/internal/worker"
)

// Labels on runner Jobs and pods.
const (
	LabelName    = "app.kubernetes.io/name"
	LabelPartOf  = "app.kubernetes.io/part-of"
	LabelRiver   = "exemplar.dev/river-job"
	LabelKind    = "exemplar.dev/kind"
	RunnerName   = "exemplar-runner"
	RunnerTarget = "runner" // the template's container that runs run-job
)

// Defaults.
const (
	DefaultPoll        = 3 * time.Second
	DefaultCancelGrace = 30 * time.Second
	DefaultReplaceWait = 60 * time.Second
	// deadlineMargin is added to the job's timeout for the Job's
	// activeDeadlineSeconds: River gives up first.
	deadlineMargin = 2 * time.Minute
	// defaultDeadline is the Job's deadline for a kind without a timeout.
	defaultDeadline = time.Hour
	// finishedTTL keeps a finished Job (and its pod's logs) this long.
	finishedTTL int32 = 600
)

var (
	errNoTemplate  = errors.New("kubelaunch: runner template")
	errNotReplaced = errors.New("kubelaunch: an earlier runner is still going")
	errJobDeleted  = errors.New("kubelaunch: runner job deleted")
)

// Config wires a Launcher.
type Config struct {
	Client    kubernetes.Interface
	Namespace string
	// Template names the PodTemplate (default exemplar-runner).
	Template string
	// Poll is how often a running Job is read (default 3 s).
	Poll time.Duration
	// CancelGrace is how long a cancelled runner may take to stop on its
	// own (it watches its River row) before its Job is deleted (default 30 s).
	CancelGrace time.Duration
	// ReplaceWait bounds the wait for an earlier Job of the same River job
	// to go (default 60 s); past it the task is snoozed.
	ReplaceWait time.Duration
	Log         *slog.Logger
}

// Launcher implements worker.Launcher with a Kubernetes Job per task.
type Launcher struct {
	cfg  Config
	tmpl corev1.PodTemplateSpec
}

// New reads the runner PodTemplate; a missing or unusable one fails here,
// at startup, rather than at the first review.
func New(ctx context.Context, cfg Config) (*Launcher, error) {
	if cfg.Client == nil || cfg.Namespace == "" {
		return nil, fmt.Errorf("%w: needs a client and a namespace", errNoTemplate)
	}
	if cfg.Template == "" {
		cfg.Template = RunnerName
	}
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultPoll
	}
	if cfg.CancelGrace <= 0 {
		cfg.CancelGrace = DefaultCancelGrace
	}
	if cfg.ReplaceWait <= 0 {
		cfg.ReplaceWait = DefaultReplaceWait
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	pt, err := cfg.Client.CoreV1().PodTemplates(cfg.Namespace).Get(ctx, cfg.Template, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w %s/%s: %w", errNoTemplate, cfg.Namespace, cfg.Template, err)
	}
	if runnerIndex(pt.Template.Spec.Containers) < 0 {
		return nil, fmt.Errorf("%w %s has no %q container", errNoTemplate, cfg.Template, RunnerTarget)
	}
	return &Launcher{cfg: cfg, tmpl: pt.Template}, nil
}

func runnerIndex(cs []corev1.Container) int {
	for i, c := range cs {
		if c.Name == RunnerTarget {
			return i
		}
	}
	return -1
}

// JobName is the Job for one attempt of a River job.
func JobName(t worker.Task) string {
	return fmt.Sprintf("%s-%d-%d", RunnerName, t.JobID, t.Attempt)
}

// Launch implements worker.Launcher.
func (l *Launcher) Launch(ctx context.Context, t worker.Task) (worker.Exit, error) {
	if err := l.clearEarlier(ctx, t); err != nil {
		return worker.ExitError, err
	}
	jobs := l.cfg.Client.BatchV1().Jobs(l.cfg.Namespace)
	job, err := jobs.Create(ctx, l.job(t), metav1.CreateOptions{})
	if err != nil {
		return worker.ExitError, fmt.Errorf("kubelaunch: create job: %w", err)
	}
	l.cfg.Log.Info("runner started", "job", t.JobID, "kind", t.Kind, "k8s_job", job.Name, "config", t.ConfigVersion)
	tick := time.NewTicker(l.cfg.Poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return l.stop(ctx, job.Name)
		case <-tick.C:
		}
		exit, done, err := l.finished(ctx, job.Name)
		if err != nil || done {
			return exit, err
		}
	}
}

// job builds the Job for t from the template.
func (l *Launcher) job(t worker.Task) *batchv1.Job {
	labels := map[string]string{
		LabelName: RunnerName, LabelPartOf: "exemplar",
		LabelRiver: strconv.FormatInt(t.JobID, 10), LabelKind: t.Kind,
	}
	tmpl := *l.tmpl.DeepCopy()
	if tmpl.Labels == nil {
		tmpl.Labels = map[string]string{}
	}
	maps.Copy(tmpl.Labels, labels)
	tmpl.Spec.RestartPolicy = corev1.RestartPolicyNever
	c := &tmpl.Spec.Containers[runnerIndex(tmpl.Spec.Containers)]
	c.Args = append([]string{"run-job", "--job=" + strconv.FormatInt(t.JobID, 10), "--config-version=" + t.ConfigVersion}, c.Args...)
	deadline := int64((max(t.Timeout, 0) + deadlineMargin) / time.Second)
	if t.Timeout <= 0 {
		deadline = int64(defaultDeadline / time.Second)
	}
	backoff, ttl := int32(0), finishedTTL
	return &batchv1.Job{
		Name: JobName(t), Namespace: l.cfg.Namespace, Labels: labels,
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff, ActiveDeadlineSeconds: &deadline, TTLSecondsAfterFinished: &ttl,
			Template: tmpl,
		},
	}
}

// clearEarlier deletes every Job left for t's River job (an earlier
// attempt, or this one from a control pod that died) and waits until
// they are gone, pods included.
func (l *Launcher) clearEarlier(ctx context.Context, t worker.Task) error {
	jobs := l.cfg.Client.BatchV1().Jobs(l.cfg.Namespace)
	sel := metav1.ListOptions{LabelSelector: LabelRiver + "=" + strconv.FormatInt(t.JobID, 10)}
	list, err := jobs.List(ctx, sel)
	if err != nil {
		return fmt.Errorf("kubelaunch: list earlier jobs: %w", err)
	}
	if len(list.Items) == 0 {
		return nil
	}
	fg := metav1.DeletePropagationForeground
	for _, j := range list.Items {
		l.cfg.Log.Warn("deleting an earlier runner", "job", t.JobID, "k8s_job", j.Name)
		if err := jobs.Delete(ctx, j.Name, metav1.DeleteOptions{PropagationPolicy: &fg}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("kubelaunch: delete earlier job %s: %w", j.Name, err)
		}
	}
	wctx, cancel := context.WithTimeout(ctx, l.cfg.ReplaceWait)
	defer cancel()
	tick := time.NewTicker(l.cfg.Poll)
	defer tick.Stop()
	for {
		list, err := jobs.List(wctx, sel)
		if err == nil && len(list.Items) == 0 {
			return nil
		}
		select {
		case <-wctx.Done():
			return fmt.Errorf("%w: %w", worker.ErrNoCapacity, errNotReplaced)
		case <-tick.C:
		}
	}
}

// finished reports whether the Job ended and how.
func (l *Launcher) finished(ctx context.Context, name string) (worker.Exit, bool, error) {
	job, err := l.cfg.Client.BatchV1().Jobs(l.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return worker.ExitError, true, fmt.Errorf("%w: %s", errJobDeleted, name)
	}
	if err != nil {
		// The API server blipped; the Job runs on.
		l.cfg.Log.Warn("reading runner job failed", "k8s_job", name, "err", err)
		return 0, false, nil
	}
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		if c.Type == batchv1.JobComplete {
			return worker.ExitDone, true, nil
		}
		if c.Type == batchv1.JobFailed {
			return l.exitCode(ctx, name, c.Reason), true, nil
		}
	}
	return 0, false, nil
}

// exitCode reads the runner container's exit code from the Job's pod; a
// pod that never got one (evicted, deadline, node lost) is an error.
func (l *Launcher) exitCode(ctx context.Context, name, reason string) worker.Exit {
	pods, err := l.cfg.Client.CoreV1().Pods(l.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: batchv1.JobNameLabel + "=" + name})
	if err != nil {
		l.cfg.Log.Warn("reading runner pod failed", "k8s_job", name, "err", err)
		return worker.ExitError
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Name == RunnerTarget && cs.State.Terminated != nil {
				return worker.Exit(cs.State.Terminated.ExitCode)
			}
		}
	}
	l.cfg.Log.Warn("runner ended without an exit code", "k8s_job", name, "reason", reason)
	return worker.ExitError
}

// stop ends a cancelled task.
//   - A supersede (River's remote cancel) reaches the runner through its
//     River row: it may finish (posting its notice) within CancelGrace,
//     otherwise its Job is deleted.
//   - River's timeout deletes the Job at once: the attempt is over, and
//     after the last one nothing would replace it.
//   - Anything else is control stopping: the Job is left, River retries the
//     job, and the retry deletes it first.
func (l *Launcher) stop(ctx context.Context, name string) (worker.Exit, error) {
	cause := context.Cause(ctx)
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		l.delete(ctx, name)
		return worker.ExitError, fmt.Errorf("kubelaunch: job %s timed out: %w", name, cause)
	case !worker.Superseded(ctx):
		return worker.ExitError, fmt.Errorf("kubelaunch: job %s left running: %w", name, cause)
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.cfg.CancelGrace)
	defer cancel()
	tick := time.NewTicker(l.cfg.Poll)
	defer tick.Stop()
	for {
		if exit, done, err := l.finished(sctx, name); done && err == nil {
			return exit, nil
		}
		select {
		case <-sctx.Done():
			l.delete(ctx, name)
			return worker.ExitError, fmt.Errorf("kubelaunch: job %s cancelled: %w", name, cause)
		case <-tick.C:
		}
	}
}

// delete removes a runner Job and its pod (SIGTERM, then the grace period).
func (l *Launcher) delete(ctx context.Context, name string) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	bg := metav1.DeletePropagationBackground
	if err := l.cfg.Client.BatchV1().Jobs(l.cfg.Namespace).Delete(dctx, name, metav1.DeleteOptions{PropagationPolicy: &bg}); err != nil && !apierrors.IsNotFound(err) {
		l.cfg.Log.Warn("deleting a runner job failed", "k8s_job", name, "err", err)
	}
}
