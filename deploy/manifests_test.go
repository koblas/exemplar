// Package deploy checks the Kubernetes manifests in deploy/k8s against the
// code they run, without a cluster: references resolve, every pod is
// hardened, probes and ports match the binaries, grace periods outlast
// the shutdown deadlines in the source, and every environment variable is
// one the code reads.
package deploy

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
)

// core holds everything but inbound; settings.yaml there documents the
// secrets every build references.
const core = "k8s/core"

type obj = map[string]any

func get(o any, path ...string) any {
	for _, p := range path {
		m, ok := o.(map[string]any)
		if !ok {
			return nil
		}
		o = m[p]
	}
	return o
}

func str(o any, path ...string) string {
	s, _ := get(o, path...).(string)
	return s
}

func list(o any, path ...string) []any {
	l, _ := get(o, path...).([]any)
	return l
}

// as asserts that v is a T, failing the test with what if it is not.
func as[T any](t *testing.T, v any, what string) T {
	t.Helper()
	x, ok := v.(T)
	if !ok {
		t.Fatalf("%s: got %T, want %T", what, v, x)
	}
	return x
}

// variants are the builds the repository ships, plus the one an
// installation composes for both ways in (base with components/ingress).
var variants = []struct {
	name      string
	roots     []string
	workloads int      // long-running services and CronJobs
	ingresses int      // Ingress objects
	inbound   []string // who may reach the webhook and the API
}{
	{"tunnel", []string{"k8s/base"}, 19, 0, []string{"pod exemplar-cloudflared"}},
	{"direct", []string{"k8s/direct"}, 18, 2, []string{"namespace ingress-nginx"}},
	{"both", []string{"k8s/base", "k8s/components/ingress"}, 19, 2, []string{"namespace ingress-nginx", "pod exemplar-cloudflared"}},
	{"postgres", []string{"k8s/base", "k8s/components/postgres"}, 20, 0, []string{"pod exemplar-cloudflared"}},
}

// load reads every document of every manifest the kustomizations under
// roots list, following resource directories and components. It returns
// the documents and every kustomization's images.
func load(t *testing.T, roots ...string) ([]obj, []any) {
	t.Helper()
	var docs []obj
	var images []any
	seen := map[string]bool{}
	var walk func(dir string)
	walk = func(dir string) {
		dir = filepath.Clean(dir)
		if seen[dir] {
			return
		}
		seen[dir] = true
		var kustom obj
		raw, err := os.ReadFile(filepath.Join(dir, "kustomization.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(raw, &kustom); err != nil {
			t.Fatal(err)
		}
		images = append(images, list(kustom, "images")...)
		if len(list(kustom, "patches")) > 0 || get(kustom, "patchesStrategicMerge") != nil {
			t.Errorf("%s: patches are not modelled here; add resources instead", dir)
		}
		listed := map[string]bool{}
		for _, r := range list(kustom, "resources") {
			rs := as[string](t, r, dir+": resource")
			if fi, err := os.Stat(filepath.Join(dir, rs)); err == nil && fi.IsDir() {
				walk(filepath.Join(dir, rs))
				continue
			}
			listed[rs] = true
		}
		for _, c := range list(kustom, "components") {
			walk(filepath.Join(dir, as[string](t, c, dir+": component")))
		}
		files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
		for _, f := range files {
			name := filepath.Base(f)
			if name == "kustomization.yaml" {
				continue
			}
			if !listed[name] {
				t.Errorf("%s is not in %s/kustomization.yaml resources", name, dir)
			}
			delete(listed, name)
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte("&")) && regexp.MustCompile(`:\s+&\w`).Match(data) {
				t.Errorf("%s uses a YAML anchor; spell blocks out for kustomize", name)
			}
			dec := yaml.NewDecoder(bytes.NewReader(data))
			for {
				var d obj
				err := dec.Decode(&d)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("%s: %v", f, err)
				}
				if d != nil {
					d["_file"] = filepath.Join(dir, name)
					docs = append(docs, d)
				}
			}
		}
		for name := range listed {
			t.Errorf("%s/kustomization.yaml lists missing %s", dir, name)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	ids := map[string]string{}
	for _, d := range docs {
		id := str(d, "kind") + "/" + str(d, "metadata", "name")
		if f, dup := ids[id]; dup {
			t.Errorf("%s is in both %s and %s", id, f, d["_file"])
		}
		ids[id] = as[string](t, d["_file"], id+": _file")
	}
	return docs, images
}

// workload is a pod template with what owns it.
type workload struct {
	kind, name string
	labels     obj // template labels
	spec       obj // pod spec
	doc        obj
}

func workloads(docs []obj) []workload {
	var out []workload
	for _, d := range docs {
		tmpl := get(d, "spec", "template")
		if str(d, "kind") == "CronJob" {
			tmpl = get(d, "spec", "jobTemplate", "spec", "template")
		}
		if tmpl == nil {
			continue
		}
		labels, _ := get(tmpl, "metadata", "labels").(obj)
		spec, _ := get(tmpl, "spec").(obj)
		out = append(out, workload{kind: str(d, "kind"), name: str(d, "metadata", "name"), labels: labels, spec: spec, doc: d})
	}
	return out
}

func containers(t *testing.T, spec obj) []obj {
	t.Helper()
	var out []obj
	for _, k := range []string{"initContainers", "containers"} {
		for _, c := range list(spec, k) {
			out = append(out, as[obj](t, c, k))
		}
	}
	return out
}

func matches(selector, labels obj) bool {
	if len(selector) == 0 {
		return false
	}
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func TestManifests(t *testing.T) {
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			docs, images := load(t, v.roots...)
			checkManifests(t, docs, images, v.workloads)
			checkInbound(t, docs, v.ingresses, v.inbound)
		})
	}
}

