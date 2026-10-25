package kubelaunch

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/worker"
)

const ns = "exemplar"

func template() *corev1.PodTemplate {
	return &corev1.PodTemplate{
		Name: RunnerName, Namespace: ns,
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{LabelName: RunnerName}},
			Spec: corev1.PodSpec{
				ServiceAccountName: RunnerName,
				Containers: []corev1.Container{{
					Name: RunnerTarget, Image: "exemplar", Command: []string{"/usr/local/bin/exemplar-worker"},
					Args: []string{"--work-dir=/var/lib/exemplar/work"},
				}},
			},
		},
	}
}

// cluster is a fake API server with a hand-driven Job controller. Tests
// build it inside a synctest bubble, so the launcher's polls, grace and
// replace waits run at their default lengths in fake time.
type cluster struct {
	t      *testing.T
	client *fake.Clientset
	l      *Launcher
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	client := fake.NewClientset(template())
	l, err := New(t.Context(), Config{Client: client, Namespace: ns})
	if err != nil {
		t.Fatal(err)
	}
	return &cluster{t: t, client: client, l: l}
}

// waitJob waits, in bubble time, for the Job named name to exist.
func (c *cluster) waitJob(name string) *batchv1.Job {
	c.t.Helper()
	for range 100 {
		synctest.Wait()
		if j, err := c.client.BatchV1().Jobs(ns).Get(context.Background(), name, metav1.GetOptions{}); err == nil {
			return j
		}
		time.Sleep(DefaultPoll)
	}
	c.t.Fatalf("job %s never created", name)
	return nil
}

// finish ends Job name as its runner exiting with code would; a nil code
// is a pod that died without one.
func (c *cluster) finish(name string, code *int32) {
	c.t.Helper()
	ctx := context.Background()
	if code != nil {
		pod := &corev1.Pod{
			Name: name + "-abcde", Namespace: ns, Labels: map[string]string{batchv1.JobNameLabel: name},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: RunnerTarget, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: *code}},
			}}},
		}
		if _, err := c.client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
			c.t.Fatal(err)
		}
	}
	j, err := c.client.BatchV1().Jobs(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		c.t.Fatal(err)
	}
	cond := batchv1.JobFailed
	if code != nil && *code == 0 {
		cond = batchv1.JobComplete
	}
	j.Status.Conditions = append(j.Status.Conditions, batchv1.JobCondition{Type: cond, Status: corev1.ConditionTrue, Reason: "test"})
	if _, err := c.client.BatchV1().Jobs(ns).UpdateStatus(ctx, j, metav1.UpdateOptions{}); err != nil {
		c.t.Fatal(err)
	}
}

type result struct {
	exit worker.Exit
	err  error
}

func (c *cluster) launch(ctx context.Context, task worker.Task) <-chan result {
	out := make(chan result, 1)
	go func() {
		exit, err := c.l.Launch(ctx, task)
		out <- result{exit, err}
	}()
	return out
}

func task(id int64, attempt int) worker.Task {
	return worker.Task{JobID: id, Kind: "pr_review", Attempt: attempt, ConfigVersion: "abc123", Timeout: worker.ReviewTimeout, Nonce: "n0nce"}
}

func TestJobSpec(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t)
		tk := task(7, 2)
		res := c.launch(t.Context(), tk)
		j := c.waitJob("exemplar-runner-7-2")
		c.finish(j.Name, new(int32(0)))
		if r := <-res; r.exit != worker.ExitDone || r.err != nil {
			t.Fatalf("launch = %+v", r)
		}
		s := j.Spec
		if *s.BackoffLimit != 0 || *s.ActiveDeadlineSeconds != int64((worker.ReviewTimeout+deadlineMargin)/time.Second) || *s.TTLSecondsAfterFinished != finishedTTL {
			t.Errorf("job spec = backoff %d deadline %d ttl %d", *s.BackoffLimit, *s.ActiveDeadlineSeconds, *s.TTLSecondsAfterFinished)
		}
		if j.Labels[LabelRiver] != "7" || s.Template.Labels[LabelRiver] != "7" || s.Template.Labels[LabelName] != RunnerName || s.Template.Labels[LabelKind] != "pr_review" {
			t.Errorf("labels = %v / %v", j.Labels, s.Template.Labels)
		}
		want := []string{"run-job", "--job=7", "--config-version=abc123", "--work-dir=/var/lib/exemplar/work"}
		if got := s.Template.Spec.Containers[0].Args; !slices.Equal(got, want) || s.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
			t.Errorf("runner args = %v, restart %s", got, s.Template.Spec.RestartPolicy)
		}
		if env := s.Template.Spec.Containers[0].Env; len(env) != 1 || env[0].Name != NonceEnv || env[0].Value != "n0nce" {
			t.Errorf("runner env = %v", env)
		}
		// The template itself is untouched.
		if got := c.l.tmpl.Spec.Containers[0].Args; len(got) != 1 {
			t.Errorf("template args = %v", got)
		}
	})
}

