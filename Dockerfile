FROM golang:1.27-alpine AS build

WORKDIR /app

COPY . .

RUN CGO_ENABLED=0 go build .

FROM scratch

COPY --from=build /app/bitbucketreviewer /bitbucketreviewer

ENTRYPOINT ["/bitbucketreviewer"]