func checkManifests(t *testing.T, docs []obj, images []any, want int) {
	t.Helper()
	ws := workloads(docs)
	if len(ws) != want {
		t.Errorf("workloads = %d, want %d", len(ws), want)
	}

	accounts, settings, secrets := map[string]bool{}, obj{}, map[string]bool{}
	for _, d := range docs {
		switch {
		case str(d, "kind") == "Namespace":
			if str(d, "metadata", "labels", "pod-security.kubernetes.io/enforce") != "restricted" {
				t.Error("namespace does not enforce restricted Pod Security")
			}
		case str(d, "metadata", "namespace") != "exemplar":
			t.Errorf("%s %s is not in namespace exemplar", str(d, "kind"), str(d, "metadata", "name"))
		}
		switch str(d, "kind") {
		case "ServiceAccount":
			accounts[str(d, "metadata", "name")] = true
			if get(d, "automountServiceAccountToken") != false {
				t.Errorf("service account %s mounts a token", str(d, "metadata", "name"))
			}
		case "ConfigMap":
			if str(d, "metadata", "name") == "exemplar-settings" {
				settings, _ = get(d, "data").(obj)
			}
		}
	}
	// The secrets settings.yaml documents; anything else is a typo.
	settingsDoc, _ := os.ReadFile(filepath.Join(core, "settings.yaml"))
	for _, m := range regexp.MustCompile(`#\s+(exemplar-[a-z-]+)\s{2,}`).FindAllStringSubmatch(string(settingsDoc), -1) {
		secrets[m[1]] = true
	}

	managed := map[string]bool{}
	for _, im := range images {
		managed[str(im, "name")] = true
		if !strings.HasPrefix(str(im, "digest"), "sha256:") {
			t.Errorf("image %s is not pinned by digest", str(im, "name"))
		}
	}

	envs := envNames(t)
	for _, w := range ws {
		spec := w.spec
		if !accounts[str(spec, "serviceAccountName")] || get(spec, "automountServiceAccountToken") != false {
			t.Errorf("%s: service account %q or token mount", w.name, str(spec, "serviceAccountName"))
		}
		if get(spec, "securityContext", "runAsNonRoot") != true || str(spec, "securityContext", "seccompProfile", "type") != "RuntimeDefault" {
			t.Errorf("%s: pod security context", w.name)
		}
		volumes := map[string]obj{}
		for _, v := range list(w.doc, "spec", "volumeClaimTemplates") {
			volumes[str(v, "metadata", "name")] = as[obj](t, v, w.name+": volumeClaimTemplate")
		}
		for _, v := range list(spec, "volumes") {
			volumes[str(v, "name")] = as[obj](t, v, w.name+": volume")
			if s := str(v, "secret", "secretName"); s != "" && !secrets[s] {
				t.Errorf("%s: secret %s is not documented in settings.yaml", w.name, s)
			}
		}
		for _, c := range containers(t, spec) {
			cname := w.name + "/" + str(c, "name")
			sc := get(c, "securityContext")
			if get(sc, "allowPrivilegeEscalation") != false || get(sc, "readOnlyRootFilesystem") != true ||
				!slices.Contains(list(sc, "capabilities", "drop"), any("ALL")) {
				t.Errorf("%s: container security context", cname)
			}
			for _, r := range []string{"requests", "limits"} {
				if get(c, "resources", r, "cpu") == nil || get(c, "resources", r, "memory") == nil {
					t.Errorf("%s: no cpu/memory %s", cname, r)
				}
			}
			img := str(c, "image")
			if !managed[img] && !strings.Contains(img, "@sha256:") || strings.HasSuffix(img, ":latest") {
				t.Errorf("%s: image %q is neither kustomize-pinned nor a digest", cname, img)
			}
			tmp := false
			for _, m := range list(c, "volumeMounts") {
				if volumes[str(m, "name")] == nil {
					t.Errorf("%s: mounts undefined volume %s", cname, str(m, "name"))
				}
				tmp = tmp || str(m, "mountPath") == "/tmp"
			}
			if !tmp {
				t.Errorf("%s: no writable /tmp under a read-only root", cname)
			}
			for _, e := range list(c, "env") {
				name := str(e, "name")
				if strings.HasPrefix(name, "EXEMPLAR_") && name != "EXEMPLAR_CONFIG_GIT_URL" && !envs[name] {
					t.Errorf("%s: no code reads %s", cname, name)
				}
				if k := str(e, "valueFrom", "configMapKeyRef", "key"); k != "" && settings[k] == nil {
					t.Errorf("%s: settings has no key %s", cname, k)
				}
				if s := str(e, "valueFrom", "secretKeyRef", "name"); s != "" && !secrets[s] {
					t.Errorf("%s: secret %s is not documented in settings.yaml", cname, s)
				}
			}
		}
	}
	checkServices(t, docs, ws)
}

