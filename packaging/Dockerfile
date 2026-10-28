# A container with every AX program, for a host that would rather not install
# anything. The image is the same binaries the release publishes.
#
#   docker build -f packaging/Dockerfile -t ax .
#   docker run --rm -it -v "$PWD/bot:/bot" -e DEEPSEEK_API_KEY ax ax -C /bot "..."
#
# Nothing here needs a shell at runtime: distroless has the CA certificates the
# model API needs and nothing else.
FROM golang:1.27 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
ENV HOME=/home/nonroot
WORKDIR /home/nonroot
ENTRYPOINT ["ax"]
