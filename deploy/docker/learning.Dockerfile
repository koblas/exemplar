# The Python learning-plane image (learning/: cluster, rank). Build from the
# repository root:
#
#   docker build -f deploy/docker/learning.Dockerfile -t exemplar-learning .
#
# The base is pinned by digest, uv by version, and the project's
# dependencies by learning/uv.lock (uv sync --frozen).

FROM python:3.13.15-slim-trixie@sha256:7c61056e61ac89e852de05f3dc6fa51a6dd2181797bceed46aa725dd7cb2cd3b
RUN pip install --no-cache-dir uv==0.12.11
WORKDIR /app
COPY learning/pyproject.toml learning/uv.lock learning/README.md ./
COPY learning/src ./src
ENV UV_PROJECT_ENVIRONMENT=/opt/venv UV_NO_CACHE=1 UV_PYTHON_DOWNLOADS=never
RUN uv sync --frozen --no-dev
ENV PATH=/opt/venv/bin:$PATH HOME=/tmp
USER 65532:65532
ENTRYPOINT ["exemplar-learning"]
