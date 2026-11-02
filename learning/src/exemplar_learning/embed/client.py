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
    """Embeds document texts; callable as ``embed(texts) -> vectors``.

    ``model`` is the gateway's embedding model (GET /v1/embeddings/model),
    fetched on first use. A response from any other model raises
    GatewayError: providers.embed changed mid-run, and the next run starts
    over with the new model.
    """

    def __init__(self, gateway_url: str, gateway_token: str, timeout: float = 120.0) -> None:
        if not gateway_url.startswith(("http://", "https://")):
            raise ValueError("EXEMPLAR_GATEWAY_URL must be an http(s) URL")
        self.url = gateway_url.rstrip("/") + "/v1/embeddings"
        self.token = gateway_token
        self.timeout = timeout
        self._model = ""

    @property
    def model(self) -> str:
        if not self._model:
            body = self._send(urllib.request.Request(self.url + "/model", method="GET"))  # noqa: S310
            if not body.get("model") or body.get("dimensions") != DIMENSIONS:
                raise GatewayError(f"gateway embeddings: unusable model {body}")
            self._model = str(body["model"])
        return self._model

    def _send(self, req: urllib.request.Request) -> dict:
        req.add_header("X-Api-Key", self.token)
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:  # noqa: S310 - the scheme is checked in __init__
                return json.load(resp)
        except urllib.error.HTTPError as err:
            kind, message = _error(err)
            if err.code == 422 and kind == "exemplar_unredacted_secret":
                raise RefusedError(message) from err
            raise GatewayError(f"gateway embeddings: HTTP {err.code} {kind}: {message}") from err
        except (urllib.error.URLError, TimeoutError) as err:
            raise GatewayError(f"gateway embeddings: {err}") from err

    def __call__(self, texts: list[str]) -> list[list[float]]:
        model = self.model
        req = urllib.request.Request(  # noqa: S310 - the scheme is checked in __init__
            self.url,
            data=json.dumps({"input": texts}).encode(),
            method="POST",
            headers={
                "Content-Type": "application/json",
                "X-Exemplar-Stage": "embed",
                "X-Exemplar-Caller": "learning",
                "X-Exemplar-Data-Classes": "review_comments",
            },
        )
        body = self._send(req)
        if body.get("model") != model:
            raise GatewayError(
                f"gateway embeddings: answered with {body.get('model')!r}, expected {model!r}"
            )
        data = sorted(body.get("data", []), key=lambda d: d["index"])
        vectors = [d["embedding"] for d in data]
        if len(vectors) != len(texts) or any(len(v) != DIMENSIONS for v in vectors):
            raise GatewayError(
                f"gateway embeddings: want {len(texts)} vectors of {DIMENSIONS}, got "
                f"{[len(v) for v in vectors]}"
            )
        return vectors


def _error(err: urllib.error.HTTPError) -> tuple[str, str]:
    try:
        e = json.loads(err.read()).get("error", {})
        return str(e.get("type", "")), str(e.get("message", ""))
    except (ValueError, AttributeError, OSError):
        return "", ""
