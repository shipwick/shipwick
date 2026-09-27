# Build a static binary, then ship only that, on an image with no shell and a
# non-root user.
FROM golang:{{.Go.Version}}-alpine AS build
WORKDIR /src
COPY go.mod {{if .Go.HasSum}}go.sum {{end}}./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/app {{.Go.Package}}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
EXPOSE {{.Port}}
ENTRYPOINT ["/app"]
