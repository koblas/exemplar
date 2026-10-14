"""Candidate score and owner (spec §8.5).

    score = log(1 + cluster_size) · (1 + distinct_reviewers)
            · recency_weight · resolution_weight · doc_boost · incident_boost

The spec fixes the shape, doc_boost (1.5) and incident_boost (2.0). The
other terms are not defined there; the choices below are deliberate, live
in Params so they can be tuned against reviewer judgement, and are recorded
with every score in score_detail.
"""

from __future__ import annotations

import math
import statistics
from collections import Counter
from dataclasses import asdict, dataclass
from datetime import datetime


@dataclass(frozen=True)
class Params:
    # Accepted bot findings count for less than human comments (PRD FR-L1.2).
    bot_source_weight: float = 0.5
    # Recency: exponential decay on the median source age, never below the floor.
    half_life_days: float = 365.0
    recency_floor: float = 0.25
    # Resolution: 0.5 when no source thread was resolved, 1.0 when all were.
    resolution_base: float = 0.5
    doc_boost: float = 1.5  # spec
    incident_boost: float = 2.0  # spec
    # Clusters smaller than this do not become candidates.
    min_cluster_size: int = 2
    # Owner when CODEOWNERS gives none or ties (spec: ties go to the platform team).
    default_owner: str = "@platform/exemplar"


@dataclass(frozen=True)
class Source:
    """One source comment of a cluster, with what ranking needs to know."""

    comment_id: int
    reviewer: str
    created_at: datetime
    resolved: bool
    has_doc_link: bool
    incident_linked: bool  # revert seed, or its path has an incident/revert event
    bot_finding: bool
    owner: str | None


@dataclass(frozen=True)
class Score:
    value: float
    weighted_size: float
    distinct_reviewers: int
    recency: float
    resolution: float
    doc: float
    incident: float
    median_age_days: float

    def detail(self, params: Params) -> dict:
        return {
            "score": self.value,
            "factors": {
                "weighted_size": self.weighted_size,
                "distinct_reviewers": self.distinct_reviewers,
                "recency": self.recency,
                "resolution": self.resolution,
                "doc_boost": self.doc,
                "incident_boost": self.incident,
            },
            "median_age_days": self.median_age_days,
            "params": asdict(params),
        }


def score(sources: list[Source], now: datetime, params: Params = Params()) -> Score:
    if not sources:
        raise ValueError("a cluster has at least one source")
    weighted = sum(params.bot_source_weight if s.bot_finding else 1.0 for s in sources)
    reviewers = len({s.reviewer for s in sources})
    ages = [max(0.0, (now - s.created_at).total_seconds() / 86400) for s in sources]
    median_age = statistics.median(ages)
    recency = max(params.recency_floor, 0.5 ** (median_age / params.half_life_days))
    resolved = sum(s.resolved for s in sources) / len(sources)
    resolution = params.resolution_base + (1 - params.resolution_base) * resolved
    doc = params.doc_boost if any(s.has_doc_link for s in sources) else 1.0
    incident = params.incident_boost if any(s.incident_linked for s in sources) else 1.0
    value = math.log1p(weighted) * (1 + reviewers) * recency * resolution * doc * incident
    return Score(value, weighted, reviewers, recency, resolution, doc, incident, median_age)


def owner(sources: list[Source], params: Params = Params()) -> str:
    """Majority CODEOWNERS owner of the source files; none or a tie goes to
    the platform team."""
    counts = Counter(s.owner for s in sources if s.owner)
    if not counts:
        return params.default_owner
    ranked = counts.most_common()
    if len(ranked) > 1 and ranked[0][1] == ranked[1][1]:
        return params.default_owner
    return ranked[0][0]
