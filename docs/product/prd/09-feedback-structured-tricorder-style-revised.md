<a id="9-feedback-structured-tricorder-style-revised"></a>

## 9. Feedback — structured, Tricorder-style — *revised*

*(Borrowed: Google Tricorder's "Not useful" feedback with reasons, the ≤ 10% effective false-positive bar, and analyzer-owner accountability.)*

**Effective false positive** = a finding the author did not act on and/or marked not useful. It is measured from the developer's point of view, not by whether the finding was technically correct.

| ID | Requirement |
|---|---|
| FR-B1 | Every comment offers structured responses (links or slash commands in-thread): **Fixed / applied suggestion** , **Wrong** , **Not important here** , **Already handled elsewhere** , **Disagree with the rule** . 👍/👎 remain as low-effort secondary signals. |
| FR-B2 | Each dismissal reason routes differently: *Wrong* → verifier and prompt tuning; *Not important here* → scope/importance thresholds and path suppressions; *Already handled* → context providers; *Disagree with the rule* → owner review of the rule itself. |
| FR-B3 | Implicit signals are also captured: anchored lines changed in a later commit (with a light-model check that the change addresses the finding), suggestion applied, thread resolved, reply sentiment, ignored at merge. |
| FR-B4 | **Per-rule effective FP rate** is computed weekly. Any rule above **10%** over its last 30 findings is flagged to its owner; above the demotion threshold it is auto-demoted to `shadow` via an auto-merged config PR. Promotion always needs a human. |
| FR-B5 | **Owner accountability:** each rule owner gets a weekly digest; rules whose owners do not respond to a flag within two weeks are demoted. |
| FR-B6 | The same feedback model applies to compiled rules and to each bug-lane class. |
| FR-B7 | **Role-weighted feedback** — *new* . Dismissals are weighted by who gave them: PR author < another reviewer on the PR < the rule's owner. An author's *Wrong* or *Disagree with the rule* alone flags a rule for owner review but never drives auto-demotion (FR-B4); demotion requires the weighted effective-FP rate, which an author cannot reach by themselves. Owners can adjudicate any dismissal, and an adjudicated dismissal replaces the original weight. |
| FR-B8 | **Human agreement counts.** A finding that was withheld as a `human_duplicate` (FR-E15), or that a human reviewer independently also raised, is recorded as a true positive for the rule even though the bot did not post it. This keeps the dedupe from silently starving good rules of evidence. |
| FR-B9 (not built: #270) | **Learned importance priors** — *v0.9*. *Not important here* dismissals (FR-B2) accumulate per path prefix and category. When a prefix has at least 5 such dismissals from at least 3 people over 90 days for a category, the learning plane proposes a path-level importance prior (a lower threshold or a suppression for that category under that prefix) as a config PR, with the dismissals as citations. The verifier's per-finding importance (FR-V2) stays; this gives the second false-positive class (§2.2) the same feedback loop the first has, instead of waiting for someone to tune a threshold by hand. |
