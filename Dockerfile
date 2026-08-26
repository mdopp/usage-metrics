# usage-metrics — small static Go binary, lean runtime image.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /out/usage-metrics .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/usage-metrics /usage-metrics
EXPOSE 8080
ENTRYPOINT ["/usage-metrics"]
