# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ENV HTTP_PROXY=$HTTP_PROXY HTTPS_PROXY=$HTTPS_PROXY NO_PROXY=$NO_PROXY
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/papers-api ./cmd/server

FROM scratch
WORKDIR /app
COPY --from=build /out/papers-api /app/papers-api
EXPOSE 8080
ENTRYPOINT ["/app/papers-api"]
