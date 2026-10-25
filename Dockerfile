# tock — single-image build: Vue SPA -> static bundle -> Go binary.
FROM node:20-alpine AS spa
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM golang:1.24-alpine AS gobuild
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=spa /src/public/build ./internal/web/dist/build
COPY --from=spa /src/public/favicons ./internal/web/dist/favicons
COPY --from=spa /src/public/images ./internal/web/dist/images
COPY --from=spa /src/public/fonts ./internal/web/dist/fonts
COPY --from=spa /src/public/desktop-version ./internal/web/dist/desktop-version
COPY --from=spa /src/public/favicon.ico /src/public/robots.txt /src/public/security.txt ./internal/web/dist/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/tock ./cmd/tock

FROM gcr.io/distroless/static-debian12
COPY --from=gobuild /out/tock /tock
EXPOSE 8080
ENTRYPOINT ["/tock"]
CMD ["serve"]
