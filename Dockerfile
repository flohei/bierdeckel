FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /bierdeckel .

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /bierdeckel /bierdeckel
ENV BIERDECKEL_DATA=/data BIERDECKEL_ADDR=:8080
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/bierdeckel"]