// shutdown is each long-running binary's worst-case shutdown, from its
// source and its args.
func shutdown(t *testing.T, binary string, args []string) time.Duration {
	t.Helper()
	flag := func(name string, def time.Duration) time.Duration {
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
				d, err := time.ParseDuration(v)
				if err != nil {
					t.Fatalf("%s %s: %v", binary, a, err)
				}
				return d
			}
		}
		return def
	}
	src := func(file, re string) time.Duration {
		data, err := os.ReadFile(filepath.Join("..", "cmd", binary, file))
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(re).FindSubmatch(data)
		if m == nil {
			t.Fatalf("%s/%s: no match for %s (did shutdown change?)", binary, file, re)
		}
		n, _ := strconv.Atoi(string(m[1]))
		return time.Duration(n) * time.Second
	}
	switch binary {
	case "exemplar-webhook", "exemplar-api":
		return flag("drain", 5*time.Second) + src("main.go", `WithTimeout\(context\.Background\(\), (\d+)\*time\.Second\)\s+defer cancel\(\)\s+(?:return|if err :=) hs\.Shutdown`)
	case "exemplar-gateway":
		return src("main.go", `shutdownTimeout = (\d+) \* time\.Second`)
	case "exemplar-search":
		return flag("drain", 5*time.Second) + src("serve.go", `sctx, cancel := context\.WithTimeout\(context\.Background\(\), (\d+)\*time\.Second\)`)
	case "exemplar-worker":
		return flag("drain", 5*time.Second) + flag("stop-timeout", 60*time.Second) + 10*time.Second + 5*time.Second
	case "cloudflared":
		return flag("grace-period", 30*time.Second)
	case "docker-entrypoint.sh": // Postgres; args include its preStop pg_ctl stop
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--timeout="); ok {
				n, err := strconv.Atoi(v)
				if err != nil {
					t.Fatalf("%s %s: %v", binary, a, err)
				}
				return time.Duration(n) * time.Second
			}
		}
		t.Fatalf("%s: no preStop pg_ctl stop --timeout", binary)
	}
	t.Fatalf("no shutdown budget for %s", binary)
	return 0
}