func TestExitCodes(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		code *int32
		want worker.Exit
	}{
		"done":       {new(int32(0)), worker.ExitDone},
		"cancel":     {new(int32(worker.ExitCancel)), worker.ExitCancel},
		"superseded": {new(int32(worker.ExitSuperseded)), worker.ExitSuperseded},
		"error":      {new(int32(1)), worker.ExitError},
		"oom killed": {new(int32(137)), worker.Exit(137)},
		"pod lost":   {nil, worker.ExitError},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				c := newCluster(t)
				res := c.launch(t.Context(), task(1, 1))
				c.finish(c.waitJob("exemplar-runner-1-1").Name, tc.code)
				if r := <-res; r.exit != tc.want || r.err != nil {
					t.Errorf("launch = %+v, want %d", r, tc.want)
				}
			})
		})
	}
}

// A re-dispatch (control died, or River retried) first deletes every
// earlier Job of the same River job, so two runners never overlap.
func TestEarlierJobDeleted(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t)
		old := c.l.job(task(9, 1))
		if _, err := c.client.BatchV1().Jobs(ns).Create(t.Context(), old, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		other := c.l.job(task(10, 1))
		if _, err := c.client.BatchV1().Jobs(ns).Create(t.Context(), other, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		res := c.launch(t.Context(), task(9, 2))
		c.finish(c.waitJob("exemplar-runner-9-2").Name, new(int32(0)))
		if r := <-res; r.exit != worker.ExitDone || r.err != nil {
			t.Fatalf("launch = %+v", r)
		}
		if _, err := c.client.BatchV1().Jobs(ns).Get(t.Context(), old.Name, metav1.GetOptions{}); err == nil {
			t.Error("the earlier runner still exists")
		}
		if _, err := c.client.BatchV1().Jobs(ns).Get(t.Context(), other.Name, metav1.GetOptions{}); err != nil {
			t.Errorf("another job's runner was touched: %v", err)
		}
	})
}

// An earlier Job that does not go in time leaves the task snoozed.
func TestEarlierJobStuck(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		c := newCluster(t)
		if _, err := c.client.BatchV1().Jobs(ns).Create(t.Context(), c.l.job(task(9, 1)), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		c.client.PrependReactor("delete", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
		exit, err := c.l.Launch(t.Context(), task(9, 2))
		if !errors.Is(err, worker.ErrNoCapacity) || exit != worker.ExitError {
			t.Fatalf("launch = %d, %v", exit, err)
		}
		if _, err := c.client.BatchV1().Jobs(ns).Get(t.Context(), "exemplar-runner-9-2", metav1.GetOptions{}); err == nil {
			t.Error("started beside a running runner")
		}
	})
}

