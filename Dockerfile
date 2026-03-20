FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-X main.version=${VERSION}" -o /opdev ./src

FROM scratch AS export
COPY --from=build /opdev /opdev

FROM scratch
COPY --from=build /opdev /opdev
ENTRYPOINT ["/opdev"]
