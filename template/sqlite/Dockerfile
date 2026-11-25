FROM golang:1.27-alpine AS build
WORKDIR /src
ENV GOWORK=off
COPY go.* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/myapp .
RUN install -d -m 0700 -o 65532 -g 65532 /data

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build --chown=65532:65532 /data /data
COPY --from=build /out/myapp /myapp
ENV DB_PATH=/data/app.db
USER nonroot
EXPOSE 8080
# A one-off operator command can replace CMD without a shell or an entrypoint.
CMD ["/myapp"]
