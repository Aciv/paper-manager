FROM python:3.12-slim
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
WORKDIR /app/pdf_parser
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PYTHONPATH=/app:/app/pdf_parser
COPY pdf_parser/requirements.txt ./requirements.txt
# Do not persist a developer proxy into the parser runtime. Build arguments
# override Docker Desktop's potentially stale lowercase proxy configuration.
RUN HTTP_PROXY="$HTTP_PROXY" HTTPS_PROXY="${HTTPS_PROXY:-$HTTP_PROXY}" NO_PROXY="$NO_PROXY" \
    http_proxy="$HTTP_PROXY" https_proxy="${HTTPS_PROXY:-$HTTP_PROXY}" no_proxy="$NO_PROXY" \
    pip install --no-cache-dir -r requirements.txt
# Fail the image build if the worker runtime is missing; a healthy gRPC server
# alone does not prove that recognize/render workers can start.
RUN python -c "import pymupdf, pymupdf4llm"
COPY proto /app/proto
COPY pdf_parser /app/pdf_parser
# Runtime imports the exact pure feature functions used during training, but
# artifacts are mounted read-only by Compose rather than baked into the image.
COPY model_train/paper_crf /app/model_train/paper_crf
# Generated stubs are checked in, but regenerate them during image construction
# so the image always matches the contract it receives.
RUN python -m grpc_tools.protoc -I/app/proto --python_out=. --grpc_python_out=. /app/proto/document_parser.proto
RUN addgroup --gid 10001 --system parser && adduser --uid 10001 --system --ingroup parser --home /app parser \
    && chown -R parser:parser /app
USER parser
EXPOSE 50051
ENTRYPOINT ["python", "-m", "papers_parser.server"]
