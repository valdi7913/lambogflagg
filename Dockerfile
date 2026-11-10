# Build
FROM golang:1.27-alpine AS build
WORKDIR /src
RUN go install github.com/a-h/templ/cmd/templ@v0.3.1100
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN templ generate && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bookclub .

# Run
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/bookclub /bookclub
ENV ADDR=:8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/bookclub"]
