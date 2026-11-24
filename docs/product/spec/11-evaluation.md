## 11. Evaluation

Evaluation is specified with the learning plane: eval generation (§8.8), eval CI (§8.9), the replay harness and baseline comparison (§8.8, §16.1), verifier calibration (§6.8), and the change gate made of the incident set and the clean-PR set (§8.8, FR-X11–X12). The shadow exit (§16) is an **adjudicated sample**: owners label a random 200 shadow findings as right, wrong or unimportant through the web UI's finding page (the labels land in `verifier_labels` with `source='human_review'`), and precision is computed on those labels, not against human comments, since a correct finding no reviewer made would otherwise count against the bot; injected-violation recall (FR-X6) per active rule is the other half.

---