func TestCancel(t *testing.T) {
	t.Parallel()
	t.Run("superseded runner stops itself", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			c := newCluster(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			res := c.launch(ctx, task(3, 1))
			name := c.waitJob("exemplar-runner-3-1").Name
			cancel(rivertype.ErrJobCancelledRemotely)
			c.finish(name, new(int32(worker.ExitSuperseded)))
			if r := <-res; r.exit != worker.ExitSuperseded || r.err != nil {
				t.Errorf("launch = %+v", r)
			}
		})
	})
	t.Run("superseded runner past its grace is deleted", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			c := newCluster(t)
			ctx, cancel := context.WithCancelCause(t.Context())
			res := c.launch(ctx, task(4, 1))
			name := c.waitJob("exemplar-runner-4-1").Name
			cancel(rivertype.ErrJobCancelledRemotely)
			r := <-res
			if r.err == nil || r.exit != worker.ExitError {
				t.Errorf("launch = %+v", r)
			}
			if _, err := c.client.BatchV1().Jobs(ns).Get(t.Context(), name, metav1.GetOptions{}); err == nil {
				t.Error("cancelled runner not deleted")
			}
		})
	})
	t.Run("timed out runner is deleted", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			c := newCluster(t)
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			res := c.launch(ctx, task(6, 1))
			name := c.waitJob("exemplar-runner-6-1").Name
			if r := <-res; !errors.Is(r.err, context.DeadlineExceeded) {
				t.Errorf("launch = %+v", r)
			}
			if _, err := c.client.BatchV1().Jobs(ns).Get(t.Context(), name, metav1.GetOptions{}); err == nil {
				t.Error("timed-out runner not deleted")
			}
		})
	})
	t.Run("control stopping leaves the runner", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			c := newCluster(t)
			ctx, cancel := context.WithCancel(t.Context())
			res := c.launch(ctx, task(5, 1))
			name := c.waitJob("exemplar-runner-5-1").Name
			cancel()
			if r := <-res; !errors.Is(r.err, context.Canceled) {
				t.Errorf("launch = %+v", r)
			}
			if _, err := c.client.BatchV1().Jobs(ns).Get(t.Context(), name, metav1.GetOptions{}); err != nil {
				t.Errorf("runner deleted on shutdown: %v", err)
			}
		})
	})
}

func TestNewChecksTemplate(t *testing.T) {
	t.Parallel()
	if _, err := New(t.Context(), Config{Client: fake.NewClientset(), Namespace: ns}); !errors.Is(err, errNoTemplate) {
		t.Errorf("missing template = %v", err)
	}
	bad := template()
	bad.Template.Spec.Containers[0].Name = "worker"
	if _, err := New(t.Context(), Config{Client: fake.NewClientset(bad), Namespace: ns}); !errors.Is(err, errNoTemplate) {
		t.Errorf("template without a runner container = %v", err)
	}
}

// fakeRunner is an in-process runner that exits as told.
type fakeRunner struct{ exit worker.Exit }

func (f fakeRunner) Run(context.Context, int64, string, string) worker.Exit { return f.exit }

// conformanceLauncher returns the launcher named name whose runner exits
// with exit; the kubernetes one finishes its Job as that runner would.
func conformanceLauncher(t *testing.T, name string, exit worker.Exit) worker.Launcher {
	t.Helper()
	if name == "in-process" {
		return worker.InProcess{Runner: fakeRunner{exit}}
	}
	c := newCluster(t)
	go func() {
		name := c.waitJob(JobName(task(1, 1))).Name
		code := int32(exit)
		c.finish(name, &code)
	}()
	return c.l
}

// TestConformance runs the same runner outcomes through the dispatcher on
// both launchers: River sees the same result whichever runs the review.
func TestConformance(t *testing.T) {
	t.Parallel()
	snap := &config.Snapshot{Version: "abc123"}
	outcomes := map[string]struct {
		exit worker.Exit
		want func(error) bool
	}{
		"done": {worker.ExitDone, func(err error) bool { return err == nil }},
		"cancel": {worker.ExitCancel, func(err error) bool {
			var c *rivertype.JobCancelError
			return errors.As(err, &c) && err != rivertype.ErrJobCancelledRemotely //nolint:errorlint // the sentinel itself
		}},
		"superseded": {worker.ExitSuperseded, func(err error) bool { return err == rivertype.ErrJobCancelledRemotely }}, //nolint:errorlint // the sentinel itself
		"error": {worker.ExitError, func(err error) bool {
			var c *rivertype.JobCancelError
			return err != nil && !errors.As(err, &c)
		}},
	}
	for _, lname := range []string{"in-process", "kubernetes"} {
		for oname, o := range outcomes {
			t.Run(lname+"/"+oname, func(t *testing.T) {
				t.Parallel()
				synctest.Test(t, func(t *testing.T) {
					d, err := worker.NewReviewDispatcher(worker.DispatchConfig{
						Launcher: conformanceLauncher(t, lname, o.exit), Snapshot: func(context.Context) (*config.Snapshot, error) { return snap, nil },
					})
					if err != nil {
						t.Fatal(err)
					}
					err = d.Work(t.Context(), &river.Job[store.PRReviewArgs]{
						JobRow: &rivertype.JobRow{ID: 1, Kind: "pr_review", Attempt: 1}, Args: store.PRReviewArgs{Repo: "acme/app"},
					})
					if !o.want(err) {
						t.Errorf("work = %v", err)
					}
				})
			})
		}
	}
}
