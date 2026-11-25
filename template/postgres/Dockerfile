FROM golang:1.27-alpine AS build
WORKDIR /src
ENV GOWORK=off
COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/myapp .

# The database lives in a separate PostgreSQL server, so the image holds no
# data directory and needs no volume.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/myapp /myapp
USER nonroot
EXPOSE 8080
# A one-off operator command can replace CMD without a shell or an entrypoint.
CMD ["/myapp"]
