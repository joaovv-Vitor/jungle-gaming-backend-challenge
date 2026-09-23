FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/sqs-gateway ./cmd/sqs-gateway
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/sqs-gateway ./cmd/sqs-gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sqs-gateway /sqs-gateway
USER nonroot:nonroot
ENTRYPOINT ["/sqs-gateway"]