func checkServices(t *testing.T, docs []obj, ws []workload) {
	t.Helper()
	long := map[string]workload{}
	for _, w := range ws {
		if w.kind == "CronJob" {
			continue
		}
		long[w.name] = w
		sel, _ := get(w.doc, "spec", "selector", "matchLabels").(obj)
		if !matches(sel, w.labels) {
			t.Errorf("%s: selector does not match its pod labels", w.name)
		}
		main := as[obj](t, list(w.spec, "containers")[0], w.name+": container")
		var cmd []string
		for _, c := range list(main, "command") {
			cmd = append(cmd, as[string](t, c, w.name+": command"))
		}
		var args []string
		for _, a := range list(main, "args") {
			args = append(args, as[string](t, a, w.name+": arg"))
		}
		if len(cmd) == 0 {
			t.Errorf("%s: main container has no command", w.name)
			continue
		}
		binary := filepath.Base(cmd[0])
		if binary == "docker-entrypoint.sh" {
			checkPostgres(t, w, main, args)
			continue
		}
		// Exemplar's binaries serve /healthz and /readyz on --addr (default
		// :8080) as the http port; cloudflared serves its own on --metrics.
		portName, addrFlag, addr, live, ready := "http", "--addr=", ":8080", "/healthz", "/readyz"
		if binary == "cloudflared" {
			portName, addrFlag, addr, live, ready = "metrics", "--metrics=", "", "/healthcheck", "/ready"
		}
		port := 0
		for _, p := range list(main, "ports") {
			if str(p, "name") == portName {
				port = int(as[uint64](t, get(p, "containerPort"), w.name+": containerPort"))
			}
		}
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, addrFlag); ok {
				addr = v
			}
		}
		for _, e := range list(main, "env") {
			if str(e, "name") == "EXEMPLAR_GATEWAY_ADDR" {
				addr = str(e, "value")
			}
		}
		if _, p, _ := strings.Cut(addr, ":"); port == 0 || p != strconv.Itoa(port) {
			t.Errorf("%s: listens on %q but exposes %s port %d", w.name, addr, portName, port)
		}
		if str(main, "livenessProbe", "httpGet", "path") != live || str(main, "readinessProbe", "httpGet", "path") != ready ||
			str(main, "livenessProbe", "httpGet", "port") != portName || str(main, "readinessProbe", "httpGet", "port") != portName {
			t.Errorf("%s: probes must be %s and %s on the %s port", w.name, live, ready, portName)
		}
		checkGrace(t, w, binary, args)
	}
	covered := map[string]bool{}
	for _, d := range docs {
		kind, name := str(d, "kind"), str(d, "metadata", "name")
		var sel obj
		switch kind {
		case "Service":
			sel, _ = get(d, "spec", "selector").(obj)
		case "PodDisruptionBudget":
			sel, _ = get(d, "spec", "selector", "matchLabels").(obj)
		case "NetworkPolicy":
			sel, _ = get(d, "spec", "podSelector", "matchLabels").(obj)
			if len(sel) == 0 {
				continue // the namespace-wide default-deny and DNS rules
			}
		default:
			continue
		}
		hit := false
		for _, w := range ws {
			if matches(sel, w.labels) {
				hit = true
				if kind == "NetworkPolicy" {
					covered[w.name] = true
				}
			}
		}
		if !hit {
			t.Errorf("%s %s selects no workload", kind, name)
		}
		if kind == "Service" {
			want := "http"
			if name == "exemplar-postgres" {
				want = "postgres"
			}
			for _, p := range list(d, "spec", "ports") {
				if str(p, "targetPort") != want {
					t.Errorf("service %s targets %v, not the named %s port", name, get(p, "targetPort"), want)
				}
			}
		}
	}
	for _, w := range ws {
		if !covered[w.name] {
			t.Errorf("%s has no NetworkPolicy of its own", w.name)
		}
	}
}

