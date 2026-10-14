"""GatewayJudge against a local stand-in for exemplar-gateway."""

from __future__ import annotations

import json
import threading
from collections.abc import Iterator
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from exemplar_learning.cluster.judge import GatewayJudge
from exemplar_learning.cluster.merge import Summary


class FakeGateway(BaseHTTPRequestHandler):
    reply: dict = {}
    seen: list[tuple[dict, dict]] = []

    def do_POST(self) -> None:  # noqa: N802 - http.server API
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        FakeGateway.seen.append((dict(self.headers), body))
        data = json.dumps(FakeGateway.reply).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *_: object) -> None:
        pass


def message(text: str, stop: str = "end_turn") -> dict:
    return {
        "id": "msg_1",
        "type": "message",
        "role": "assistant",
        "model": "claude-opus-5-5",
        "content": [{"type": "text", "text": text}],
        "stop_reason": stop,
        "stop_sequence": None,
        "usage": {"input_tokens": 10, "output_tokens": 5},
    }


@pytest.fixture
def gateway() -> Iterator[str]:
    server = HTTPServer(("127.0.0.1", 0), FakeGateway)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    FakeGateway.seen = []
    yield f"http://127.0.0.1:{server.server_port}"
    server.shutdown()


A = Summary("ha", (("Set timeouts on outbound calls", "Calls need timeouts."),))
B = Summary("hb", (("Use deadlines for RPCs", "RPCs need deadlines."),))


def test_same_verdict(gateway: str) -> None:
    FakeGateway.reply = message(
        json.dumps(
            {
                "same": True,
                "merged_title": "Bound outbound calls",
                "merged_description": "Use timeouts.",
            }
        )
    )
    judge = GatewayJudge(gateway, "gw-token")
    d = judge(A, B)
    assert d is not None and d.same and d.merged_title == "Bound outbound calls"
    assert judge.model == "claude-opus-5-5"

    headers, body = FakeGateway.seen[0]
    lower = {k.lower(): v for k, v in headers.items()}
    assert lower["x-api-key"] == "gw-token"
    assert lower["x-exemplar-stage"] == "generator" and lower["x-exemplar-tier"] == "high"
    assert lower["x-exemplar-caller"] == "learning"
    assert lower["x-exemplar-data-classes"] == "review_comments"
    assert body["output_config"]["format"]["type"] == "json_schema"
    assert "untrusted" in body["system"]


def test_not_same(gateway: str) -> None:
    FakeGateway.reply = message(
        json.dumps({"same": False, "merged_title": "", "merged_description": ""})
    )
    d = GatewayJudge(gateway, "t")(A, B)
    assert d is not None and not d.same


def test_refusal_is_undecided(gateway: str) -> None:
    FakeGateway.reply = message("", stop="refusal")
    assert GatewayJudge(gateway, "t")(A, B) is None


def test_same_without_usable_rule_is_undecided(gateway: str) -> None:
    FakeGateway.reply = message(
        json.dumps({"same": True, "merged_title": "", "merged_description": ""})
    )
    assert GatewayJudge(gateway, "t")(A, B) is None


def test_malformed_answer_is_undecided(gateway: str) -> None:
    FakeGateway.reply = message('{"same": "maybe"}')
    assert GatewayJudge(gateway, "t")(A, B) is None
