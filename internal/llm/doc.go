// Package llm is the model gateway: the hardcoded provider allowlist, stage
// routing with ordered failover, cost accounting against the soft ceiling,
// and the HTTP handler that is the only path from Exemplar to a model
// provider (spec §2.2, §6.14, §6.15, §13.1).
package llm
