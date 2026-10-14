package bench

import (
	"fmt"
	"strings"
)

// Markdown renders the report for docs/ and the Phase 0 review.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Monorepo benchmark (T-0.4)\n\n")
	fmt.Fprintf(&b, "Repository `%s`, branch `%s`, run %s.\n\n", r.Repo, r.Branch, r.StartedAt.Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&b, "%d files, %s checked out.\n\n", r.Repository.Files, human(r.Repository.Bytes))

	b.WriteString("## Worker checkout (shallow, blobless, sparse)\n\n")
	fmt.Fprintf(&b, "| Run | Seconds |\n|---|---|\n| Cold | %.1f |\n| Warm | %.1f |\n\n", r.SparseClone.ColdSeconds, r.SparseClone.WarmSeconds)

	b.WriteString("## Mirror and index\n\n| Measure | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Mirror clone | %.1f s, %s |\n", r.Mirror.Seconds, human(r.Mirror.Bytes))
	fmt.Fprintf(&b, "| Full index (with ctags) | %.1f s, %s in %d shards |\n\n", r.FullIndex.Seconds, human(r.FullIndex.IndexBytes), r.FullIndex.Shards)

	if len(r.Incremental) > 0 {
		b.WriteString("## Incremental index\n\n| Commits back | Files changed | Seconds |\n|---|---|---|\n")
		for _, i := range r.Incremental {
			fmt.Fprintf(&b, "| %d | %d | %.1f |\n", i.CommitsBack, i.FilesChanged, i.Seconds)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Query latency\n\n| Query | Matches | p50 ms | p95 ms | max ms |\n|---|---|---|---|---|\n")
	for _, q := range r.Queries {
		fmt.Fprintf(&b, "| `%s` | %d | %.1f | %.1f | %.1f |\n", strings.ReplaceAll(q.Query, "|", `\|`), q.Matches, q.P50MS, q.P95MS, q.MaxMS)
	}
	b.WriteString("\n## ctags symbol coverage (v1 languages)\n\n| Language | Files | Sampled | With symbols | Coverage | Symbols/file |\n|---|---|---|---|---|---|\n")
	for _, c := range r.Ctags {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %.0f%% | %.1f |\n", c.Language, c.FilesInRepo, c.Sampled, c.WithSymbols, c.Coverage*100, c.SymbolsPerFile)
	}
	b.WriteString("\n## exemplar-search sizing\n\n")
	fmt.Fprintf(&b, "- Disk per replica: %s (%.1f× checked-out source; spec estimate 3–4×)\n", human(r.Sizing.DiskBytesPerReplica), r.Sizing.SourceMultiple)
	fmt.Fprintf(&b, "- RAM to keep every shard in page cache: %s\n", human(r.Sizing.RAMBytesForHotShards))
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "\nNote: %s\n", n)
	}
	return b.String()
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
