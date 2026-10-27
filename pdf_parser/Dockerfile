FROM python:3.12-slim
ARG VERSION=1.0.1
ARG VCS_REF=unknown
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
WORKDIR /app/pdf_parser
LABEL org.opencontainers.image.title="paper_manager parser" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$VCS_REF \
      org.opencontainers.image.source="https://github.com/Aciv/paper-manager"
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PYTHONPATH=/app:/app/pdf_parser
COPY packaging/desktop/requirements-runtime.lock ./requirements-runtime.lock
# Do not persist a developer proxy into the parser runtime. Build arguments
# override Docker Desktop's potentially stale lowercase proxy configuration.
RUN HTTP_PROXY="$HTTP_PROXY" HTTPS_PROXY="${HTTPS_PROXY:-$HTTP_PROXY}" NO_PROXY="$NO_PROXY" \
    http_proxy="$HTTP_PROXY" https_proxy="${HTTPS_PROXY:-$HTTP_PROXY}" no_proxy="$NO_PROXY" \
    pip install --no-cache-dir --no-compile --only-binary=:all: --require-hashes \
    -r requirements-runtime.lock
# Fail the image build if the worker runtime is missing; a healthy gRPC server
# alone does not prove that recognize/render workers can start.
RUN python -c "import pymupdf, pymupdf4llm"
COPY proto /app/proto
COPY pdf_parser/papers_parser ./papers_parser
COPY pdf_parser/logger.py ./logger.py
COPY pdf_parser/paper_crf/__init__.py ./paper_crf/__init__.py
COPY pdf_parser/paper_crf/models ./paper_crf/models
COPY pdf_parser/paper_crf/specs ./paper_crf/specs
COPY pdf_parser/paper_crf/weights/header-text-title-priors.crfsuite \
     pdf_parser/paper_crf/weights/citation-context.crfsuite ./paper_crf/weights/
COPY packaging/desktop/dependencies.json /app/model-manifest.json
COPY packaging/docker/verify_parser.py /app/verify_parser.py
COPY LICENSE /licenses/paper_manager-MIT.txt
# Generated stubs are checked in, but regenerate them during image construction
# so the image always matches the contract it receives.
RUN python -m grpc_tools.protoc -I/app/proto --python_out=. --grpc_python_out=. /app/proto/document_parser.proto
RUN LOG_PATH=/dev/null python /app/verify_parser.py
RUN addgroup --gid 10001 --system parser && adduser --uid 10001 --system --ingroup parser --home /app parser \
    && chown -R parser:parser /app
USER parser
EXPOSE 50051
ENTRYPOINT ["python", "-m", "papers_parser.server"]
