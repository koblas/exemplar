package deploy

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/api/types/container"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/koblas/exemplar/internal/config"
	"github.com/koblas/exemplar/internal/store"
	"github.com/koblas/exemplar/internal/store/queries"
	"github.com/koblas/exemplar/internal/store/storetest"
	"github.com/koblas/exemplar/internal/worker"
)

func TestMain(m *testing.M) { storetest.Main(m) }

const pgbouncerDir = "k8s/components/pgbouncer"

// pgbouncerManifests returns the component's pgbouncer.ini, its container
// and its pod spec.
func pgbouncerManifests(t *testing.T) (string, obj, obj) {
	t.Helper()
	var ini string
	var main, spec obj
	data, err := os.ReadFile(filepath.Join(pgbouncerDir, "pgbouncer.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for part := range strings.SplitSeq(string(data), "\n---\n") {
		var d obj
		if err := yaml.Unmarshal([]byte(part), &d); err != nil {
			t.Fatalf("%s: %v", pgbouncerDir, err)
		}
		switch str(d, "kind") {
		case "ConfigMap":
			ini = str(d, "data", "pgbouncer.ini")
		case "Deployment":
			spec = as[obj](t, get(d, "spec", "template", "spec"), "pgbouncer pod spec")
			main = as[obj](t, list(spec, "containers")[0], "pgbouncer container")
		}
	}
	if ini == "" || main == nil {
		t.Fatalf("%s: no pgbouncer.ini or Deployment", pgbouncerDir)
	}
	return ini, main, spec
}

// iniSettings parses the [pgbouncer] section of ini.
func iniSettings(ini string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(ini, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok || line[0] == ';' || line[0] == '[' || line[0] == '%' {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// checkPgbouncer checks the optional pooler: transaction mode with
// prepared statements on the named postgres port, TCP probes on it, and a
// preStop SIGINT shutdown that finishes inside the grace period. SIGTERM
// alone waits for every runner to disconnect.
func checkPgbouncer(t *testing.T, w workload, main obj) {
	t.Helper()
	ini, _, _ := pgbouncerManifests(t)
	set := iniSettings(ini)
	for k, want := range map[string]string{"pool_mode": "transaction", "listen_port": "5432", "auth_file": "/etc/pgbouncer/secret/userlist.txt"} {
		if set[k] != want {
			t.Errorf("%s: pgbouncer.ini %s = %q, want %q", w.name, k, set[k], want)
		}
	}
	if n, _ := strconv.Atoi(set["max_prepared_statements"]); n < 1 {
		t.Errorf("%s: pgbouncer.ini max_prepared_statements must be set: pgx prepares every query", w.name)
	}
	if !strings.Contains(ini, "\n%include /etc/pgbouncer/secret/databases.ini") {
		t.Errorf("%s: pgbouncer.ini does not include the Secret's databases.ini", w.name)
	}
	ports := list(main, "ports")
	if len(ports) != 1 || str(ports[0], "name") != "postgres" || get(ports[0], "containerPort") != uint64(5432) {
		t.Errorf("%s: ports must be postgres 5432, got %v", w.name, ports)
	}
	for _, probe := range []string{"livenessProbe", "readinessProbe"} {
		if str(main, probe, "tcpSocket", "port") != "postgres" {
			t.Errorf("%s: %s must be a TCP check of the postgres port", w.name, probe)
		}
	}
	stop := list(main, "lifecycle", "preStop", "exec", "command")
	script := ""
	if len(stop) == 3 {
		script = as[string](t, stop[2], w.name+": preStop")
	}
	sleep := regexp.MustCompile(`^sleep (\d+); kill -INT 1; .*-lt (\d+) \]`).FindStringSubmatch(script)
	if sleep == nil {
		t.Errorf("%s: preStop must sleep, then SIGINT pgbouncer and wait a bounded time, got %v", w.name, stop)
		return
	}
	drain, _ := strconv.Atoi(sleep[1])
	wait, _ := strconv.Atoi(sleep[2])
	graceSeconds := as[uint64](t, get(w.spec, "terminationGracePeriodSeconds"), w.name+": terminationGracePeriodSeconds")
	if need := uint64(drain + wait); graceSeconds <= need {
		t.Errorf("%s: terminationGracePeriodSeconds %d does not outlast the preStop shutdown %ds", w.name, graceSeconds, need)
	}
}

// pooledJobArgs is a job the test inserts through the pooler.
type pooledJobArgs struct{ N int }

func (pooledJobArgs) Kind() string { return "pooled_test" }

// TestPgbouncerTransactionMode runs the component's pgbouncer.ini in its
// pinned image in front of the test Postgres and drives what a runner does
// through it: claims (sqlc), River job reads, a transaction. A pool of two
// server connections under many clients moves every prepared statement
// between connections. Then the preStop shutdown must stop PgBouncer while
// a client sits idle, which SIGTERM alone would wait on.
func TestPgbouncerTransactionMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	direct := storetest.EmptyURL(t)
	migrated, err := store.Open(ctx, direct)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := migrated.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Control stores the config version runners load.
	snap, err := config.LoadSnapshot(os.DirFS("../examples/exemplar-config"), "cfg")
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.PutSnapshot(ctx, migrated, snap); err != nil {
		t.Fatal(err)
	}
	migrated.Close()

	u, err := url.Parse(direct)
	if err != nil {
		t.Fatal(err)
	}
	pgPort, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	dbName := strings.TrimPrefix(u.Path, "/")

	ini, main, spec := pgbouncerManifests(t)
	// Mounted read-only as the pod mounts the ConfigMap and the Secret.
	conf := as[string](t, list(main, "args")[0], "pgbouncer args")
	mounts := map[string]map[string]string{
		filepath.Dir(conf): {filepath.Base(conf): ini},
		"/etc/pgbouncer/secret": {
			"databases.ini": fmt.Sprintf("[databases]\n%s = host=%s port=%d dbname=%s pool_size=2\n",
				dbName, testcontainers.HostInternal, pgPort, dbName),
			"userlist.txt": fmt.Sprintf("%q %q\n", u.User.Username(), password),
		},
	}
	binds := make([]string, 0, len(mounts))
	for target, files := range mounts {
		dir := t.TempDir()
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o444); err != nil {
				t.Fatal(err)
			}
		}
		binds = append(binds, dir+":"+target+":ro")
	}
	argv := append(list(main, "command"), list(main, "args")...)
	cmd := make([]string, 0, len(argv))
	for _, a := range argv {
		cmd = append(cmd, as[string](t, a, "pgbouncer command"))
	}
	pod := as[obj](t, get(spec, "securityContext"), "pgbouncer securityContext")
	user := fmt.Sprintf("%v:%v", get(pod, "runAsUser"), get(pod, "runAsGroup"))
	ctr, err := testcontainers.Run(ctx, str(main, "image"),
		testcontainers.WithEntrypoint(cmd[0]),
		testcontainers.WithCmd(cmd[1:]...),
		// As the pod runs it: its user, a read-only root, no capabilities,
		// and only /tmp writable.
		testcontainers.WithConfigModifier(func(c *container.Config) { c.User = user }),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, binds...)
			hc.ReadonlyRootfs = true
			hc.CapDrop = []string{"ALL"}
			hc.SecurityOpt = append(hc.SecurityOpt, "no-new-privileges")
			hc.Tmpfs = map[string]string{"/tmp": "size=16m"}
		}),
		testcontainers.WithHostPortAccess(pgPort),
		testcontainers.WithExposedPorts("5432/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("5432/tcp").WithStartupTimeout(time.Minute)),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start pgbouncer: %v", err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	pooled := *u
	pooled.Host = endpoint

	// As run-job opens it.
	st, err := store.OpenWithConns(ctx, pooled.String(), 4)
	if err != nil {
		t.Fatalf("open through pgbouncer: %v", err)
	}
	defer st.Close()
	jobs, err := river.NewClient(riverpgxv5.New(st.Pool), &river.Config{})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := worker.NewClaims(st, []byte("pgbouncer test claim key"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for g := range 16 {
		wg.Go(func() {
			for i := range 10 {
				if err := runnerRound(ctx, st, jobs, claims, g*100+i); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	idle, err := pgx.Connect(ctx, pooled.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idle.Close(context.Background()) }()
	if err := idle.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	preStop := list(main, "lifecycle", "preStop", "exec", "command")
	stop := make([]string, 0, len(preStop))
	for _, a := range preStop {
		stop = append(stop, as[string](t, a, "preStop"))
	}
	start := time.Now()
	// The exec ends with the container; its error is that.
	if _, _, err := ctr.Exec(ctx, stop); err != nil {
		t.Logf("preStop exec: %v", err)
	}
	for {
		s, err := ctr.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !s.Running {
			break
		}
		if time.Since(start) > 20*time.Second {
			t.Fatal("pgbouncer still running 20 s after its preStop with a client idle")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// runnerRound is one runner's database work: mint, redeem and release a
// claim, load its config version, insert and read a River job, and a
// transaction.
func runnerRound(ctx context.Context, st *store.Store, jobs *river.Client[pgx.Tx], claims *worker.Claims, n int) error {
	res, err := jobs.Insert(ctx, pooledJobArgs{N: n}, nil)
	if err != nil {
		return fmt.Errorf("round %d: insert job: %w", n, err)
	}
	id := res.Job.ID
	nonce, err := claims.Mint(ctx, worker.Task{JobID: id, Attempt: 1, ConfigVersion: "cfg"}, worker.Grant{})
	if err != nil {
		return fmt.Errorf("round %d: %w", n, err)
	}
	_, v, err := claims.Redeem(ctx, id, nonce)
	if err != nil || v != "cfg" {
		return fmt.Errorf("round %d: redeem: %q, %w", n, v, err)
	}
	if _, err := worker.StoredSnapshot(ctx, st, v); err != nil {
		return fmt.Errorf("round %d: %w", n, err)
	}
	if err := claims.Release(ctx, id); err != nil {
		return fmt.Errorf("round %d: %w", n, err)
	}
	if _, err := jobs.JobGet(ctx, id); err != nil {
		return fmt.Errorf("round %d: get job: %w", n, err)
	}
	return st.InTx(ctx, func(tx pgx.Tx, _ *queries.Queries) error {
		var one int
		return tx.QueryRow(ctx, "SELECT $1::int", 1).Scan(&one)
	})
}