// checkGrace checks that w's terminationGracePeriodSeconds outlasts its
// binary's shutdown.
func checkGrace(t *testing.T, w workload, binary string, args []string) {
	t.Helper()
	graceSeconds := as[uint64](t, get(w.spec, "terminationGracePeriodSeconds"), w.name+": terminationGracePeriodSeconds")
	grace := time.Duration(graceSeconds) * time.Second
	if need := shutdown(t, binary, args); grace <= need {
		t.Errorf("%s: terminationGracePeriodSeconds %s does not outlast shutdown %s", w.name, grace, need)
	}
}

// checkPostgres checks the optional in-cluster Postgres: it listens on the
// named postgres port, its probes run pg_isready against it, and a preStop
// fast shutdown finishes inside the grace period. SIGTERM alone is a smart
// shutdown, which waits for pooled clients forever.
func checkPostgres(t *testing.T, w workload, main obj, args []string) {
	t.Helper()
	port := uint64(0)
	for _, p := range list(main, "ports") {
		if str(p, "name") == "postgres" {
			port = as[uint64](t, get(p, "containerPort"), w.name+": containerPort")
		}
	}
	if port != 5432 {
		t.Errorf("%s: postgres port is %d, want 5432", w.name, port)
	}
	for _, probe := range []string{"startupProbe", "livenessProbe", "readinessProbe"} {
		cmd := list(main, probe, "exec", "command")
		if len(cmd) == 0 || cmd[0] != "pg_isready" || !slices.Contains(cmd, any("--port=5432")) {
			t.Errorf("%s: %s must run pg_isready on port 5432, got %v", w.name, probe, cmd)
		}
	}
	stop := list(main, "lifecycle", "preStop", "exec", "command")
	if len(stop) < 2 || stop[0] != "pg_ctl" || stop[1] != "stop" || !slices.Contains(stop, any("--mode=fast")) {
		t.Errorf("%s: preStop must be pg_ctl stop --mode=fast, got %v", w.name, stop)
	}
	for _, a := range stop {
		args = append(args, as[string](t, a, w.name+": preStop"))
	}
	checkGrace(t, w, "docker-entrypoint.sh", args)
}

