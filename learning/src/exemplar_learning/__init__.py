"""Exemplar learning-plane batch jobs: embed, cluster and rank.

The jobs read and write Postgres tables written by the Go services and the
extraction driver; they expose no API. Contracts shared with Go come from
the JSON Schemas in ../schema via ``scripts/gen-contracts.sh``.
"""
