"""Graph clustering on synthetic embeddings with known structure."""

from __future__ import annotations

import numpy as np
import pytest

from exemplar_learning.cluster import graph


def blobs(centers: list[np.ndarray], per: int, noise: float, seed: int = 0) -> np.ndarray:
    rng = np.random.default_rng(seed)
    rows = [c + noise * rng.standard_normal((per, len(c))) for c in centers]
    return graph.normalize(np.vstack(rows))


def axis(dim: int, i: int) -> np.ndarray:
    v = np.zeros(dim, dtype=np.float32)
    v[i] = 1.0
    return v


def test_separated_groups_become_clusters() -> None:
    vecs = blobs([axis(32, 0), axis(32, 1), axis(32, 2)], per=15, noise=0.05)
    clusters = graph.cluster(vecs)
    assert sorted(len(c) for c in clusters) == [15, 15, 15]
    # Each cluster holds one generated group.
    for c in clusters:
        assert len({i // 15 for i in c}) == 1


def test_deterministic() -> None:
    vecs = blobs([axis(32, 0), axis(32, 1)], per=30, noise=0.2)
    assert graph.cluster(vecs) == graph.cluster(vecs)


def test_dissimilar_vectors_stay_singletons() -> None:
    vecs = graph.normalize(np.eye(8, dtype=np.float32))  # all cosines are 0
    assert graph.cluster(vecs) == [[i] for i in range(8)]


def test_edges_respect_threshold_and_are_undirected() -> None:
    vecs = blobs([axis(16, 0), axis(16, 1)], per=5, noise=0.05)
    edges = graph.knn_edges(vecs, k=20, threshold=0.65)
    assert edges, "expected edges within each group"
    for a, b, w in edges:
        assert a < b
        assert w >= 0.65
        assert a // 5 == b // 5, "no edge should cross the orthogonal groups"


def test_oversized_community_is_resplit(monkeypatch: pytest.MonkeyPatch) -> None:
    # Two sub-groups 45 degrees apart (cosine ~0.71): linked at the 0.65 edge
    # threshold but not at the 0.75 re-split threshold. Force the first pass
    # to return one oversized community, as a chain would in real data, and
    # check the re-split separates it using only the stricter edges.
    a = axis(16, 0)
    b = graph.normalize((axis(16, 0) + axis(16, 1))[None, :])[0]
    vecs = blobs([a, b], per=12, noise=0.01)
    assert any(a // 12 != b // 12 for a, b, _ in graph.knn_edges(vecs, 20, 0.65))
    assert all(a // 12 == b // 12 for a, b, _ in graph.knn_edges(vecs, 20, 0.75))

    real_leiden = graph.leiden
    calls = []

    def first_pass_one_community(n: int, edges: list, seed: int) -> list[list[int]]:
        calls.append(len(edges))
        if len(calls) == 1:
            return [list(range(n))]
        return real_leiden(n, edges, seed)

    monkeypatch.setattr(graph, "leiden", first_pass_one_community)
    split = graph.cluster(vecs, graph.Params(max_community=20))
    assert sorted(len(c) for c in split) == [12, 12]
    assert len(calls) == 2, "the oversized community should be re-clustered once"

    calls.clear()
    whole = graph.cluster(vecs, graph.Params(max_community=1000))
    assert len(whole) == 1 and len(calls) == 1, "small enough communities are not re-split"


def test_most_central_orders_by_similarity() -> None:
    vecs = graph.normalize(np.array([[1, 0], [1, 0.1], [1, 1], [0.2, 1]], dtype=np.float32))
    central = graph.most_central(vecs, [0, 1, 2, 3], 2)
    assert set(central) <= {0, 1, 2, 3} and len(central) == 2
    assert 3 not in central  # the outlier is least central
