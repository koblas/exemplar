"""Command line for the learning jobs (spec §8.3-8.5).

- ``check``: verify database access and schema version (a CronJob's first step).
- ``cluster``: cluster embedded extractions and write a run snapshot (§8.4).

- ``rank``: score the latest cluster run into owned candidates (§8.5).

``embed`` joins them once an embedding provider is approved.
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import sys
from dataclasses import asdict

import psycopg

from exemplar_learning import db
from exemplar_learning.cluster import graph, job
from exemplar_learning.cluster.judge import GatewayJudge
from exemplar_learning.rank import job as rank_job
from exemplar_learning.rank import score


def _check(_: argparse.Namespace) -> int:
    with db.connect() as conn:
        version = db.check_schema(conn)
        pending_embedding = conn.execute(
            "SELECT count(*) FROM extractions WHERE embedding IS NULL"
        ).fetchone()[0]
    print(
        json.dumps({"schema_version": version, "extractions_without_embedding": pending_embedding})
    )
    return 0


def _cluster(args: argparse.Namespace) -> int:
    url, token = (
        os.environ.get("EXEMPLAR_GATEWAY_URL", ""),
        os.environ.get("EXEMPLAR_GATEWAY_TOKEN", ""),
    )
    if not url or not token:
        raise RuntimeError("EXEMPLAR_GATEWAY_URL and EXEMPLAR_GATEWAY_TOKEN are required")
    judge = GatewayJudge(url, token)
    params = graph.Params(
        k=args.k,
        edge_threshold=args.edge_threshold,
        seed=args.seed,
        merge_threshold=args.merge_threshold,
    )
    with (
        db.connect() as conn,
        psycopg.connect(
            db.database_url(), autocommit=True, application_name="exemplar-learning-cache"
        ) as cache_conn,
    ):
        db.check_schema(conn)
        conn.commit()
        rep = job.run(conn, cache_conn, judge, lambda: judge.model, params)
    print(json.dumps(asdict(rep)))
    return 0


def _rank(args: argparse.Namespace) -> int:
    params = score.Params(
        min_cluster_size=args.min_cluster_size,
        half_life_days=args.half_life_days,
        default_owner=args.default_owner,
    )
    with db.connect() as conn:
        db.check_schema(conn)
        conn.commit()
        rep = rank_job.run(conn, params)
    print(json.dumps(asdict(rep)))
    return 0


def main(argv: list[str] | None = None) -> int:
    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s: %(message)s"
    )
    parser = argparse.ArgumentParser(prog="exemplar-learning", description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("check", help="verify database access and schema version").set_defaults(
        func=_check
    )
    c = sub.add_parser("cluster", help="cluster embedded extractions (spec §8.4)")
    c.add_argument("--k", type=int, default=graph.K_NEIGHBOURS, help="neighbours per extraction")
    c.add_argument(
        "--edge-threshold", type=float, default=graph.EDGE_THRESHOLD, help="cosine for a graph edge"
    )
    c.add_argument(
        "--seed", type=int, default=graph.SEED, help="Leiden seed (fixed for determinism)"
    )
    c.add_argument(
        "--merge-threshold",
        type=float,
        default=graph.MERGE_THRESHOLD,
        help="centroid cosine to ask the judge",
    )
    c.set_defaults(func=_cluster)
    r = sub.add_parser("rank", help="score the latest cluster run into candidates (spec §8.5)")
    r.add_argument("--min-cluster-size", type=int, default=score.Params.min_cluster_size)
    r.add_argument("--half-life-days", type=float, default=score.Params.half_life_days)
    r.add_argument(
        "--default-owner",
        default=score.Params.default_owner,
        help="owner when CODEOWNERS gives none or ties",
    )
    r.set_defaults(func=_rank)
    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except (db.SchemaTooOldError, rank_job.NoClusterRunError, RuntimeError) as err:
        print(f"error: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
