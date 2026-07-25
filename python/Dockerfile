FROM python:3.12-slim
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ENV HTTP_PROXY=$HTTP_PROXY HTTPS_PROXY=$HTTPS_PROXY NO_PROXY=$NO_PROXY
WORKDIR /app/python
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PYTHONPATH=/app/python
COPY python/requirements.txt ./requirements.txt
RUN pip install --no-cache-dir -r requirements.txt
COPY proto /app/proto
COPY python /app/python
# Generated stubs are checked in, but regenerate them during image construction
# so the image always matches the contract it receives.
RUN python -m grpc_tools.protoc -I/app/proto --python_out=. --grpc_python_out=. /app/proto/document_parser.proto
RUN addgroup --gid 10001 --system parser && adduser --uid 10001 --system --ingroup parser --home /app parser \
    && chown -R parser:parser /app
USER parser
EXPOSE 50051
ENTRYPOINT ["python", "-m", "papers_parser.server"]