// checkInbound checks how traffic gets in: the Ingresses there are, who the
// NetworkPolicies admit to the webhook and the API, the webhook's
// Postgres-only egress, and the tunnel's routes.
func checkInbound(t *testing.T, docs []obj, ingresses int, inbound []string) {
	t.Helper()
	services := map[string]bool{}
	for _, d := range docs {
		if str(d, "kind") == "Service" {
			for _, p := range list(d, "spec", "ports") {
				services[str(d, "metadata", "name")+":"+strconv.Itoa(int(as[uint64](t, get(p, "port"), "service port")))] = true
				services[str(d, "metadata", "name")+":"+str(p, "name")] = true
			}
		}
	}
	n := 0
	for _, d := range docs {
		if str(d, "kind") != "Ingress" {
			continue
		}
		n++
		for _, r := range list(d, "spec", "rules") {
			for _, p := range list(r, "http", "paths") {
				b := get(p, "backend", "service")
				if !services[str(b, "name")+":"+str(b, "port", "name")] {
					t.Errorf("ingress %s routes to unknown %s:%s", str(d, "metadata", "name"), str(b, "name"), str(b, "port", "name"))
				}
			}
		}
	}
	if n != ingresses {
		t.Errorf("ingresses = %d, want %d", n, ingresses)
	}

	for _, app := range []string{"exemplar-webhook", "exemplar-api"} {
		var from []string
		for _, d := range docs {
			if str(d, "kind") != "NetworkPolicy" || str(d, "spec", "podSelector", "matchLabels", "app.kubernetes.io/name") != app {
				continue
			}
			for _, rule := range list(d, "spec", "ingress") {
				for _, peer := range list(rule, "from") {
					switch {
					case get(peer, "namespaceSelector") != nil:
						from = append(from, "namespace "+str(peer, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name"))
					case get(peer, "podSelector") != nil:
						from = append(from, "pod "+str(peer, "podSelector", "matchLabels", "app.kubernetes.io/name"))
					default:
						from = append(from, "anything")
					}
				}
			}
			if app != "exemplar-webhook" {
				continue
			}
			// The webhook reaches Postgres and nothing else.
			eg := list(d, "spec", "egress")
			if str(d, "metadata", "name") != app {
				if len(eg) > 0 {
					t.Errorf("NetworkPolicy %s adds webhook egress %v", str(d, "metadata", "name"), eg)
				}
				continue
			}
			if len(eg) != 1 || len(list(eg[0], "ports")) != 1 || get(list(eg[0], "ports")[0], "port") != uint64(5432) {
				t.Errorf("webhook egress = %v", eg)
			}
		}
		slices.Sort(from)
		if !slices.Equal(from, inbound) {
			t.Errorf("%s admits %v, want %v", app, from, inbound)
		}
	}

	// Every tunnel route goes to a Service port, and the last one is the
	// catch-all, so an unknown host never reaches a pod.
	for _, d := range docs {
		if str(d, "kind") != "ConfigMap" || str(d, "metadata", "name") != "exemplar-cloudflared" {
			continue
		}
		var cfg obj
		if err := yaml.Unmarshal([]byte(str(d, "data", "config.yaml")), &cfg); err != nil {
			t.Fatalf("exemplar-cloudflared config.yaml: %v", err)
		}
		rules := list(cfg, "ingress")
		if len(rules) == 0 || str(rules[len(rules)-1], "hostname") != "" || !strings.HasPrefix(str(rules[len(rules)-1], "service"), "http_status:") {
			t.Errorf("tunnel routes must end with an http_status catch-all: %v", rules)
		}
		for _, r := range rules[:max(len(rules)-1, 0)] {
			u, ok := strings.CutPrefix(str(r, "service"), "http://")
			host, port, _ := strings.Cut(u, ":")
			name, ns, _ := strings.Cut(host, ".")
			if !ok || str(r, "hostname") == "" || !strings.HasPrefix(ns, "exemplar.svc") || !services[name+":"+port] {
				t.Errorf("tunnel route %v is not a host to an exemplar Service port", r)
			}
		}
	}
}

// envNames are the EXEMPLAR_* names the Go and Python code reads.
func envNames(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	re := regexp.MustCompile(`"(EXEMPLAR_[A-Z0-9_]+)"`)
	for _, root := range []string{"../cmd", "../internal", "../learning/src"} {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, ".py") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, m := range re.FindAllStringSubmatch(string(data), -1) {
				out[m[1]] = true
			}
			return nil
		})
	}
	if len(out) < 10 {
		t.Fatalf("found only %d env names", len(out))
	}
	return out
}
