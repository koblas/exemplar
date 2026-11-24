<a id="2-problem-statement"></a>

## 2. Problem Statement

| Symptom | Consequence |
|---|---|
| Code generated per engineer up ~10x | Review queues grow; lead time flat |
| "Human in the loop" applied to every PR | Senior engineers become full-time reviewers |
| Generic AI reviewer | ~21% finding acceptance — worse than no bot, because it trains engineers to ignore it |
| Review judgment lives only in past comments | Not in any style guide; style guides go stale within weeks |
| AI agents write code without org context | The same violations are generated, reviewed and fixed over and over |

<a id="21-constraints-that-rule-out-easy-answers"></a>

### 2.1 Constraints that rule out easy answers

1. **Judgment isn't written down.** It lives in millions of review comments that are mostly praise, typo fixes, and shorthand. *Rules out:* hand-writing all of it. *(v0.9)* It does not rule out an owner writing a rule by hand when they know one (FR-L1.8): a sentence from the person who owns the code can be worth a hundred mined comments, and it goes through the same gate and evals.
2. **Trust is asymmetric.** Engineers stop reading a noisy bot within a week. *Rules out:* ship first, tune later.
3. **Everything the model reads is untrusted.** Diffs, PR descriptions, repo files, and historical comments are all natural language and all injectable. *Rules out:* letting the model vouch for its own output.
4. **Scale is thousands of engineers and hundreds of rules.** One giant prompt loses attention; unbounded fan-out blows the budget. *Rules out:* both naive architectures.
5. **Build-only.** No commercial reviewer as a fallback or baseline. *Rules out:* outsourcing general bug detection; requires an in-house baseline to measure against.
6. **Self-contained, no builds.** No external code search service or vendor, reviews never run a build tool or depend on build artifacts, and the main codebase is a large monorepo. Open-source components may be embedded if they run inside our own containers. *Rules out:* hosted search, Bazel/compiler-based code navigation at review time, and naive full-checkout-plus-grep per review. *(v0.10)* What that last item rules out is cloning the monorepo for every query. A small installation may leave the search container out (FR-E12): each review already holds a checkout of the one repository under review, searching it is bounded by that repository, and the review says plainly that nothing beyond it was searched.

<a id="22-two-kinds-of-false-positive-borrowed-from-uber-ureview"></a>

### 2.2 Two kinds of false positive (borrowed from Uber uReview)

| Class | Example | Primary defense |
|---|---|---|
| **Wrong** — hallucinated or factually incorrect | Claims a call blocks when it is already awaited | Verification stage (§8.4) with adversarial refutation and code evidence |
| **Right but unimportant here** | Valid performance note in code that is not performance-sensitive | Importance check in verification, rule scoping, path-level suppression, comment budget |

Every design choice below should be traceable to reducing one of these two classes or to measuring them.
