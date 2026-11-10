FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG SERVICE
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /server ./cmd/${SERVICE}

FROM alpine:3.20
COPY --from=build /server /server
EXPOSE 8080
CMD ["/server"]
