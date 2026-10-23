# Build stage: has the Go toolchain, thrown away afterwards.
FROM golang:1.26 AS build
WORKDIR /src
# Dependencies first: this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal ./internal
# snapshot embeds the status page.
COPY site ./site
# CGO off gives a static binary that runs on the tiny image below.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /satnogs-pipeline .

# Run stage: no shell, no package manager, but CA certificates (the program
# talks HTTPS to SatNOGS) and a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /satnogs-pipeline /satnogs-pipeline
ENTRYPOINT ["/satnogs-pipeline"]
