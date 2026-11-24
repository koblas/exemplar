<a id="appendix-a-design-principles"></a>

## Appendix A — Design Principles

From the source talk: - Not a model as the output — best practices plus infrastructure. - Learn offline. Enforce online. One human gate between them. - Cheap vectors prefilter. The LLM decides. A human merges. - One rule per agent. - Deterministic first. Expensive only if it all passes. - The model never vouches for itself. - A noisy rule is worse than no rule. - The asset is the encoded judgment, not the model that reads it.

Borrowed from the industry: - Precision is the product; measure false positives from the developer's point of view (Tricorder). - Try to disprove every finding before showing it (Anthropic Code Review, Uber uReview). - Don't let the same model grade its own work (Uber uReview). - If a check can be deterministic, make it deterministic (static-analysis practice; AutoCommenter's own findings). - Deliver feedback where the code is written, as early as possible (AutoCommenter, Meta diff-time analysis). - A fix is worth more than a comment (Google ML-suggested edits, Uber Fixer). - Best practices expire (AutoCommenter). - Review is also knowledge transfer; protect it (code-review research).
