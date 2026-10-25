"""Embeddings through exemplar-gateway.

The gateway serves POST /v1/embeddings for stage ``embed``: it picks the
model (providers.embed in global.yaml, Voyage), re-scans the texts for
secrets, and records the call in model_calls. The standard library's HTTP
client is enough for one JSON POST.
"""

from __future__ import annotations

import json
import urllib.error
import urllib.request

DIMENSIONS = 1024  # extractions.embedding is halfvec(1024)


class RefusedError(Exception):
    """The gateway refused the texts: they hold an unredacted secret."""


class GatewayError(RuntimeError):
    """The gateway could not embed the texts (no provider, provider error)."""


class GatewayEmbedder:
    """Embeds document texts; callable as ``embed(texts) -> vectors``."""

    def __init__(self, gateway_url: str, gateway_token: str, timeout: float = 120.0) -> None:
        if not gateway_url.startswith(("http://", "https://")):
            raise ValueError("EXEMPLAR_GATEWAY_URL must be an http(s) URL")
        self.url = gateway_url.rstrip("/") + "/v1/embeddings"
        self.token = gateway_token
        self.timeout = timeout
        self.model = ""

    def __call__(self, texts: list[str]) -> list[list[float]]:
        req = urllib.request.Request(  # noqa: S310 - the scheme is checked in __init__
            self.url,
            data=json.dumps({"input": texts}).encode(),
            method="POST",
            headers={
                "Content-Type": "application/json",
                "X-Api-Key": self.token,
                "X-Exemplar-Stage": "embed",
                "X-Exemplar-Caller": "learning",
                "X-Exemplar-Data-Classes": "review_comments",
            },
        )
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:  # noqa: S310
                body = json.load(resp)
        except urllib.error.HTTPError as err:
            kind, message = _error(err)
            if err.code == 422 and kind == "exemplar_unredacted_secret":
                raise RefusedError(message) from err
            raise GatewayError(f"gateway embeddings: HTTP {err.code} {kind}: {message}") from err
        except (urllib.error.URLError, TimeoutError) as err:
            raise GatewayError(f"gateway embeddings: {err}") from err
        data = sorted(body.get("data", []), key=lambda d: d["index"])
        vectors = [d["embedding"] for d in data]
        if len(vectors) != len(texts) or any(len(v) != DIMENSIONS for v in vectors):
            raise GatewayError(
                f"gateway embeddings: want {len(texts)} vectors of {DIMENSIONS}, got "
                f"{[len(v) for v in vectors]}"
            )
        self.model = body.get("model", "")
        return vectors


def _error(err: urllib.error.HTTPError) -> tuple[str, str]:
    try:
        e = json.loads(err.read()).get("error", {})
        return str(e.get("type", "")), str(e.get("message", ""))
    except (ValueError, AttributeError, OSError):
        return "", ""
