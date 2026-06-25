# Build context: agent-library  (root) — needs both web/ and examples/web/.
FROM node:20-slim AS build
WORKDIR /src
COPY web ./web
COPY examples/web ./examples/web
WORKDIR /src/examples/web
RUN corepack enable && yarn install --frozen-lockfile && yarn build

FROM nginx:1.27-alpine
COPY deploy/web.nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/examples/web/dist /usr/share/nginx/html
EXPOSE 8080
